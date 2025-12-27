package git

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/skssmd/graft/internal/ssh"
)

// CheckGraftHookExists checks if graft-hook is already deployed on the server
func CheckGraftHookExists(client *ssh.Client) (bool, error) {
	// Check if /opt/graft/webhook/docker-compose.yml exists
	cmd := "test -f /opt/graft/webhook/docker-compose.yml && echo 'exists' || echo 'not found'"
	
	output := &strings.Builder{}
	err := client.RunCommand(cmd, output, os.Stderr)
	if err != nil {
		return false, fmt.Errorf("failed to check graft-hook: %v", err)
	}
	
	return strings.Contains(output.String(), "exists"), nil
}

// PromptGraftHookSetup prompts the user for graft-hook setup based on deployment mode
// Returns: (shouldDeploy bool, domain string, error)
func PromptGraftHookSetup(mode string) (bool, string, error) {
	reader := bufio.NewReader(os.Stdin)
	
	if mode == "git-manual" {
		// For git-manual, ask if they want graft-hook
		fmt.Println("\n🔗 Graft-Hook Setup (Optional)")
		fmt.Println("Do you want to install graft-hook for CI/CD automation?")
		fmt.Println("See documentation: https://github.com/skssmd/graft-hook")
		fmt.Print("\nInstall graft-hook? (y/n): ")
		
		response, _ := reader.ReadString('\n')
		response = strings.ToLower(strings.TrimSpace(response))
		
		if response != "y" && response != "yes" {
			return false, "", nil
		}
	}
	
	// Prompt for webhook domain
	fmt.Print("\nEnter webhook domain (e.g., graft-hook.example.com): ")
	domain, _ := reader.ReadString('\n')
	domain = strings.TrimSpace(domain)
	
	if domain == "" {
		return false, "", fmt.Errorf("webhook domain cannot be empty")
	}
	
	return true, domain, nil
}

// DeployGraftHook deploys the graft-hook service to the server
func DeployGraftHook(client *ssh.Client, domain string) error {
	fmt.Println("\n🚀 Deploying graft-hook webhook service...")
	
	// Create webhook directory
	cmd := "sudo mkdir -p /opt/graft/webhook && sudo chown $USER:$USER /opt/graft/webhook"
	if err := client.RunCommand(cmd, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to create webhook directory: %v", err)
	}
	
	// Generate docker-compose.yml content
	composeContent := generateGraftHookCompose(domain)
	
	// Write compose file to temp location
	tmpFile := "/tmp/graft-hook-compose.yml"
	writeCmd := fmt.Sprintf("cat > %s << 'EOF'\n%s\nEOF", tmpFile, composeContent)
	if err := client.RunCommand(writeCmd, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to write compose file: %v", err)
	}
	
	// Move to final location
	moveCmd := fmt.Sprintf("mv %s /opt/graft/webhook/docker-compose.yml", tmpFile)
	if err := client.RunCommand(moveCmd, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to move compose file: %v", err)
	}
	
	// Deploy graft-hook
	deployCmd := "cd /opt/graft/webhook && sudo docker compose up -d"
	if err := client.RunCommand(deployCmd, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to deploy graft-hook: %v", err)
	}
	
	fmt.Printf("\n✅ Graft-hook deployed at https://%s\n", domain)
	return nil
}

// generateGraftHookCompose generates the docker-compose.yml content for graft-hook
func generateGraftHookCompose(domain string) string {
	return fmt.Sprintf(`version: '3.8'

services:
  graft-hook:
    image: ghcr.io/skssmd/graft-hook:latest
    environment:
      - configpath=/opt/graft/config/projects.json
      - RUST_LOG=info
    
    labels:
      # Graft deployment mode
      - "graft.mode=serverbuild"
      
      # Enable Traefik for this container
      - "traefik.enable=true"

      - "traefik.http.routers.graft-hook.rule=Host(` + "`%s`" + `)"
      - "traefik.http.routers.graft-hook.priority=1"

      - "traefik.http.routers.graft-hook.service=graft-hook-service"
      - "traefik.http.services.graft-hook-service.loadbalancer.server.port=3000"
      - "traefik.http.routers.graft-hook.entrypoints=websecure"
      - "traefik.http.routers.graft-hook.tls.certresolver=letsencrypt"
    
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /opt/graft:/opt/graft/
      
    networks:
      - graft-public
    
    restart: always
    
networks:
  graft-public:
    external: true
`, domain)
}

// InitializeServerGit initializes Git on the server and sets the remote
func InitializeServerGit(client *ssh.Client, projectPath, remoteURL string) error {
	fmt.Println("\n🔧 Initializing Git on server...")

	// Check if Git is installed
	checkGitCmd := "git --version"
	if err := client.RunCommand(checkGitCmd, nil, nil); err != nil {
		fmt.Println("📦 Git not found on server. Detecting package manager...")
		
		// Try to detect package manager
		installCmd := ""
		pkgManagers := []struct {
			name string
			cmd  string
		}{
			{"apt-get", "sudo apt-get update && sudo apt-get install -y git"},
			{"dnf", "sudo dnf install -y git"},
			{"yum", "sudo yum install -y git"},
			{"apk", "sudo apk add git"},
			{"pacman", "sudo pacman -S --noconfirm git"},
			{"zypper", "sudo zypper install -y git"},
		}

		for _, pm := range pkgManagers {
			checkPM := fmt.Sprintf("command -v %s", pm.name)
			if err := client.RunCommand(checkPM, nil, nil); err == nil {
				installCmd = pm.cmd
				fmt.Printf("📂 Detected %s. Installing Git...\n", pm.name)
				break
			}
		}

		if installCmd == "" {
			return fmt.Errorf("failed to detect a supported package manager (apt, dnf, yum, apk, pacman, zypper). Please install Git manually on the server")
		}

		if err := client.RunCommand(installCmd, os.Stdout, os.Stderr); err != nil {
			return fmt.Errorf("failed to install Git on server: %v", err)
		}
		fmt.Println("✅ Git installed successfully")
	}

	// Ensure project directory exists
	mkdirCmd := fmt.Sprintf("sudo mkdir -p %s && sudo chown $USER:$USER %s", projectPath, projectPath)
	if err := client.RunCommand(mkdirCmd, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to create project directory: %v", err)
	}
	
	// Check if Git is already initialized
	checkCmd := fmt.Sprintf("cd %s && git rev-parse --git-dir 2>/dev/null && echo 'GIT_READY' || echo 'GIT_MISSING'", projectPath)
	output := &strings.Builder{}
	if err := client.RunCommand(checkCmd, output, os.Stderr); err != nil {
		return fmt.Errorf("failed to check Git status: %v", err)
	}
	
	if !strings.Contains(output.String(), "GIT_READY") {
		// Initialize Git
		initCmd := fmt.Sprintf("cd %s && git init", projectPath)
		if err := client.RunCommand(initCmd, os.Stdout, os.Stderr); err != nil {
			return fmt.Errorf("failed to initialize Git: %v", err)
		}
		fmt.Println("✅ Git initialized")
	}
	
	// Check if remote exists
	checkRemoteCmd := fmt.Sprintf("cd %s && git remote get-url origin 2>/dev/null || echo 'no remote'", projectPath)
	remoteOutput := &strings.Builder{}
	if err := client.RunCommand(checkRemoteCmd, remoteOutput, os.Stderr); err != nil {
		return fmt.Errorf("failed to check remote: %v", err)
	}
	
	if strings.Contains(remoteOutput.String(), "no remote") {
		// Add remote
		addRemoteCmd := fmt.Sprintf("cd %s && git remote add origin %s", projectPath, remoteURL)
		if err := client.RunCommand(addRemoteCmd, os.Stdout, os.Stderr); err != nil {
			return fmt.Errorf("failed to add remote: %v", err)
		}
		fmt.Println("✅ Git remote added")
	} else {
		// Update remote if different
		currentRemote := strings.TrimSpace(remoteOutput.String())
		if currentRemote != remoteURL {
			setRemoteCmd := fmt.Sprintf("cd %s && git remote set-url origin %s", projectPath, remoteURL)
			if err := client.RunCommand(setRemoteCmd, os.Stdout, os.Stderr); err != nil {
				return fmt.Errorf("failed to update remote: %v", err)
			}
			fmt.Println("✅ Git remote updated")
		} else {
			fmt.Println("✅ Git remote already configured")
		}
	}
	
	return nil
}
