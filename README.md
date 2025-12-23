```text
 ██████  ██████   █████  ███████ ████████ 
██       ██   ██ ██   ██ ██         ██    
██   ███ ██████  ███████ █████      ██    
██    ██ ██   ██ ██   ██ ██         ██    
 ██████  ██   ██ ██   ██ ██         ██    
```

# Graft 🚀

**Graft** is a lightweight, no-overhead deployment tool that simplifies deployment abstractness. Built directly on top of Docker Compose, its purpose is to provide a native experience for handling cloud servers without managed extra steps.

If you know and have worked with Docker Compose for local development services (what you run on localhost), you already know 90% of Graft. You can stay in your IDE or terminal and manage your web application exactly as you do on localhost, but you'll be managing your remote server.

There's no catch: no extra installs, no management agents, and no additional containers on your server. All you need is Docker and SSH. Graft can manage AWS EC2, Google Cloud VPS, Regular VPSs, and any Linux host you have access to.

> [!IMPORTANT]
> To set up your host, please use a **clean and fresh server**. Graft automatically configures Docker, reverse proxies (Traefik), and load balancers, which may conflict with existing manual setups.

## 🏗️ How it Works

Graft is entirely based on **Docker Compose**. Every Graft project is essentially a collection of source code and a special `graft-compose.yml` file.

When you run `graft init`, the tool provides a **template compose file** featuring a reverse proxy (Traefik) demonstration. This template serves as a working starting point that you can use for inspiration to build your own Graft-optimized compose configurations.

## ✨ Key Features

- **Instant Sync**: Deploy your local code to a remote server with `graft sync`. Supports rsync for speed and Git for version-specific deployments.
- **Centralized Gateway**: Automatically configures a centralized reverse proxy that works as a gateway for all your projects and webapps.
- **Shared Infrastructure**: Initialize shared Postgres or Redis instances that can be used across all projects. This allows for centralized management with shared credentials, while still keeping databases separate.
- **Project Contexts**: Jump between projects anywhere on your machine using `graft -p <name>`.
- **Flexible Data Layers**: Prefer isolation? You can always add fully separate database or Redis services directly to your project's `graft-compose.yml`.
- **Remote Project Registry**: Keep track of all your projects on the server with automated remote registration and conflict detection.
- **Registry Management**: Manage multiple servers in a global registry (`graft registry ls/add/del`).
- **Shell Access**: Direct interactive SSH sessions or non-interactive command execution via `graft -sh`.
- **Project Pulling**: "Clone" a project from a remote server to any local machine using `graft pull`.

## 🛠️ Installation & Build

### Quick Installation (Linux)
Install Graft globally with a single command:
```bash
curl -sSL https://raw.githubusercontent.com/skssmd/Graft/main/bin/install.sh | sh
```

### Quick Installation (Windows)
Download the binary via PowerShell:
```powershell
wget install graft
```

### Prerequisites
- **Go**: Version 1.24+ recommended.

### Building from Source
1. Clone this repository.
2. Build the binary using Go:
   ```bash
   go build -o graft.exe cmd/graft/main.go
   ```
3. (Optional) Add the binary to your system PATH for easier access.

## 📖 Documentation

For a complete reference of all available commands and flags, please see the [COMMANDS.md](COMMANDS.md) file.

## 🚀 Quick Start

```bash
# 1. Initialize a new project (select a server or add a new one)
graft init

# 2. Deploy your project for the first time
graft sync

# 3. Check service status
graft ps

# 4. View logs
graft logs backend
```

