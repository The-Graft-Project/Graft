package deploy

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/ssh"
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

	// Inject secrets into the compose file
	content, err := os.ReadFile(localFile)
	if err != nil {
		return err
	}

	secrets, _ := config.LoadSecrets()
	contentStr := string(content)
	for key, value := range secrets {
		contentStr = strings.ReplaceAll(contentStr, fmt.Sprintf("${%s}", key), value)
	}

	// Write modified compose file
	tmpFile := path.Join(os.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(tmpFile, []byte(contentStr), 0644); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	// Upload docker-compose.yml
	remoteCompose := path.Join(remoteDir, "docker-compose.yml")
	fmt.Fprintln(stdout, "📤 Uploading docker-compose.yml...")
	if err := client.UploadFile(tmpFile, remoteCompose); err != nil {
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
