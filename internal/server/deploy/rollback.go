package deploy

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/server/ssh"
	"gopkg.in/yaml.v3"
)

func PerformBackup(client *ssh.Client, p *Project, stdout, stderr io.Writer) error {
	if p.RollbackBackups <= 0 {
		return nil
	}

	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)

	// Check if project exists on remote
	if err := client.RunCommand(fmt.Sprintf("ls %s/docker-compose.yml", remoteDir), nil, nil); err != nil {
		return nil
	}

	// 1. Get timestamp
	out, err := client.GetCommandOutput("date +%Y%m%d%H%M%S")
	if err != nil {
		return fmt.Errorf("failed to get timestamp: %v", err)
	}
	timestamp := strings.TrimSpace(out)

	backupBase := fmt.Sprintf("/opt/graft/backup/%s", p.Name)
	backupDir := fmt.Sprintf("%s/%s", backupBase, timestamp)

	fmt.Fprintf(stdout, "\n📦 Creating rollback backup: %s\n", timestamp)

	// Create backup dirs
	client.RunCommand(fmt.Sprintf("sudo mkdir -p %s/compose %s/images", backupDir, backupDir), stdout, stderr)

	// Backup docker-compose.yml and env/
	client.RunCommand(fmt.Sprintf("sudo cp %s/docker-compose.yml %s/compose/ 2>/dev/null", remoteDir, backupDir), stdout, stderr)
	client.RunCommand(fmt.Sprintf("sudo cp -r %s/env %s/compose/ 2>/dev/null", remoteDir, backupDir), stdout, stderr)

	// Backup images using docker compose config to get exact tags
	fmt.Fprintf(stdout, "  🖼️  Saving service images (compressed)...\n")
	saveImagesCmd := fmt.Sprintf("cd %s && for tag in $(sudo docker compose config --images); do "+
		"img_id=$(sudo docker image inspect \"$tag\" --format '{{.Id}}' 2>/dev/null); "+
		"if [ -n \"$img_id\" ]; then "+
		"fname=$(echo \"$tag\" | tr ':/' '__'); "+
		"echo \"    📦 Saving $tag...\"; "+
		"sudo docker save \"$tag\" | gzip > %s/images/\"$fname\".tar.gz; "+
		"echo \"$tag\" | sudo tee %s/images/\"$fname\".tag > /dev/null; "+
		"fi; done", remoteDir, backupDir, backupDir)
	client.RunCommand(saveImagesCmd, stdout, stderr)

	// Clean up old backups
	cleanCmd := fmt.Sprintf("cd %s && ls -1dt * | tail -n +%d | xargs -I {} sudo rm -rf {}", backupBase, p.RollbackBackups+1)
	client.RunCommand(cleanCmd, stdout, stderr)

	return nil
}

func RestoreRollback(client *ssh.Client, p *Project, backupTimestamp string, stdout, stderr io.Writer) error {
	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)
	backupDir := fmt.Sprintf("/opt/graft/backup/%s/%s", p.Name, backupTimestamp)

	fmt.Fprintf(stdout, "⏪ Rolling back to version %s...\n", backupTimestamp)

	// 1. Restore files
	client.RunCommand(fmt.Sprintf("sudo cp %s/compose/docker-compose.yml %s/", backupDir, remoteDir), stdout, stderr)
	client.RunCommand(fmt.Sprintf("sudo cp -r %s/compose/env %s/ 2>/dev/null", backupDir, remoteDir), stdout, stderr)

	// 2. Identify images and stop services
	fmt.Fprintf(stdout, "🛑 Stopping current services and clearing images...\n")

	// Get tags from the RESTORED compose file
	outTags, _ := client.GetCommandOutput(fmt.Sprintf("cd %s && sudo docker compose config --images", remoteDir))
	imageTags := strings.Split(strings.TrimSpace(outTags), "\n")

	// Stop services
	client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose down", remoteDir), stdout, stderr)

	// Forcefully remove existing tags to make room for rollback images
	for _, tag := range imageTags {
		tag = strings.TrimSpace(tag)
		if tag != "" && tag != "<nil>" {
			fmt.Fprintf(stdout, "  🗑️  Removing existing image: %s\n", tag)
			client.RunCommand(fmt.Sprintf("sudo docker rmi -f %s 2>/dev/null", tag), nil, nil)
		}
	}

	// 3. Load images and fix tags
	fmt.Fprintf(stdout, "📥 Loading backed up images...\n")
	loadCmd := fmt.Sprintf("expected_images=$(cd %s && sudo docker compose config --images); "+
		"for img_tar in %s/images/*.tar*; do "+
		"[ -f \"$img_tar\" ] || continue; "+
		"tag_file=\"${img_tar%%.tar*}.tag\"; "+
		"fname=$(basename \"$img_tar\"); fname=\"${fname%%.tar*}\"; "+
		"if [ -f \"$tag_file\" ]; then "+
		"tag=$(cat \"$tag_file\" | tr -d '\\r\\n'); "+
		"else "+
		"tag=\"\"; "+
		"for expected in $expected_images; do "+
		"sanitized=$(echo \"$expected\" | tr ':/' '__'); "+
		"if [ \"$sanitized\" = \"$fname\" ]; then tag=\"$expected\"; break; fi; "+
		"done; "+
		"fi; "+
		"if [ -z \"$tag\" ]; then tag=\"$fname\"; fi; "+
		"echo \"  🏷️  Target tag: $tag\"; "+
		"sudo docker rmi -f \"$tag\" 2>/dev/null; "+
		"out=$(sudo docker load -i \"$img_tar\"); "+
		"echo \"  $out\"; "+
		"done", remoteDir, backupDir)
	client.RunCommand(loadCmd, stdout, stderr)

	// 4. Start services (using --pull never to ensure we use the restored images)
	fmt.Fprintf(stdout, "🚀 Starting services...\n")
	restartCmd := fmt.Sprintf("cd %s && sudo docker compose up -d --remove-orphans --pull never", remoteDir)
	return client.RunCommand(restartCmd, stdout, stderr)
}

func RestoreServiceRollback(client *ssh.Client, p *Project, backupTimestamp string, serviceName string, stdout, stderr io.Writer) error {
	remoteDir := fmt.Sprintf("/opt/graft/projects/%s", p.Name)
	backupDir := fmt.Sprintf("/opt/graft/backup/%s/%s", p.Name, backupTimestamp)

	fmt.Fprintf(stdout, "⏪ Rolling back service '%s' to version %s...\n", serviceName, backupTimestamp)

	// 1. Download backup's docker-compose.yml
	tmpBackupCompose := filepath.Join(os.TempDir(), "backup-compose-"+backupTimestamp+".yml")
	if err := client.DownloadFile(path.Join(backupDir, "compose", "docker-compose.yml"), tmpBackupCompose); err != nil {
		return fmt.Errorf("failed to download backup compose: %v", err)
	}
	defer os.Remove(tmpBackupCompose)

	backupCompose, err := ParseComposeFile(tmpBackupCompose, "")
	if err != nil {
		return fmt.Errorf("failed to parse backup compose: %v", err)
	}

	backupSvc, exists := backupCompose.Services[serviceName]
	if !exists {
		return fmt.Errorf("service '%s' not found in backup version %s", serviceName, backupTimestamp)
	}

	// 2. Download current docker-compose.yml from server
	tmpCurrentCompose := filepath.Join(os.TempDir(), "current-compose.yml")
	if err := client.DownloadFile(path.Join(remoteDir, "docker-compose.yml"), tmpCurrentCompose); err != nil {
		return fmt.Errorf("failed to download current remote compose: %v", err)
	}
	defer os.Remove(tmpCurrentCompose)

	currentCompose, err := ParseComposeFile(tmpCurrentCompose, "")
	if err != nil {
		return fmt.Errorf("failed to parse current remote compose: %v", err)
	}

	// 3. Update the service in the current compose
	if currentCompose.Services == nil {
		currentCompose.Services = make(map[string]ComposeService)
	}
	currentCompose.Services[serviceName] = backupSvc

	// 4. Save and Upload updated compose
	updatedData, err := yaml.Marshal(currentCompose)
	if err != nil {
		return fmt.Errorf("failed to marshal updated compose: %v", err)
	}
	if err := os.WriteFile(tmpCurrentCompose, updatedData, 0644); err != nil {
		return fmt.Errorf("failed to save updated compose locally: %v", err)
	}
	if err := client.UploadFile(tmpCurrentCompose, path.Join(remoteDir, "docker-compose.yml")); err != nil {
		return fmt.Errorf("failed to upload updated compose: %v", err)
	}

	// 5. Restore env files
	client.RunCommand(fmt.Sprintf("sudo cp -r %s/compose/env %s/ 2>/dev/null", backupDir, remoteDir), stdout, stderr)

	// 6. Identify images and stop specific service
	fmt.Fprintf(stdout, "🛑 Stopping service '%s' and clearing images...\n", serviceName)

	// Get tags for this specific service from restored compose
	// We need to be careful to only RMI the image this service uses
	_, _ = client.GetCommandOutput(fmt.Sprintf("cd %s && sudo docker compose config --images | grep -E '%s' || true", remoteDir, serviceName))
	// Note: grep -E 'serviceName' might match multiple if substrings match, but more accurate would be:
	// image=$(sudo docker compose config | yq '.services."%s".image')
	// Since we don't have yq, we'll try to find it via config config.

	client.RunCommand(fmt.Sprintf("cd %s && sudo docker compose stop %s && sudo docker compose rm -f %s", remoteDir, serviceName, serviceName), stdout, stderr)

	// 7. Load images from backup and fix tags
	fmt.Fprintf(stdout, "📥 Loading backed up images...\n")
	loadCmd := fmt.Sprintf("expected_images=$(cd %s && sudo docker compose config --images); "+
		"for img_tar in %s/images/*.tar*; do "+
		"[ -f \"$img_tar\" ] || continue; "+
		"tag_file=\"${img_tar%%.tar*}.tag\"; "+
		"fname=$(basename \"$img_tar\"); fname=\"${fname%%.tar*}\"; "+
		"if [ -f \"$tag_file\" ]; then "+
		"tag=$(cat \"$tag_file\" | tr -d '\\r\\n'); "+
		"else "+
		"tag=\"\"; "+
		"for expected in $expected_images; do "+
		"sanitized=$(echo \"$expected\" | tr ':/' '__'); "+
		"if [ \"$sanitized\" = \"$fname\" ]; then tag=\"$expected\"; break; fi; "+
		"done; "+
		"fi; "+
		"if [ -z \"$tag\" ]; then tag=\"$fname\"; fi; "+
		"echo \"  🏷️  Target tag: $tag\"; "+
		"sudo docker rmi -f \"$tag\" 2>/dev/null; "+
		"out=$(sudo docker load -i \"$img_tar\"); "+
		"echo \"  $out\"; "+
		"done", remoteDir, backupDir)
	client.RunCommand(loadCmd, stdout, stderr)

	// 8. Start ONLY the specific service
	fmt.Fprintf(stdout, "🚀 Starting service '%s'...\n", serviceName)
	restartCmd := fmt.Sprintf("cd %s && sudo docker compose up -d %s --pull never", remoteDir, serviceName)
	return client.RunCommand(restartCmd, stdout, stderr)
}
