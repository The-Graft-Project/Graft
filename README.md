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

Built for the solo developer rotating between cloud free tiers and the small team that finds Kubernetes overkill. If you can run `docker compose up` locally, you can deploy to production with Graft.

> [!IMPORTANT]
> Use a **clean server** for initial setup. Graft configures Docker, Traefik, and networking automatically, which may conflict with existing manual configurations.

---

## 🎯 What Problem Does This Solve?

**The typical deployment journey:**
1. Docker Compose works great locally
2. Time to deploy to production
3. Options: Learn Kubernetes, pay for managed platforms, or SSH in manually
4. All of these suck in different ways

**Graft's approach:**
```bash
# Local development
docker compose up -d --pull always

# Production with Graft (same commands)
graft up -d --pull always
```

Same workflow. Different server. That's it.

**Graft is a simple tool for simple use cases.** Sometimes you don't need Kubernetes-level features—you just need to put your service online as fast as possible, make sure it stays healthy, and interact with it without verbose steps. No manifest sprawl, no cluster management.

---

## 🏗️ How It Works

**Core concept:** Graft extends Docker Compose to remote servers over SSH.

Every Graft project has:
- Your existing `docker-compose.yml`
- A `graft-compose.yml` (Graft-specific context—basically a smarter docker-compose that Graft reads)
- A `.graft` folder (stores server config, project context, and all the boring infrastructure details)
- SSH access to a server

When you run `graft init`, it:
1. Attaches to your server (or registers a new one if this is your first rodeo)
2. Checks if the server is ready—if not, it installs Docker, Traefik, configures networking, and basically does all the boring sysadmin stuff you'd rather not do
3. Asks you which deployment mode you want (or skip it, you can configure later when you've had your coffee)
4. Installs grafthook if needed (for webhook-based deployments—it's like a doorbell for your CI/CD)
5. Generates a `graft-compose.yml` boilerplate because blank files are scary

When you run `graft sync`, it:
1. Checks your mode and project context (Graft remembers which project belongs to which server—better memory than you after a long weekend)
2. Updates or generates Dockerfiles based on your context (smart templating, not just copy-paste)
3. Extracts environment variables from `graft-compose.yml` into a separate `.env` file and wires it to your `docker-compose.yml` (because mixing config and secrets is a recipe for accidentally committing API keys)
4. Generates a GitHub Actions workflow that actually works—with grafthook integration, proper secrets handling, and zero additional configuration needed (assuming your `graft-compose.yml` isn't a disaster)
5. Asks nicely if you want to send environment variables directly to the server (secure transfer, not shouting them over SSH)
6. Syncs your source code based on mode—rsync for direct modes, or politely asks Git to handle it for CI/CD modes

Then you manage it like localhost: `graft ps`, `graft logs`, `graft restart frontend`—all the Docker Compose commands you already know.

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
- Project export/import
- Works with AWS, GCP, DigitalOcean, any VPS with SSH

---

## 🛠️ Installation

### Linux
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

**Full command reference:** [COMMANDS.md](COMMANDS.md)

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

**Perfect for:**
- Solo developers with side projects who need to ship fast
- Small teams not ready for Kubernetes (and probably don't need it)
- Agencies or freelancers juggling multiple clients and projects across different servers—Graft keeps track so you don't have to
- Migrating between cloud providers (free tier rotation is a valid business strategy - i think :P)
- Rapid prototyping and iteration without deployment overhead
- Projects that outgrew localhost but don't need enterprise orchestration

**Not for:**
- Multi-region deployments
- Multi-server deployments
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

**TL;DR:** Docker Compose commands that work on remote servers. Deploy your projects without learning Kubernetes or paying for managed platforms. Works with any server you can SSH into.