package hostinit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/ssh"
)

func InitHost(client *ssh.Client, setupPostgres, setupRedis, exposePostgres, exposeRedis bool, pgUser, pgPass, pgDB string, stdout, stderr io.Writer) error {
	// Detect OS and set appropriate package manager commands
	var dockerInstallCmd, composeInstallCmd string

	// Use official Docker installation script for all Linux distributions to ensure latest stable version
	// This script installs docker-ce, docker-ce-cli, containerd.io, docker-buildx-plugin, and docker-compose-plugin
	installCmd := "curl -fsSL https://get.docker.com | sudo sh && sudo systemctl start docker && sudo systemctl enable docker && sudo usermod -aG docker $USER"

	if err := client.RunCommand("cat /etc/os-release | grep -i 'amazon linux'", nil, nil); err == nil {
		fmt.Fprintln(stdout, "🔍 Detected: Amazon Linux")
		dockerInstallCmd = installCmd
		composeInstallCmd = installCmd
	} else if err := client.RunCommand("cat /etc/os-release | grep -i 'ubuntu\\|debian'", nil, nil); err == nil {
		fmt.Fprintln(stdout, "🔍 Detected: Ubuntu/Debian")
		// Wait for any existing apt processes to finish
		waitCmd := "while sudo fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1 || sudo fuser /var/lib/dpkg/lock >/dev/null 2>&1; do echo 'Waiting for apt lock...'; sleep 3; done"
		
		ubuntuInstallCmd := `
# Uninstall conflicting packages
for pkg in docker.io docker-doc docker-compose docker-compose-v2 podman-docker containerd runc; do sudo apt-get remove -y $pkg || true; done

# Add Docker's official GPG key
sudo apt-get update
sudo apt-get install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc

# Add the repository to Apt sources
sudo tee /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Signed-By: /etc/apt/keyrings/docker.asc
EOF

sudo apt-get update
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo systemctl start docker
sudo systemctl enable docker
sudo usermod -aG docker $USER`

		dockerInstallCmd = waitCmd + " && " + ubuntuInstallCmd
		composeInstallCmd = dockerInstallCmd
	} else {
		fmt.Fprintln(stdout, "🔍 Detected: Generic Linux")
		dockerInstallCmd = installCmd
		composeInstallCmd = installCmd
	}

	steps := []struct {
		name    string
		check   string
		cmd     string
		skipMsg string
	}{
		{
			name:    "Check Docker",
			check:   "docker --version",
			cmd:     dockerInstallCmd,
			skipMsg: "Docker is already installed.",
		},
		{
			name:    "Check Docker Compose",
			check:   "docker compose version",
			cmd:     composeInstallCmd,
			skipMsg: "Docker Compose is already installed.",
		},
		{
			name:    "Create Network",
			check:   "sudo docker network inspect graft-public",
			cmd:     "sudo docker network create graft-public",
			skipMsg: "Docker network 'graft-public' already exists.",
		},
		{
			name:    "Create Base Dirs",
			check:   "ls -d /opt/graft/gateway /opt/graft/infra",
			cmd:     "sudo mkdir -p /opt/graft/gateway /opt/graft/infra && sudo chown $USER:$USER /opt/graft/gateway /opt/graft/infra",
			skipMsg: "Base directories already exist.",
		},
		{
			name:  "Setup Traefik",
			check: "sudo docker ps | grep graft-traefik",
			cmd: `sudo tee /opt/graft/gateway/docker-compose.yml <<EOF
version: '3.8'
services:
  traefik:
    container_name: graft-traefik
    image: traefik:v3
    command:
      # API and Dashboard
      - "--api.insecure=true"
      
      # Docker provider
      - "--providers.docker=true"
      - "--providers.docker.exposedbydefault=false"
      
      # Entrypoints
      - "--entrypoints.web.address=:80"
      - "--entrypoints.websecure.address=:443"
      
      # HTTP to HTTPS redirect
      - "--entrypoints.web.http.redirections.entrypoint.to=websecure"
      - "--entrypoints.web.http.redirections.entrypoint.scheme=https"
      
      # Let's Encrypt
      - "--certificatesresolvers.letsencrypt.acme.email=admin@yourdomain.com"
      - "--certificatesresolvers.letsencrypt.acme.storage=/letsencrypt/acme.json"
      - "--certificatesresolvers.letsencrypt.acme.httpchallenge=true"
      - "--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web"
    ports:
      - "80:80"
      - "443:443"
      # - "8080:8080"  # Traefik dashboard
    volumes:
      - "/var/run/docker.sock:/var/run/docker.sock:ro"
      - "/opt/graft/gateway/letsencrypt:/letsencrypt"
    networks:
      - graft-public
    restart: unless-stopped

networks:
  graft-public:
    external: true
EOF
sudo mkdir -p /opt/graft/gateway/letsencrypt
sudo chmod 600 /opt/graft/gateway/letsencrypt
sudo docker compose -f /opt/graft/gateway/docker-compose.yml up -d`,
			skipMsg: "Traefik gateway is already running.",
		},
	}

	for _, step := range steps {
		// Check if step is already completed
		if step.check != "" {
			err := client.RunCommand(step.check, nil, nil)
			if err == nil {
				if step.skipMsg != "" {
					fmt.Fprintf(stdout, "✅ %s\n", step.skipMsg)
				}
				continue
			}
		}

		fmt.Fprintf(stdout, "⏩ Running: %s...\n", step.name)
		if err := client.RunCommand(step.cmd, stdout, stderr); err != nil {
			return fmt.Errorf("step %s failed: %v", step.name, err)
		}
	}

	// Conditionally setup shared infrastructure
	if setupPostgres || setupRedis {
		fmt.Fprintf(stdout, "\n🔧 Setup Shared Infra\n")

		pgPort := ""
		if exposePostgres {
			pgPort = "5432"
		}
		redisPort := ""
		if exposeRedis {
			redisPort = "6379"
		}

		infraCfg := config.InfraConfig{
			PostgresUser:     pgUser,
			PostgresPassword: pgPass,
			PostgresDB:       pgDB,
			PostgresPort:     pgPort,
			RedisPort:        redisPort,
		}

		if err := SetupInfra(client, setupPostgres, setupRedis, infraCfg, stdout, stderr); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "\n⏭️  Skipping shared infrastructure setup\n")
	}

	return nil
}

func SetupInfra(client *ssh.Client, setupPostgres, setupRedis bool, cfg config.InfraConfig, stdout, stderr io.Writer) error {
	var services string
	if setupPostgres {
		ports := ""
		if cfg.PostgresPort != "" && cfg.PostgresPort != "null" {
			ports = fmt.Sprintf(`    ports:
      - "%s:5432"
`, cfg.PostgresPort)
		}
		services += fmt.Sprintf(`  postgres:
    container_name: graft-postgres
    image: postgres:18.1-alpine
%s    environment:
      POSTGRES_USER: %s
      POSTGRES_PASSWORD: %s
      POSTGRES_DB: %s
    networks:
      - graft-public
`, ports, cfg.PostgresUser, cfg.PostgresPassword, cfg.PostgresDB)
	}
	if setupRedis {
		ports := ""
		if cfg.RedisPort != "" && cfg.RedisPort != "null" {
			ports = fmt.Sprintf(`    ports:
      - "%s:6379"
`, cfg.RedisPort)
		}
		services += fmt.Sprintf(`  redis:
    container_name: graft-redis
    image: redis:alpine
%s    networks:
      - graft-public
`, ports)
	}

	infraCmd := fmt.Sprintf(`sudo tee /opt/graft/infra/docker-compose.yml <<EOF
version: '3.8'
services:
%s
networks:
  graft-public:
    external: true
EOF
sudo docker compose -f /opt/graft/infra/docker-compose.yml up -d`, services)

	if err := client.RunCommand(infraCmd, stdout, stderr); err != nil {
		return fmt.Errorf("shared infrastructure setup failed: %v", err)
	}

	// Save credentials to remote config file
	data, _ := json.MarshalIndent(cfg, "", "  ")

	tmpFile := filepath.Join(os.TempDir(), "infra.config")
	os.WriteFile(tmpFile, data, 0644)
	defer os.Remove(tmpFile)

	if err := client.UploadFile(tmpFile, config.RemoteInfraPath); err != nil {
		fmt.Fprintf(stdout, "⚠️  Warning: Could not save infra credentials to remote server: %v\n", err)
	} else {
		fmt.Fprintln(stdout, "✅ Infra credentials saved to remote server")
	}

	return nil
}
