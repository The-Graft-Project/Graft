package deploy

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/ssh"
	"gopkg.in/yaml.v3"
)

// SyncComposeOnly uploads only the docker-compose.yml and restarts services
func SyncComposeOnly(client *ssh.Client, p *Project, heave bool, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "📄 Syncing compose file only...\n")

	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)
	
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

	// Process environments for ALL services
	for sName := range compose.Services {
		sPtr := compose.Services[sName]
		ProcessServiceEnvironment(sName, &sPtr, secrets)
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

	// Ensure remote projects directory exists
	if err := client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown $USER:$USER %s", remoteDir, remoteDir), stdout, stderr); err != nil {
		return err
	}

	// Upload env directory if it exists
	if _, err := os.Stat("env"); err == nil {
		fmt.Fprintf(stdout, "📤 Uploading environment files...\n")
		remoteEnvDir := path.Join(remoteDir, "env")
		client.RunCommand(fmt.Sprintf("mkdir -p %s", remoteEnvDir), stdout, stderr)
		
		files, _ := os.ReadDir("env")
		for _, f := range files {
			if !f.IsDir() {
				localEnvPath := filepath.Join("env", f.Name())
				remoteEnvPath := path.Join(remoteEnvDir, f.Name())
				client.UploadFile(localEnvPath, remoteEnvPath)
			}
		}
	}

	// Upload the generated docker-compose.yml
	remoteCompose := path.Join(remoteDir, "docker-compose.yml")
	fmt.Fprintln(stdout, "📤 Uploading generated docker-compose.yml...")
	if err := client.UploadFile("docker-compose.yml", remoteCompose); err != nil {
		return err
	}

	if heave {
		fmt.Fprintln(stdout, "✅ Compose file uploaded!")
		return nil
	}

	// Restart services without rebuilding
	fmt.Fprintln(stdout, "🔄 Restarting services...")
	if err := client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose up -d --remove-orphans", remoteDir), stdout, stderr); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "✅ Compose file synced!")
	return nil
}
