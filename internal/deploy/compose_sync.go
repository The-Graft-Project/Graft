package deploy

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/git"
	"github.com/skssmd/graft/internal/ssh"
	"gopkg.in/yaml.v3"
)

// SyncComposeOnly uploads only the docker-compose.yml and restarts services
func SyncComposeOnly(client *ssh.Client, p *Project, heave bool, stdout, stderr io.Writer,doCompose bool , doEnv bool) error {
	if !doCompose && !doEnv {
		return fmt.Errorf("at least one of doCompose or doEnv must be true")
	}
	printstr:=""
	if doCompose{
		printstr+="compose"
	}
	if doEnv{
		if printstr!=""{
			printstr+=" and "
		}
		printstr+="env"
	}
	fmt.Fprintf(stdout, "📄 Syncing %s file only...\n", printstr)

	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)

	// Perform backup before sync if configured
	if err := PerformBackup(client, p, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Backup warning: %v\n", err)
	}
	
	// Ensure remote projects directory exists and is owned by the user
	// We do this once at the beginning to handle both compose and env sync cases
	// Use -R to ensure existing files (like docker-compose.yml) are also owned by the user
	if err := client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown -R $USER:$USER %s", remoteDir, remoteDir), stdout, stderr); err != nil {
		return fmt.Errorf("failed to prepare remote directory: %v", err)
	}

	if doCompose {
		// Find and parse the local graft-compose.yml file
		localFile := "graft-compose.yml"
		if _, err := os.Stat(localFile); err != nil {
			return fmt.Errorf("project file not found: %s", localFile)
		}

		// Parse compose file to get service configurations
		compose, err := ParseComposeFile(localFile)
		if err != nil {
			return fmt.Errorf("failed to parse compose file: %v", err)
		}

		// Load secrets
		secrets, _ := config.LoadSecrets()

		// Process environments and handle git-images mode transformation
		for sName := range compose.Services {
			sPtr := compose.Services[sName]
			ProcessServiceEnvironment(sName, &sPtr, secrets)
			
			// If in git-images mode and has build, replace with GHCR image
			mode := getGraftMode(sPtr.Labels)
			if mode == "git-images" && sPtr.Build != nil {
				remoteURL, err := git.GetRemoteURL(".", "origin")
				if err == nil {
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
					
					if ownerRepo != "" {
						sPtr.Image = fmt.Sprintf("ghcr.io/%s/%s:latest", strings.ToLower(ownerRepo), sName)
						sPtr.Build = nil // Remove build context
					}
				}
			}
			
			compose.Services[sName] = sPtr
		}

		// Generate the actual docker-compose.yml content
		updatedComposeData, err := yaml.Marshal(compose)
		if err != nil {
			return fmt.Errorf("failed to marshal updated compose file: %v", err)
		}

		// Save the actual docker-compose.yml locally
		if err := os.WriteFile("docker-compose.yml", updatedComposeData, 0644); err != nil {
			return fmt.Errorf("failed to save docker-compose.yml: %v", err)
		}

		// Ensure .gitignore is up to date
		EnsureGitignore(".")
	}
	
	// Upload env directory if it exists
	if doEnv {
		if _, err := os.Stat("env"); err == nil {
			fmt.Fprintf(stdout, "📤 Uploading environment files...\n")
			remoteEnvDir := path.Join(remoteDir, "env")
			// Create env dir with proper permissions
			if err := client.RunCommand(fmt.Sprintf("mkdir -p %s", remoteEnvDir), stdout, stderr); err != nil {
				return fmt.Errorf("failed to create remote env directory: %v", err)
			}
			
			files, _ := os.ReadDir("env")
			for _, f := range files {
				if !f.IsDir() {
					localEnvPath := filepath.Join("env", f.Name())
					remoteEnvPath := path.Join(remoteEnvDir, f.Name())
					if err := client.UploadFile(localEnvPath, remoteEnvPath); err != nil {
						return fmt.Errorf("failed to upload environment file %s: %v", f.Name(), err)
					}
				}
			}
		}
	}

	if doCompose {
		// Upload the generated docker-compose.yml
		remoteCompose := path.Join(remoteDir, "docker-compose.yml")
		fmt.Fprintf(stdout, "🔍 Verifying local %s exists...\n", "docker-compose.yml")
		if _, err := os.Stat("docker-compose.yml"); err != nil {
			return fmt.Errorf("local docker-compose.yml was not generated: %v", err)
		}

		fmt.Fprintf(stdout, "📤 Uploading generated docker-compose.yml to %s...\n", remoteCompose)
		if err := client.UploadFile("docker-compose.yml", remoteCompose); err != nil {
			return fmt.Errorf("failed to upload docker-compose.yml: %v", err)
		}
		
	}

	
		fmt.Fprintf(stdout, "✅ %s file uploaded!\n", printstr)
		
	

	if !heave {
		// Restart services without rebuilding
		fmt.Fprintln(stdout, "🔄 Restarting services...")
		if err := client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose up -d --remove-orphans", remoteDir), stdout, stderr); err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "✅ %s file synced!\n", printstr)

	return nil
}
