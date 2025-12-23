package hostinit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/ssh"
)

func InitHost(client *ssh.Client, setupPostgres, setupRedis, exposePostgres, exposeRedis bool, pgUser, pgPass, pgDB string, stdout, stderr io.Writer) error {
	// Detect OS and set appropriate package manager commands
	var dockerInstallCmd, composeInstallCmd string
	
	// Check if it's Amazon Linux (uses yum/dnf)
	if err := client.RunCommand("cat /etc/os-release | grep -i 'amazon linux'", nil, nil); err == nil {
		fmt.Fprintln(stdout, "🔍 Detected: Amazon Linux")
		dockerInstallCmd = "sudo yum update -y && sudo yum install -y docker && sudo systemctl start docker && sudo systemctl enable docker && sudo usermod -aG docker $USER"
		// Install Docker Compose v2 plugin and buildx
		composeInstallCmd = `sudo mkdir -p /usr/local/lib/docker/cli-plugins && \
sudo curl -SL https://github.com/docker/compose/releases/download/v5.0.1/docker-compose-linux-x86_64 -o /usr/local/lib/docker/cli-plugins/docker-compose && \
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose && \
sudo curl -SL https://github.com/docker/buildx/releases/download/v0.30.1/buildx-v0.30.1.linux-amd64 -o /usr/local/lib/docker/cli-plugins/docker-buildx && \
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-buildx`
	} else if err := client.RunCommand("cat /etc/os-release | grep -i 'ubuntu\\|debian'", nil, nil); err == nil {
		fmt.Fprintln(stdout, "🔍 Detected: Ubuntu/Debian")
		dockerInstallCmd = "curl -fsSL https://get.docker.com | sudo sh && sudo systemctl start docker && sudo systemctl enable docker && sudo usermod -aG docker $USER"
		composeInstallCmd = `sudo mkdir -p /usr/local/lib/docker/cli-plugins && \
sudo curl -SL https://github.com/docker/compose/releases/download/v5.0.1/docker-compose-linux-x86_64 -o /usr/local/lib/docker/cli-plugins/docker-compose && \
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose`
	} else {
		fmt.Fprintln(stdout, "🔍 Detected: Generic Linux (using Docker install script)")
		dockerInstallCmd = "curl -fsSL https://get.docker.com | sudo sh && sudo systemctl start docker && sudo systemctl enable docker && sudo usermod -aG docker $USER"
		composeInstallCmd = `sudo mkdir -p /usr/local/lib/docker/cli-plugins && \
sudo curl -SL https://github.com/docker/compose/releases/download/v5.0.1/docker-compose-linux-x86_64 -o /usr/local/lib/docker/cli-plugins/docker-compose && \
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-compose && \
sudo curl -SL https://github.com/docker/buildx/releases/download/v0.30.1/buildx-v0.30.1.linux-amd64 -o /usr/local/lib/docker/cli-plugins/docker-buildx && \
sudo chmod +x /usr/local/lib/docker/cli-plugins/docker-buildx`
	}

	steps := []struct {
		name     string
		check    string
		cmd      string
		skipMsg  string
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
			name:    "Setup Traefik",
			check:   "sudo docker ps | grep graft-traefik",
			cmd: `sudo tee /opt/graft/gateway/docker-compose.yml <<EOF
version: '3.8'
services:
  traefik:
    container_name: graft-traefik
    image: traefik:v3.6
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
      - "8080:8080"  # Traefik dashboard
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
		
		var services string
		if setupPostgres {
			ports := ""
			if exposePostgres {
				ports = `    ports:
      - "5432:5432"
`
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
`, ports, pgUser, pgPass, pgDB)
		}
		if setupRedis {
			ports := ""
			if exposeRedis {
				ports = `    ports:
      - "6379:6379"
`
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
		infraCfg := config.InfraConfig{
			PostgresUser:     pgUser,
			PostgresPassword: pgPass,
			PostgresDB:       pgDB,
		}
		data, _ := json.MarshalIndent(infraCfg, "", "  ")
		
		tmpFile := filepath.Join(os.TempDir(), "infra.config")
		os.WriteFile(tmpFile, data, 0644)
		defer os.Remove(tmpFile)

		if err := client.UploadFile(tmpFile, config.RemoteInfraPath); err != nil {
			fmt.Fprintf(stdout, "⚠️  Warning: Could not save infra credentials to remote server: %v\n", err)
		} else {
			fmt.Fprintln(stdout, "✅ Infra credentials saved to remote server")
		}
	} else {
		fmt.Fprintf(stdout, "\n⏭️  Skipping shared infrastructure setup\n")
	}

	return nil
}
