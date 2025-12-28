package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Service struct {
	Name      string            `yaml:"name"`
	Image     string            `yaml:"image"`
	GraftMode string            `yaml:"graft-mode,omitempty"`
	Port      int               `yaml:"port,omitempty"`
	Env       map[string]string `yaml:"env,omitempty"`
	Labels    []string          `yaml:"labels,omitempty"`
}

type Project struct {
	Name           string             `yaml:"name"`
	Domain         string             `yaml:"domain"`
	DeploymentMode string             `yaml:"-"` // Not exported to YAML, used for generation logic
	Services       map[string]Service `yaml:"services"`
}

func GenerateBoilerplate(name, domain, deploymentMode string) *Project {
	p := &Project{
		Name:           name,
		Domain:         domain,
		DeploymentMode: deploymentMode,
		Services: map[string]Service{
			"frontend": {
				Name:  "frontend",
				Image: "nginx:alpine",
			},
			"backend": {
				Name:  "backend",
				Image: "golang:alpine",
			},
		},
	}
	return p
}

func (p *Project) Save(dir string) error {
	filename := "graft-compose.yml"
	path := filepath.Join(dir, filename)
	
	// Determine the graft mode label based on deployment mode
	var graftMode string
	switch p.DeploymentMode {
	case "git-images":
		graftMode = "git-images"
	case "git-repo-serverbuild":
		graftMode = "git-repo-serverbuild"
	case "git-manual":
		graftMode = "git-manual"
	case "direct-localbuild":
		graftMode = "localbuild"
	case "direct-serverbuild":
		graftMode = "serverbuild"
	default:
		graftMode = "serverbuild" // Default to serverbuild for backward compatibility
	}
	
	// Generate a valid docker-compose.yml file that can be used directly
	template := `# Docker Compose Configuration for: %s
# Domain: %s
# Deployment Mode: %s
# This is a standard docker-compose.yml file - you can run it with:
# docker compose -f graft-compose.yml up -d

version: '3.8'


services:
  # Frontend Service (React/Vue/Angular/etc)
  frontend:
    # Build configuration
    build:
      context: ./frontend
      dockerfile: Dockerfile
    
    # Production volumes (uncomment for npm cache optimization)
    # volumes:
    #   - frontend-modules:/app/node_modules  # Separate for this service
    #   - npm-cache:/root/.npm                # Shared npm cache
    
    # Development: Mount source code for hot reload (comment out for production)
    # volumes:
    #   - ./frontend:/app
    #   - /app/node_modules
    
    # Working directory inside container
    # working_dir: /app
    
    # Development command (comment out for production, use CMD in Dockerfile instead)
    # command: npm run dev
    
    # Environment variables
    environment:
      - NODE_ENV=production
      - PORT=3000
    
    labels:
      # Graft deployment mode: git | git-repo-manual | localbuild | serverbuild
      - "graft.mode=%s"
      
      # Enable Traefik for this container
      - "traefik.enable=true"

      # 1. Define the Router (The "Entry" rule)
      # serves all requests to %s/
      - "traefik.http.routers.%s-frontend.rule=Host(` + "`%s`" + `)"
      - "traefik.http.routers.%s-frontend.priority=1"

      # 2. Define the Service (The "Destination")
      # This links the router above to the internal port 3000
      - "traefik.http.routers.%s-frontend.service=%s-frontend-service"
      - "traefik.http.services.%s-frontend-service.loadbalancer.server.port=3000"
      
      # 3. HTTPS / TLS Configuration (Uncomment these once DNS is pointed to the server)
      - "traefik.http.routers.%s-frontend.entrypoints=websecure"
      - "traefik.http.routers.%s-frontend.tls.certresolver=letsencrypt"
    
    networks:
      - graft-public
    
    restart: unless-stopped
    
    # Optional: Wait for backend to be ready
    # depends_on:
    #   - backend

  # Backend Service (Go/Node/Python/etc API)
  backend:
    # Build configuration
    build:
      context: ./backend
      dockerfile: Dockerfile
    
    # Production volumes (uncomment based on your backend language)
    # For Node.js/npm:
    # volumes:
    #   - backend-modules:/app/node_modules  # Separate for this service
    #   - npm-cache:/root/.npm               # Shared npm cache
    
    # For Go:
    # volumes:
    #   - go-mod-cache:/go/pkg/mod           # Shared Go module cache
    #   - go-build-cache:/root/.cache/go-build  # Shared Go build cache
    
    # For Python:
    # volumes:
    #   - pip-cache:/root/.cache/pip         # Shared pip cache
    
    # Development: Mount source code for hot reload (comment out for production)
    # volumes:
    #   - ./backend:/app
    
    # Working directory inside container
    # working_dir: /app
    
    # Development command (comment out for production)
    # command: go run main.go
    
    # Environment variables
    environment:
      # Database connection (uncomment after: graft db myproject init)
      # - DB_URL=${GRAFT_POSTGRES_MYPROJECT_URL}
      
      # Redis connection (uncomment after: graft redis mycache init)  
      # - REDIS_URL=${GRAFT_REDIS_MYCACHE_URL}
      
      # Application settings
      - PORT=5000
      - GIN_MODE=release
    
    labels:
      # Graft deployment mode
      - "graft.mode=%s"
      
      # Enable Traefik for this container
      - "traefik.enable=true"

      # 1. Define the Router (The "Entry" rule)
      # serves %s/api/* and strips /api prefix
      - "traefik.http.routers.%s-backend.rule=Host(` + "`%s`" + `) && PathPrefix(` + "`/api`" + `)"
      - "traefik.http.routers.%s-backend.priority=1"
      
      # 2. Define the Service & Middleware
      - "traefik.http.middlewares.%s-backend-strip.stripprefix.prefixes=/api" 
      - "traefik.http.routers.%s-backend.middlewares=%s-backend-strip" 
      - "traefik.http.routers.%s-backend.service=%s-backend-service"
      - "traefik.http.services.%s-backend-service.loadbalancer.server.port=5000"
      
      # 3. HTTPS / TLS Configuration (Uncomment these once DNS is pointed to the server)
      - "traefik.http.routers.%s-backend.entrypoints=websecure"
      - "traefik.http.routers.%s-backend.tls.certresolver=letsencrypt"
    
    networks:
      - graft-public
    
    restart: unless-stopped
    
    # Optional: Wait for database to be ready (uncomment if using DB)
    # depends_on:
    #   - postgres

# Persistent volumes
# Uncomment volumes as needed for your services
volumes:
  # Node.js: Separate node_modules for each service (prevents conflicts)
  # frontend-modules:
  # backend-modules:
  
  # Node.js: Shared npm cache (speeds up installs, reuses packages)
  # npm-cache:
  
  # Go: Shared module and build cache (faster builds)
  # go-mod-cache:
  # go-build-cache:
  
  # Python: Shared pip cache (faster installs)
  # pip-cache:

# Network configuration
networks:
  graft-public:
    external: true  # Created by 'graft host init'

# ============================================================================
# USAGE GUIDE
# ============================================================================
#
# Routing:
#   - %s/ → frontend (priority 1)
#   - %s/api/* → backend (strips /api prefix)
#   - Example: %s/api/users → backend receives /users
#
# Deployment Modes (graft.mode label):
#   Git-based modes:
#     - git-images: GitHub Actions builds and pushes to GHCR, automated deployment via webhook
#     - git-repo-serverbuild: GitHub Actions triggers server build, automated deployment via webhook
#     - git-manual: Git repository setup, manual deployment via graft sync (no CI/CD workflow)
#   Direct modes:
#     - localbuild: Build Docker image locally, upload to server
#     - serverbuild: Upload source, build Docker image on server
#
# Database Setup:
#   1. Run: graft db myproject init
#   2. Uncomment DB_URL line in backend environment
#   3. Uncomment depends_on for backend if needed
#
# Redis Setup:
#   1. Run: graft redis mycache init
#   2. Uncomment REDIS_URL line in backend environment
#
# Development vs Production:
#   - Development: Uncomment volumes, working_dir, and command
#   - Production: Use Dockerfile CMD, remove volumes
#
# HTTPS/SSL:
#   1. Ensure DNS points to your server
#   2. Uncomment entrypoints and tls.certresolver lines
#   3. Traefik will auto-request Let's Encrypt certificates
#
# Adding Services:
#   1. Copy a service block above
#   2. Update name, build context, and ports
#   3. Update Traefik labels (change service name)
#   4. Add to networks: [graft-public]
`
	
	content := fmt.Sprintf(template,
		p.Name, p.Domain, p.DeploymentMode, // Header info
		graftMode, // Frontend graft.mode
		p.Domain, p.Name, p.Domain, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, // Frontend: domain, name-router, domain-host, name-priority, name-router-service, name-service, name-service-port, name-router-entrypoints, name-router-tls
		graftMode, // Backend graft.mode
		p.Domain, p.Name, p.Domain, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, // Backend: domain, name-router, domain-host, name-priority, name-middleware, name-router-middleware, name-middleware-strip, name-router-service, name-service, name-service-port, name-router-entrypoints, name-router-tls
		p.Domain, p.Domain, p.Domain, // Footer
	)
	// Create env directory if it doesn't exist
	envDir := filepath.Join(dir, "env")
	if err := os.MkdirAll(envDir, 0755); err != nil {
		fmt.Printf("Warning: Could not create env directory: %v\n", err)
	}

	// Update .gitignore
	if err := EnsureGitignore(dir); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	return os.WriteFile(path, []byte(content), 0644)
}

// EnsureGitignore ensures that sensitive Graft files are added to .gitignore
func EnsureGitignore(dir string) error {
	gitignorePath := filepath.Join(dir, ".gitignore")
	gitignoreEntries := []string{"graft-compose.yml", ".graft/", "env/"}
	
	var existingContent string
	if data, err := os.ReadFile(gitignorePath); err == nil {
		existingContent = string(data)
	}

	lines := strings.Split(existingContent, "\n")
	newContent := existingContent
	modified := false

	for _, entry := range gitignoreEntries {
		found := false
		for _, line := range lines {
			if strings.TrimSpace(line) == entry {
				found = true
				break
			}
		}

		if !found {
			if len(newContent) > 0 && !strings.HasSuffix(newContent, "\n") {
				newContent += "\n"
			}
			newContent += entry + "\n"
			modified = true
		}
	}

	if modified {
		if err := os.WriteFile(gitignorePath, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("could not update .gitignore: %v", err)
		}
	}
	if modified {
		if err := os.WriteFile(gitignorePath, []byte(newContent), 0644); err != nil {
			return fmt.Errorf("could not update .gitignore: %v", err)
		}
	}
	return nil
}

// GenerateWorkflows creates .github/workflows directory and populates it with CI and Deploy templates
func GenerateWorkflows(p *Project, remoteURL string, mode string, webhook string) error {
	fmt.Println("received workflow mode: ", mode)
	workflowsDir := filepath.Join(".github", "workflows")
	if err := os.MkdirAll(workflowsDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflows directory: %v", err)
	}

	// Extract owner and repo from remote URL
	// Handles:
	// https://github.com/owner/repo.git
	// git@github.com:owner/repo.git
	ownerRepo := ""
	if strings.HasPrefix(remoteURL, "https://") {
		parts := strings.Split(strings.TrimSuffix(remoteURL, ".git"), "/")
		if len(parts) >= 2 {
			ownerRepo = parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	} else if strings.HasPrefix(remoteURL, "git@") {
		parts := strings.Split(strings.TrimSuffix(remoteURL, ".git"), ":")
		if len(parts) >= 2 {
			ownerRepo = parts[1]
		}
	}

	if ownerRepo == "" {
		ownerRepo = "username/repository" // fallback
	}

	// 1. Generate Deploy Workflow (for all git modes)
	if strings.HasPrefix(mode, "git") {
		var triggers string
		var condition string
		deployType := "image"
		
		if mode == "git-images" {
			triggers = `  workflow_run:
    workflows: ["CI/CD Pipeline"]
    types:
      - completed`
			condition = "if: ${{ github.event_name != 'workflow_run' || github.event.workflow_run.conclusion == 'success' }}"
		} else {
			triggers = `  push:
    branches: [ main, develop ]`
			condition = ""
			if mode == "git-repo-serverbuild" {
				deployType = "repo"
			}
		}

		// Prepare webhook URL
		hookURL := webhook
		if hookURL == "" {
			hookURL = "https://graft-hook.example.com"
		}
		if !strings.HasSuffix(hookURL, "/webhook") && !strings.Contains(hookURL, "/webhook?") {
			if strings.HasSuffix(hookURL, "/") {
				hookURL += "webhook"
			} else {
				hookURL += "/webhook"
			}
		}

		deployTemplate := `name: Deploy

on:
%%s
  release:
    types: [published]
  workflow_dispatch:

jobs:
  deploy:
    name: Deploy via Webhook
    runs-on: ubuntu-latest
    %%s
    environment: CI CD
    
    steps:
      - name: Send Webhook Request
        run: |
          curl -X POST %s \
            -H "Content-Type: application/json" \
            -d '{
              "project": "%%%%s",
              "repository": "${{ github.event.repository.name }}",
              "token": "${{ secrets.GITHUB_TOKEN }}",
              "user": "${{ github.actor }}",
              "type": "%s",
              "registry": "ghcr.io"
            }'
`
		deployPath := filepath.Join(workflowsDir, "deploy.yml")
		deployContent := fmt.Sprintf(deployTemplate, hookURL, deployType)
		// Now format the triggers and condition into the content (%%s -> %s)
		deployContent = fmt.Sprintf(deployContent, triggers, condition)
		// Now format the project name into the content (%%%%s -> %s)
		deployContent = fmt.Sprintf(deployContent, p.Name)
		
		if err := os.WriteFile(deployPath, []byte(deployContent), 0644); err != nil {
			return fmt.Errorf("failed to write deploy workflow: %v", err)
		}
	}

	// 2. Generate CI Workflow (only for git-images)
	if mode == "git-images" {
		ciTemplate := `name: CI/CD Pipeline

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main, develop ]
  workflow_dispatch:

env:
  REGISTRY: ghcr.io

jobs:
%s
`
		jobsContent := ""
		
		// Parse compose file to see which services have builds
		compose, err := ParseComposeFile("graft-compose.yml")
		if err != nil {
			return fmt.Errorf("failed to parse compose for workflow generation: %v", err)
		}

		for name, svc := range compose.Services {
			if svc.Build == nil {
				continue
			}

			imageName := ownerRepo + "/" + name
			context := svc.Build.Context
			dockerfile := svc.Build.Dockerfile
			if dockerfile == "" {
				dockerfile = "Dockerfile"
			}

			job := fmt.Sprintf(`  build-%s:
    name: Build %s Docker Image
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    
    steps:
      - name: Checkout code
        uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to GitHub Container Registry
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.REGISTRY }}/%s
          tags: |
            type=ref,event=branch
            type=ref,event=pr
            type=sha,prefix={{branch}}-
            type=raw,value=latest,enable={{is_default_branch}}

      - name: Build and push Docker image
        uses: docker/build-push-action@v5
        with:
          context: %s
          file: %s/%s
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
`, name, name, imageName, context, context, dockerfile)
			jobsContent += job + "\n"
		}

		if jobsContent != "" {
			ciPath := filepath.Join(workflowsDir, "ci.yml")
			ciContent := fmt.Sprintf(ciTemplate, jobsContent)
			if err := os.WriteFile(ciPath, []byte(ciContent), 0644); err != nil {
				return fmt.Errorf("failed to write ci workflow: %v", err)
			}
		}

		// 3. Generate Cleanup Workflow (only for git-images)
		cleanupTemplate := `name: Cleanup Old Images

on:
  schedule:
    - cron: '0 0 * * 0' # Weekly
  workflow_dispatch:

jobs:
  cleanup:
    runs-on: ubuntu-latest
    permissions:
      packages: write
    steps:
%s
`
		repoName := ""
		if parts := strings.Split(ownerRepo, "/"); len(parts) >= 2 {
			repoName = parts[1]
		} else {
			repoName = ownerRepo // fallback
		}

		cleanupSteps := ""
		for name := range compose.Services {
			if compose.Services[name].Build == nil {
				continue
			}
			packageName := fmt.Sprintf("%s/%s", repoName, name)
			step := fmt.Sprintf(`      - name: Delete old versions of %s
        uses: actions/delete-package-versions@v5
        with:
          package-name: '%s'
          package-type: 'container'
          min-versions-to-keep: 3
`, name, packageName)
			cleanupSteps += step
		}

		if cleanupSteps != "" {
			cleanupPath := filepath.Join(workflowsDir, "cleanup.yml")
			cleanupContent := fmt.Sprintf(cleanupTemplate, cleanupSteps)
			if err := os.WriteFile(cleanupPath, []byte(cleanupContent), 0644); err != nil {
				return fmt.Errorf("failed to write cleanup workflow: %v", err)
			}
		}
	}

	return nil
}
