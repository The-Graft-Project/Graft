package webhook

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/ssh"
)

// InstallHook installs or restarts the graft-hook webhook service
func InstallHook(client *ssh.Client, gCfg *config.GlobalConfig, deploymentMode string, reader *bufio.Reader, currentHookURL string, srv *config.ServerConfig) error {
	if client == nil {
		return errors.New("client is nil")
	}
	
	installHook := false
	if deploymentMode == "git-manual" {
		fmt.Print("\n❓ Do you want to install graft-hook for CI/CD automation? (y/n): ")
		input, _ := reader.ReadString('\n')
		installHook = strings.ToLower(strings.TrimSpace(input)) == "y"
	} else {
		// Check if already installed and restart if it exists
		if err := client.RunCommand("cd /opt/graft/webhook && sudo docker compose down", nil, nil); err != nil {
			fmt.Println("\n🔍 graft-hook is not installed on the server.")
			installHook = true
		} else {
			// Successfully stopped, now restart it
			if err := client.RunCommand("cd /opt/graft/webhook && sudo docker compose up -d", os.Stdout, os.Stderr); err != nil {
				fmt.Printf("\n⚠️  Warning: Failed to restart graft-hook: %v\n", err)
			} else {
				fmt.Println("\n✅ graft-hook restarted successfully.")
				// Fetch existing hook URL from global registry if available
				if srv, exists := gCfg.Servers[srv.RegistryName]; exists {
					if srv.GraftHookURL != "" {
						currentHookURL = srv.GraftHookURL
					}
					if currentHookURL == "" {
						fmt.Println("⚠️  Warning: graft-hook URL not found in registry.")
						fmt.Print("Enter the graft-hook domain (e.g. graft-hook.example.com): ")
						hookDomain, _ := reader.ReadString('\n')
						hookDomain = strings.TrimSpace(hookDomain)
						if hookDomain != "" {
							currentHookURL = fmt.Sprintf("https://%s", hookDomain)
							// Save to global registry
							srv.GraftHookURL = currentHookURL
							gCfg.Servers[srv.RegistryName] = srv
							if err := config.SaveGlobalConfig(gCfg); err != nil {
								fmt.Printf("⚠️  Warning: Could not save hook URL to global registry: %v\n", err)
							} else {
								fmt.Println("✅ Hook URL saved to global registry")
							}
						}
					} else {
						fmt.Printf("📍 Using hook URL from registry: %s\n", currentHookURL)
					}
				} else {
					fmt.Println("⚠️  Warning: Server not found in registry, cannot retrieve hook URL.")
				}
			}
		}
	}

	if installHook {
		fmt.Print("Enter domain for graft-hook (e.g. graft-hook.example.com): ")
		hookDomain, _ := reader.ReadString('\n')
		hookDomain = strings.TrimSpace(hookDomain)

		fmt.Println("🚀 Deploying graft-hook...")
		hookCompose := fmt.Sprintf(`version: '3.8'
services:
  graft-hook:
    image: ghcr.io/the-graft-project/graft-hook:latest
    environment:
      - configpath=/opt/graft/config/projects.json
    labels:
      - "graft.mode=serverbuild"
      - "traefik.enable=true"
      - "traefik.http.routers.graft-hook.rule=Host(`+"`"+`%s`+"`"+`)"
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
    external: true`, hookDomain)

		client.RunCommand("sudo mkdir -p /opt/graft/webhook && sudo chown $USER:$USER /opt/graft/webhook", nil, nil)
		tmpFile := filepath.Join(os.TempDir(), "hook-compose.yml")
		os.WriteFile(tmpFile, []byte(hookCompose), 0644)
		client.UploadFile(tmpFile, "/opt/graft/webhook/docker-compose.yml")
		os.Remove(tmpFile)
		client.RunCommand("sudo docker compose -f /opt/graft/webhook/docker-compose.yml up -d", os.Stdout, os.Stderr)
		fmt.Println("✅ graft-hook deployed.")
		currentHookURL = fmt.Sprintf("https://%s", hookDomain)

		// Save to global registry
		if srv, exists := gCfg.Servers[srv.RegistryName]; exists {
			srv.GraftHookURL = currentHookURL
			gCfg.Servers[srv.RegistryName] = srv
			if err := config.SaveGlobalConfig(gCfg); err != nil {
				fmt.Printf("⚠️  Warning: Could not save hook URL to global registry: %v\n", err)
			} else {
				fmt.Println("✅ Hook URL saved to global registry")
			}
		} else {
			fmt.Printf("⚠️  Warning: Server '%s' not found in global registry, cannot save hook URL\n", srv.RegistryName)
		}
	}

	return nil
}
