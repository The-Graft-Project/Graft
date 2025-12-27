package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/deploy"
	"github.com/skssmd/graft/internal/git"
	"github.com/skssmd/graft/internal/ssh"
)

// handleGitMode handles Git-based deployment modes setup
// Returns true if Git mode setup was completed and sync should return early
func handleGitMode(client *ssh.Client, meta *config.ProjectMetadata, localFile string) (bool, error) {
	if meta == nil {
		return false, nil
	}

	// Check if this is a Git-based deployment mode
	isGitMode := meta.DeploymentMode == "git-images" || 
		meta.DeploymentMode == "git-repo-serverbuild" || 
		meta.DeploymentMode == "git-manual"

	if !isGitMode {
		return false, nil
	}

	// If project is already initialized, skip setup
	if meta.Initialized {
		fmt.Printf("\n✅ Project already initialized with %s mode\n", meta.DeploymentMode)

		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Do you want to sync environment variables and regenerate docker-compose.yml? (y/n): ")
		input, _ := reader.ReadString('\n')
		input = strings.ToLower(strings.TrimSpace(input))

		if input == "y" || input == "yes" {
			// 1. For git-images mode, ensure build blocks are converted
			if meta.DeploymentMode == "git-images" {
				remoteURL, err := git.GetRemoteURL(".")
				if err == nil {
					if owner, repo, err := git.ParseGitHubRepo(remoteURL); err == nil {
						git.ConvertBuildToImage(localFile, owner, repo)
					}
				}
			}

			// 2. Generate docker-compose.yml locally
			fmt.Println("📝 Generating docker-compose.yml locally...")
			if err := deploy.GenerateLocalCompose(localFile); err != nil {
				return true, fmt.Errorf("failed to generate compose file: %v", err)
			}
			fmt.Println("✅ Generated docker-compose.yml")

			// 3. Transfer to server (heave sync)
			fmt.Println("📤 Transferring environment and compose to server...")

			// Load project
			p, err := deploy.LoadProject(localFile)
			if err != nil {
				return true, fmt.Errorf("failed to load project: %v", err)
			}

			// Run sync in heave mode (upload only)
			err = deploy.Sync(client, p, false, true, false, "", "", os.Stdout, os.Stderr)
			if err != nil {
				return true, fmt.Errorf("failed to transfer environment: %v", err)
			}
			fmt.Println("✅ Environment and compose synced to server!")
		}

		fmt.Println("\n📝 To deploy updates:")
		fmt.Println("   1. Commit your changes: git add . && git commit -m 'Your message'")
		fmt.Println("   2. Push to GitHub: git push")
		fmt.Println("   3. GitHub Actions will automatically build and deploy")
		return true, nil
	}

	fmt.Printf("\n📦 Deployment Mode: %s (First-time setup)\n", meta.DeploymentMode)

	// Check if Git repository exists
	if !git.HasGitRepo(".") {
		return true, fmt.Errorf("no Git repository found. Git mode requires a Git repository.\n   Initialize Git with: git init && git remote add origin <url>")
	}

	// Get Git remote URL
	remoteURL, err := git.GetRemoteURL(".")
	if err != nil {
		return true, fmt.Errorf("could not get Git remote URL: %v\n   Make sure you have a remote configured: git remote add origin <url>", err)
	}

	// Parse GitHub repository info
	owner, repo, err := git.ParseGitHubRepo(remoteURL)
	if err != nil {
		return true, fmt.Errorf("%v\n   This feature currently supports GitHub repositories only", err)
	}

	fmt.Printf("📍 Repository: %s/%s\n", owner, repo)

	// Initialize Git on server
	if err := git.InitializeServerGit(client, meta.RemotePath, remoteURL); err != nil {
		return true, fmt.Errorf("failed to initialize Git on server: %v", err)
	}

	// Handle graft-hook deployment
	webhookDomain := meta.WebhookDomain
	if meta.DeploymentMode == "git-images" || meta.DeploymentMode == "git-repo-serverbuild" {
		// Check if graft-hook exists
		hookExists, err := git.CheckGraftHookExists(client)
		if err != nil {
			fmt.Printf("⚠️  Warning: Could not check graft-hook status: %v\n", err)
		}

		if !hookExists || webhookDomain == "" {
			if !hookExists {
				fmt.Println("\n🔗 Graft-hook webhook service not found")
			} else {
				fmt.Println("\n🔗 Webhook domain not configured for this project")
			}

			shouldDeploy, domain, err := git.PromptGraftHookSetup(meta.DeploymentMode)
			if err != nil {
				return true, err
			}

			if shouldDeploy {
				if err := git.DeployGraftHook(client, domain); err != nil {
					return true, fmt.Errorf("failed to deploy graft-hook: %v", err)
				}
				webhookDomain = domain
				meta.WebhookDomain = domain
				config.SaveProjectMetadata(meta)
			} else {
				fmt.Println("⚠️  Skipping graft-hook deployment. Automated deployments will not work.")
				return true, nil
			}
		}
	} else if meta.DeploymentMode == "git-manual" {
		// For git-manual, optionally deploy graft-hook
		hookExists, err := git.CheckGraftHookExists(client)
		if err != nil {
			fmt.Printf("⚠️  Warning: Could not check graft-hook status: %v\n", err)
		}

		if !hookExists && webhookDomain == "" {
			shouldDeploy, domain, err := git.PromptGraftHookSetup(meta.DeploymentMode)
			if err != nil {
				return true, err
			}

			if shouldDeploy {
				if err := git.DeployGraftHook(client, domain); err != nil {
					return true, fmt.Errorf("failed to deploy graft-hook: %v", err)
				}
				webhookDomain = domain
				meta.WebhookDomain = domain
				config.SaveProjectMetadata(meta)
			}
		}
	}

	// Generate workflows if not already present
	if meta.DeploymentMode == "git-images" || meta.DeploymentMode == "git-repo-serverbuild" {
		workflowPath := filepath.Join(".github", "workflows", "ci-cd.yml")
		deployWorkflowPath := filepath.Join(".github", "workflows", "deploy.yml")

		needsWorkflows := false
		if meta.DeploymentMode == "git-images" {
			if _, err := os.Stat(workflowPath); os.IsNotExist(err) {
				needsWorkflows = true
			}
		}
		if _, err := os.Stat(deployWorkflowPath); os.IsNotExist(err) {
			needsWorkflows = true
		}

		if needsWorkflows && webhookDomain != "" {
			fmt.Println("\n📝 Generating GitHub Actions workflows...")

			// Parse services from compose file
			services, err := git.ParseServices(localFile)
			if err != nil {
				return true, fmt.Errorf("failed to parse services: %v", err)
			}

			if err := git.GenerateWorkflows(meta.DeploymentMode, owner, repo, meta.Name, webhookDomain, services); err != nil {
				return true, fmt.Errorf("failed to generate workflows: %v", err)
			}

			// For git-images mode, convert build to image in compose file
			if meta.DeploymentMode == "git-images" {
				if err := git.ConvertBuildToImage(localFile, owner, repo); err != nil {
					return true, fmt.Errorf("failed to convert compose file: %v", err)
				}
			}

			// Generate docker-compose.yml locally
			fmt.Println("📝 Generating docker-compose.yml locally...")
			if err := deploy.GenerateLocalCompose(localFile); err != nil {
				fmt.Printf("⚠️  Warning: Failed to generate compose file: %v\n", err)
			} else {
				fmt.Println("✅ Generated docker-compose.yml")
			}

			// Elective sync to server
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Do you want to sync environment variables and docker-compose.yml to the server now? (y/n): ")
			input, _ := reader.ReadString('\n')
			input = strings.ToLower(strings.TrimSpace(input))

			if input == "y" || input == "yes" {
				fmt.Println("📤 Syncing to server...")
				p, err := deploy.LoadProject(localFile)
				if err == nil {
					err = deploy.Sync(client, p, false, true, false, "", "", os.Stdout, os.Stderr)
					if err != nil {
						fmt.Printf("⚠️  Warning: Failed to sync to server: %v\n", err)
					} else {
						fmt.Println("✅ Environment and compose synced to server!")
					}
				}
			}

			// Mark project as initialized
			meta.Initialized = true
			if err := config.SaveProjectMetadata(meta); err != nil {
				fmt.Printf("⚠️  Warning: Could not update project metadata: %v\n", err)
			}

			fmt.Println("\n✅ Your project is ready for Git-based deployment!")
			fmt.Println("📝 Next steps:")
			fmt.Println("   1. Review generated workflows in .github/workflows/")
			fmt.Println("   2. Commit and push to your repository:")
			fmt.Println("      git add . && git commit -m 'Add Graft workflows' && git push")
			fmt.Println("   3. GitHub Actions will automatically build and deploy your project")
			return true, nil
		}
	}

	// For git-manual mode, mark as initialized and show completion message
	if meta.DeploymentMode == "git-manual" {
		// Generate docker-compose.yml locally
		fmt.Println("📝 Generating docker-compose.yml locally...")
		if err := deploy.GenerateLocalCompose(localFile); err != nil {
			fmt.Printf("⚠️  Warning: Failed to generate compose file: %v\n", err)
		} else {
			fmt.Println("✅ Generated docker-compose.yml")
		}

		// Elective sync to server
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Do you want to sync environment variables and docker-compose.yml to the server now? (y/n): ")
		input, _ := reader.ReadString('\n')
		input = strings.ToLower(strings.TrimSpace(input))

		if input == "y" || input == "yes" {
			fmt.Println("📤 Syncing to server...")
			p, err := deploy.LoadProject(localFile)
			if err == nil {
				err = deploy.Sync(client, p, false, true, false, "", "", os.Stdout, os.Stderr)
				if err != nil {
					fmt.Printf("⚠️  Warning: Failed to sync to server: %v\n", err)
				} else {
					fmt.Println("✅ Environment and compose synced to server!")
				}
			}
		}

		meta.Initialized = true
		if err := config.SaveProjectMetadata(meta); err != nil {
			fmt.Printf("⚠️  Warning: Could not update project metadata: %v\n", err)
		}

		fmt.Println("\n✅ Git initialized on server")
		fmt.Println("📝 Manual deployment: Continue with normal sync process")
		return false, nil // Continue with normal sync
	}

	return false, nil
}
