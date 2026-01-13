```text
 ██████  ██████   █████  ███████ ████████ 
██       ██   ██ ██   ██ ██         ██    
██   ███ ██████  ███████ █████      ██    
██    ██ ██   ██ ██   ██ ██         ██    
 ██████  ██   ██ ██   ██ ██         ██    
```

# Graft 🚀

**Manage remote Docker Compose projects like they're running on localhost.**

Graft is a deployment tool for developers who know Docker Compose and want to keep using it in production. No new DSL to learn, no complex YAML configurations, no agent installations—just your familiar `docker-compose.yml` and some SSH magic.

Built for teams that want a simple production environment. If you can run `docker compose up` locally, you can deploy to production with Graft.

> [!IMPORTANT]
> Use a **clean server** for initial setup. Graft configures Docker, Traefik, and networking automatically, which may conflict with existing manual configurations.

---

## 🎯 What Problem Does This Solve?

**The typical deployment journey:**
1. Docker Compose works great locally
2. Time to deploy to production
3. Options: Use complex orchestration, pay for managed platforms, or SSH in manually and do boring setup
4. All of these suck in different ways

**Graft's approach:**
```bash
# Local development
docker compose up -d --pull always

# Production with Graft (same commands)
graft up -d --pull always
```

Same workflow. Different server. That's it.

**Graft is a simple tool for simple use cases.** Sometimes you just need to put your service online as fast as possible, make sure it stays healthy, and interact with it without verbose steps. No complex configuration, no cluster management.

---

## 🏗️ How It Works

**Graft bridges the gap between your local development and remote servers.** It takes your standard `docker-compose.yml` and pushes it to your server over SSH, handling all the complex infrastructure setup automatically.

- **Zero-Config Infrastructure**: Graft automatically configures your server—installing Docker, Traefik, and setting up secure networking and SSL certificates—so you don't have to.
- **Project Contexts**: It remembers which project belongs to which server, allowing you to manage dozens of deployments without manually switching SSH details.
- **Automated Workflows**: Graft generates production-ready GitHub Actions, handles environment variables securely, and sets up webhooks for seamless CI/CD.
- **Remote Passthrough**: Once set up, manage your production apps exactly like localhost. `graft ps`, `graft logs`, and `graft up` work just like their Docker Compose counterparts.

---

## ✨ Key Features

### Docker Compose Passthrough (The Main Event 🎪)
This is where Graft really shines: **any Docker Compose command works on remote servers, exactly as it does locally.**

```bash
graft ps                    # See what's running
graft logs backend -f       # Follow logs in real-time
graft restart frontend      # Restart a service
graft up -d                 # Start services detached
graft down                  # Stop everything
graft pull                  # Pull latest images
graft build --no-cache      # Rebuild from scratch
```

**One-liner exec/run commands work perfectly:**
```bash
graft exec backend ls -la              # Run single commands
graft exec backend cat /app/config.yml # Read files
graft run alpine echo "hello"          # Quick throwaway commands
```

**Automatic Dns Mapping for Cloudflare based DNSs:**
```bash
graft map  #Automatically detects domains by service and sets DNS 
```
**Easy Rollback to previous deployments:**
```bash
graft rollback  #Display previous deployments and allow you to rollback to any of them
graft rollback config #Set up how many versions to keep
``` 

**Important caveat:** Interactive sessions (like `graft exec -it backend bash`) don't work due to SSH-in-SSH limitations. For that, use `graft -sh` to drop into a proper SSH session first, then run your Docker commands there.

**All your muscle memory still works.** If you know Docker Compose, you know Graft. The only difference is your services are running on a server in some datacenter instead of melting your laptop's CPU.

### Deployment Modes
Choose how you want to deploy (you can switch anytime—commitment issues are valid):

**Direct mode** (no Git required):
- **Direct sync**: Rsync your code to the server, server builds images locally. Fast iteration, perfect for "just ship it" moments.

**Git-based modes** (proper CI/CD for people who like feeling professional):
- **GitHub Actions + GHCR**: Auto-generates workflow, builds images in the cloud, pushes to GitHub Container Registry, deploys via webhook. The full adult developer experience.
- **GitHub Actions + server build**: Triggers your server to pull from repo and build there. For when you trust your server's CPU more than GitHub's runners.
- **Git manual**: Sets up the Git integration, but you're in control of when to actually deploy. Trust issues mode.

**The workflow generation is legitimately impressive:** Point Graft at your compose file, tell it your mode, and it writes a production-ready GitHub Actions workflow with grafthook integration, secrets management, image cleanup, and zero debugging needed. No more copying workflows from StackOverflow and hoping for the best.

### Multi-Project & Server Management
**Built for managing multiple clients, projects, and servers without losing your mind:**
- **Project contexts**: `graft -p project1 ps` - Graft remembers which project belongs to which server. You don't have to.
- **Service-level control**: `graft -p project1 logs api` - Target specific services within projects
- **Server registry**: Manage multiple servers without juggling SSH config files or post-it notes
- **One-command DNS migration**: `graft dns map` - Point your Cloudflare DNS at the current server. Works great for rotating between cloud free tiers when you inevitably hit the limits.
- **Remote shell**: `graft -sh` - Drop into an SSH session when you need to dig around manually (we won't judge)

When you have 5 clients with 3 projects each across different servers, Graft handles the context switching so you can focus on the actual work.

### Infrastructure Automation
- **Traefik reverse proxy**: Auto-configured with SSL via Let's Encrypt
- **Shared services**: Optional Postgres/Redis instances shared across projects
- **DNS automation**: Update Cloudflare DNS records automatically on deployment
- **Network management**: Docker networks created and configured per project

### Migration Friendly
Built for rotating between cloud providers:
- Quick server initialization (5 minutes from fresh server to deployed)
- DNS sync on migration
- Works with AWS, GCP, DigitalOcean, any VPS with SSH

---

## 🛠️ Installation

### Linux

**Homebrew (macOS/Linux):**
```bash
brew tap skssmd/tap
brew install graft
```

**Debian/Ubuntu (APT):**
```bash
echo "deb [trusted=yes] https://apt.fury.io/skssmd/ /" | sudo tee /etc/apt/sources.list.d/graft.list
sudo apt update
sudo apt install graft
```

**Fedora/RHEL/Amazon Linux (YUM/DNF):**
```bash
echo "[graft]
name=Graft Repository
baseurl=https://yum.fury.io/skssmd/
enabled=1
gpgcheck=0" | sudo tee /etc/yum.repos.d/graft.repo
sudo yum install graft
```

**Snap Store:(under review)**
```bash
sudo snap install graft --classic
```

**Arch Linux (AUR):**
```bash
yay -S graft-bin
# or
paru -S graft-bin
```

**Shell Script:**
```bash
curl -sSL https://raw.githubusercontent.com/skssmd/Graft/main/bin/install.sh | sh
```

### Windows
```powershell
powershell -ExecutionPolicy ByPass -Command "iwr -useb https://raw.githubusercontent.com/skssmd/Graft/main/bin/install.ps1 | iex"
```

**Or via WinGet:**
```bash
winget install graft
```

### From Source
```bash
git clone https://github.com/skssmd/Graft
cd Graft
go build -o graft cmd/graft/main.go
```

**Requirements:** Go 1.24+, SSH access to a Linux server, Docker (installed automatically)

---

## 🚀 Quick Start

```bash
# 1. Initialize project (adds new server or selects existing)
graft init
# Choose deployment mode from interactive prompt
# (or change later with: graft mode)

# 2. Edit graft-compose.yml if needed

# 3. Deploy
graft sync

# 4. Manage like localhost
graft ps                    # Check status
graft logs backend          # View logs
graft restart frontend      # Restart service
graft map #automatically updates cloudflare dns records
graft rollback #roll back to previous versions
```

**That's it.** Your project is running on the server, managed via familiar commands.

---

## 📖 Documentation

**Full documentation:** [graftdocs.vercel.app](https://graftdocs.vercel.app)

**Common workflows:**

```bash
# Deploy with automatic GitHub Actions CI/CD
graft init                          # Select git-images mode from prompt
graft sync                          # Generates workflow, pushes to GitHub

# Change deployment mode
graft mode                          # Interactive mode selection

# Switch between projects
graft -p project1 logs              # Project1 logs
graft -p project1 logs api          # Specific service logs
graft -p project2 restart           # Restart project2

# DNS management (Cloudflare)
graft dns map                       # Update DNS to point to current server

# Server management
graft registry ls                   # List all servers
graft registry add prod user@ip     # Add new server

# Direct server access
graft -sh                           # SSH session
graft exec backend cat /app/log.txt # One-liner commands work
```

---

## 🎯 Use Cases

<table width="100%">
  <tr>
    <td width="50%" valign="top">
      <h4>🚀 Solo Developers</h4>
      <p>Ship side projects fast without the overhead of complex orchestration or managed platform bills.</p>
    </td>
    <td width="50%" valign="top">
      <h4>👥 Small Teams</h4>
      <p>Create a simple, robust production environment that everyone on the team can understand and manage.</p>
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <h4>🏢 Agencies & Freelancers</h4>
      <p>Juggling dozens of clients? Graft manages project context across servers so you don't have to.</p>
    </td>
    <td width="50%" valign="top">
      <h4>☁️ Cloud Migrators</h4>
      <p>Easily rotate between cloud free tiers and keep your setup portable and vendor-neutral.</p>
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <h4>🧪 Rapid Prototyping</h4>
      <p>Go from a "naked" VPS to a live, SSL-secured URL with CI/CD in under 5 minutes.</p>
    </td>
    <td width="50%" valign="top">
      <h4>📈 The "Mid-Stage" App</h4>
      <p>Perfect for when your app outgrows localhost but doesn't yet need enterprise-grade complexity.</p>
    </td>
  </tr>
</table>

### 🚫 Graft is NOT a match for:
- **Multi-region/server setup**: Graft is not suited for multi server architectural meshes.


---

## 🏷️ What Makes This Different

**vs Dokku/CapRover:** No web UI, pure CLI. More flexible deployment modes. Better for managing multiple projects across multiple servers.

**vs Railway/Render:** Self-hosted. No vendor lock-in. Works anywhere you have SSH. No monthly bills that scale with your success.

**vs Manual Deployment:** Automated setup, reverse proxy, DNS, CI/CD generation, multi-project management. All the boring infrastructure work is handled.

---

## 🔮 Roadmap

Planned features:
- Dev/prod environment separation
- Slack/Discord notifications
- Health checks and monitoring

See the full roadmap in issues or check the pinned discussion.

---

## 🤝 Contributing

Graft is open source and contributions are welcome. If you're using it and something breaks or could be better, open an issue.

**Before building new features:** Check if it's on the roadmap or open an issue to discuss. Graft intentionally stays simple—feature requests are evaluated against "does this solve a common problem simply?"

---

## 📝 License

MIT License - see [LICENSE](LICENSE) file.

---

## 💬 Support & Community

- **Issues**: Bug reports and feature requests
- **Discussions**: Questions, showcase your projects, general chat
- **GitHub**: Star if you find it useful, helps others discover it

---

## ⚠️ Disclaimer

Graft is a tool for developers who understand Docker and servers. It automates deployment, it doesn't make deployment decisions for you. 

Built by a developer tired of manually SSH-ing into servers to check logs. Might be useful to others in the same boat.

---

**TL;DR:** Docker Compose commands that work on remote servers. Deploy your projects without complex setup or paying for managed platforms. Works with any server you can SSH into.