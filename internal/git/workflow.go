package git

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Service represents a Docker Compose service
type Service struct {
	Name    string
	Build   *BuildConfig
	Image   string
	Context string // Build context path
}

// BuildConfig represents the build configuration
type BuildConfig struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

// ComposeFile represents a simplified docker-compose.yml structure
type ComposeFile struct {
	Services map[string]struct {
		Build interface{} `yaml:"build"`
		Image string      `yaml:"image"`
	} `yaml:"services"`
}

// ParseServices extracts services from a docker-compose file
func ParseServices(composePath string) ([]Service, error) {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read compose file: %v", err)
	}

	var compose ComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, fmt.Errorf("failed to parse compose file: %v", err)
	}

	var services []Service
	for name, svc := range compose.Services {
		service := Service{Name: name}

		// Check if service has a build configuration
		if svc.Build != nil {
			switch v := svc.Build.(type) {
			case string:
				// Simple build: context path only
				service.Build = &BuildConfig{Context: v}
				service.Context = v
			case map[string]interface{}:
				// Complex build with context and dockerfile
				if ctx, ok := v["context"].(string); ok {
					service.Context = ctx
					service.Build = &BuildConfig{Context: ctx}
					if df, ok := v["dockerfile"].(string); ok {
						service.Build.Dockerfile = df
					}
				}
			}
		}

		// Check if service has an image
		if svc.Image != "" {
			service.Image = svc.Image
		}

		services = append(services, service)
	}

	return services, nil
}

// GenerateWorkflows generates GitHub Actions workflows based on deployment mode
func GenerateWorkflows(mode, owner, repo, projectName, webhookDomain string, services []Service) error {
	// Create .github/workflows directory
	workflowDir := filepath.Join(".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0755); err != nil {
		return fmt.Errorf("failed to create workflows directory: %v", err)
	}

	// Generate CI/CD workflow for git-images mode
	if mode == "git-images" {
		cicdContent, err := GenerateCICDWorkflow(owner, repo, services)
		if err != nil {
			return err
		}

		cicdPath := filepath.Join(workflowDir, "ci-cd.yml")
		if err := os.WriteFile(cicdPath, []byte(cicdContent), 0644); err != nil {
			return fmt.Errorf("failed to write CI/CD workflow: %v", err)
		}
		fmt.Printf("✅ Generated .github/workflows/ci-cd.yml\n")
	}

	// Generate deploy workflow for git-images and git-repo-serverbuild
	if mode == "git-images" || mode == "git-repo-serverbuild" {
		deployContent, err := GenerateDeployWorkflow(projectName, mode, webhookDomain)
		if err != nil {
			return err
		}

		deployPath := filepath.Join(workflowDir, "deploy.yml")
		if err := os.WriteFile(deployPath, []byte(deployContent), 0644); err != nil {
			return fmt.Errorf("failed to write deploy workflow: %v", err)
		}
		fmt.Printf("✅ Generated .github/workflows/deploy.yml\n")
	}

	return nil
}

// GenerateCICDWorkflow generates the CI/CD workflow for building and pushing images
func GenerateCICDWorkflow(owner, repo string, services []Service) (string, error) {
	// Filter services that have build configurations
	buildServices := []Service{}
	for _, svc := range services {
		if svc.Build != nil {
			buildServices = append(buildServices, svc)
		}
	}

	if len(buildServices) == 0 {
		return "", fmt.Errorf("no services with build configuration found")
	}

	// Generate workflow content
	workflow := fmt.Sprintf(`name: CI/CD Pipeline

on:
  push:
    branches: [ main, develop ]
  pull_request:
    branches: [ main, develop ]
  workflow_dispatch:

env:
  REGISTRY: ghcr.io
  IMAGE_PREFIX: ghcr.io/%s/%s

jobs:
`, owner, repo)

	// Generate a job for each service
	for _, svc := range buildServices {
		dockerfilePath := "Dockerfile"
		if svc.Build.Dockerfile != "" {
			dockerfilePath = svc.Build.Dockerfile
		}

		workflow += fmt.Sprintf(`  build-%s:
    name: Build %s
    runs-on: ubuntu-latest
    if: github.event_name == 'push'
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
          images: ${{ env.IMAGE_PREFIX }}/%s
          tags: |
            type=ref,event=branch
            type=ref,event=pr
            type=sha,prefix={{branch}}-
            type=raw,value=latest,enable={{is_default_branch}}

      - name: Build and push Docker image
        uses: docker/build-push-action@v5
        with:
          context: ./%s
          file: ./%s/%s
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max

`, svc.Name, svc.Name, svc.Name, svc.Context, svc.Context, dockerfilePath)
	}

	return workflow, nil
}

// GenerateDeployWorkflow generates the deployment webhook workflow
func GenerateDeployWorkflow(projectName, mode, webhookDomain string) (string, error) {
	deployType := "image"
	if mode == "git-repo-serverbuild" {
		deployType = "repo"
	}

	workflow := fmt.Sprintf(`name: Deploy

on:
  workflow_run:
    workflows: ["CI/CD Pipeline"]
    types:
      - completed
  release:
    types: [published]
  workflow_dispatch:

jobs:
  deploy:
    name: Deploy via Webhook
    runs-on: ubuntu-latest
    if: ${{ github.event_name != 'workflow_run' || github.event.workflow_run.conclusion == 'success' }}
    
    steps:
      - name: Send Webhook Request
        run: |
          curl -X POST https://%s/webhook \
            -H "Content-Type: application/json" \
            -d '{
              "project": "%s",
              "repository": "${{ github.event.repository.name }}",
              "token": "${{ secrets.GITHUB_TOKEN }}",
              "user": "${{ github.actor }}",
              "type": "%s",
              "registry": "ghcr.io"
            }'
`, webhookDomain, projectName, deployType)

	return workflow, nil
}

// ConvertBuildToImage modifies the compose file to use GHCR images instead of build contexts
func ConvertBuildToImage(composePath, owner, repo string) error {
	data, err := os.ReadFile(composePath)
	if err != nil {
		return fmt.Errorf("failed to read compose file: %v", err)
	}

	var compose map[string]interface{}
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return fmt.Errorf("failed to parse compose file: %v", err)
	}

	services, ok := compose["services"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("no services found in compose file")
	}

	// Convert each service with build to use image
	for name, svcData := range services {
		svc, ok := svcData.(map[string]interface{})
		if !ok {
			continue
		}

		if _, hasBuild := svc["build"]; hasBuild {
			// Replace build with image
			delete(svc, "build")
			svc["image"] = fmt.Sprintf("ghcr.io/%s/%s/%s:latest", owner, repo, name)
		}
	}

	// Write modified compose file
	output, err := yaml.Marshal(compose)
	if err != nil {
		return fmt.Errorf("failed to marshal compose file: %v", err)
	}

	if err := os.WriteFile(composePath, output, 0644); err != nil {
		return fmt.Errorf("failed to write compose file: %v", err)
	}

	fmt.Println("✅ Converted build contexts to GHCR images in graft-compose.yml")
	return nil
}
