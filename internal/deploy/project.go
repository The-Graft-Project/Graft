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
	Name     string             `yaml:"name"`
	Domain   string             `yaml:"domain"`
	Services map[string]Service `yaml:"services"`
}

func GenerateBoilerplate(name, domain string) *Project {
	p := &Project{
		Name:   name,
		Domain: domain,
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
	
	// Generate a valid docker-compose.yml file that can be used directly
	template := `# Docker Compose Configuration for: %s
# Domain: %s
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
      # Graft deployment mode: localbuild | serverbuild
      # - localbuild: Build image locally, push to server, run
      # - serverbuild: Copy source to server, build & run there
      - "graft.mode=serverbuild"
      
      # Traefik routing - serves all requests to %s/
      - "traefik.enable=true"
      - "traefik.http.routers.%s-frontend.rule=Host(` + "`%s`" + `)"
      - "traefik.http.routers.%s-frontend.priority=1"
      - "traefik.http.services.%s-frontend.loadbalancer.server.port=3000" #this is the internal container port that the frontend service is running on
      
      # HTTPS with Let's Encrypt (uncomment to enable)
      # - "traefik.http.routers.%s-frontend.entrypoints=websecure"
      # - "traefik.http.routers.%s-frontend.tls.certresolver=letsencrypt"
    
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
      - "graft.mode=serverbuild"
      
      # Traefik routing - serves %s/api/* and strips /api prefix
      - "traefik.enable=true"
      - "traefik.http.routers.%s-backend.rule=Host(` + "`%s`" + `) && PathPrefix(` + "`/api`" + `)"
      - "traefik.http.middlewares.%s-backend-strip.stripprefix.prefixes=/api" 
      - "traefik.http.routers.%s-backend.middlewares=%s-backend-strip" 
      - "traefik.http.services.%s-backend.loadbalancer.server.port=5000" #this is the internal container port that the backend service is running on
      
      
      # HTTPS with Let's Encrypt (uncomment to enable)
      # - "traefik.http.routers.%s-backend.entrypoints=websecure"
      # - "traefik.http.routers.%s-backend.tls.certresolver=letsencrypt"
    
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
#   - static: Upload built files directly (npm run build output)
#   - localbuild: Build Docker image locally, upload to server
#   - serverbuild: Upload source, build Docker image on server
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
		p.Name, p.Domain, // New name and domain fields
		p.Name, p.Domain, p.Name, // Header
		p.Name, p.Domain, p.Name, p.Name, p.Name, p.Name, p.Name, // Frontend
		p.Name, p.Domain, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, p.Name, // Backend
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
	return nil
}
