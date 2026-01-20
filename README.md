```text
 ██████  ██████   █████  ███████ ████████ 
██       ██   ██ ██   ██ ██         ██    
██   ███ ██████  ███████ █████      ██    
██    ██ ██   ██ ██   ██ ██         ██    
 ██████  ██   ██ ██   ██ ██         ██    
```

<p align="center">
  <strong>If it works with Docker Compose, Graft can ship it in minutes</strong>
</p>

<p align="center">
  <a href="https://graftdocs.vercel.app"><strong>Documentation</strong></a> |
  <a href="https://github.com/skssmd/Graft/issues"><strong>Support</strong></a>
</p>

---

## Your Docker Compose commands. Your production server. Zero bloat.

**Got a working `docker-compose.yml`? You're 3 commands away from production.**

```bash
# This works locally
docker compose up -d

# This works in production (same exact commands)
graft up -d
```

**Same commands. Same workflow. Different server.** If your app runs locally with Docker Compose, Graft can deploy it in under 5 minutes.

No agents running on your server. No web UIs eating RAM. No control panels you never asked for. Just SSH, your existing compose file, and automatic infrastructure setup.

**Graft is completely agentless.** The only thing it leaves on your server is a tiny webhook for CI/CD (1.7MB RAM idle, ~10-15MB during deployments). Everything else runs from your machine.

---

## Why Graft exists

**You've already done the hard work.** Your app runs perfectly locally with Docker Compose. 

Now you just need it live. But somehow "just deploy it" turns into:
- **Dokploy/Coolify/CapRover** → Install a 500MB+ web UI on your server, click through dashboards
- **Managed platforms** → Rewrite configs for their format, watch costs scale with success
- **Manual SSH deployment** → Spend hours configuring Traefik, Let's Encrypt, networking, security...

**Graft takes your working compose file and ships it.** That's it.

```bash
graft init     # Point at your server (once)
graft sync     # Deploy everything
graft logs -f  # Verify it's running
```

From working locally to live with SSL in under 5 minutes. Your server's resources stay focused on your app, not management tools.

---

## What makes Graft different

### 🪶 Completely agentless

Graft runs from **your machine**, not your server. The only footprint on your server:

```
CONTAINER ID   NAME                   CPU %     MEM USAGE / LIMIT    MEM %     PIDS
0deea9bcd77e   webhook-graft-hook-1   0.00%     1.73MiB / 916.8MiB   0.19%     3
```

**1.7MB of RAM at idle. ~10-15MB during deployments.** That's the webhook for CI/CD triggers. Everything else is your actual application.

Compare to server management UIs that consume 500MB-1GB+ just sitting there with a web interface you barely use.

### 🎯 Docker Compose, but remote

Every command you know works on production:

```bash
graft ps                          # What's running?
graft logs backend -f             # Follow logs in real-time
graft exec backend cat config.yml # Read files
graft restart frontend            # Bounce a service
graft pull && graft up -d         # Deploy updates
```

No new syntax. No proprietary config files. If you know Docker Compose, you know Graft.

### ⚡ Zero-config infrastructure

Point Graft at a clean Ubuntu/Debian server and it:
- Installs Docker automatically
- Configures Traefik reverse proxy
- Sets up SSL certificates via Let's Encrypt
- Creates isolated Docker networks
- Handles all the boring infrastructure work

All done over SSH. Nothing installed server-side except what you actually need.

### 🚀 Flexible deployment modes

**Just ship it:** Direct sync mode
```bash
graft sync  # Rsync code → server builds → done
```

**Proper CI/CD:** GitHub Actions + GHCR
```bash
graft init  # Choose git-images mode
graft sync  # Auto-generates workflow, sets up webhooks, done
```

Graft writes production-ready GitHub Actions workflows with zero configuration. The webhook receiver on your server? That tiny 1.7MB container above.

### 🏢 Built for managing multiple projects

Managing 10 clients across 5 servers?

```bash
graft -p client1 logs api          # Project 1
graft -p client2 restart backend   # Project 2
graft -p client3 ps                # Project 3
```

Graft remembers which project lives where. You don't juggle SSH configs or server IPs.

**Bonus:** `graft dns map` updates Cloudflare DNS automatically. Perfect for rotating between cloud free tiers.

### 🔄 Bulletproof rollbacks

```bash
graft rollback  # See deployment history, choose which to restore
```

Every deployment is versioned. Broke production? Roll back in 10 seconds.

---

## Quick start

**Already have a working `docker-compose.yml`? You're ready.**

```bash
# Install (Linux/macOS)
brew tap skssmd/tap && brew install graft

# Initialize (point at your server)
graft init

# Deploy (that's it)
graft sync

# Manage like localhost
graft ps                # Status check
graft logs backend -f   # Live logs
graft map               # Update DNS
graft rollback          # Undo deployment
```

**Production-ready in under 5 minutes.** SSL, reverse proxy, and CI/CD all configured automatically.

---

## Installation

<details>
<summary><strong>Linux/macOS (Homebrew)</strong></summary>

```bash
brew tap skssmd/tap
brew install graft
```
</details>

<details>
<summary><strong>Debian/Ubuntu</strong></summary>

```bash
echo "deb [trusted=yes] https://apt.fury.io/skssmd/ /" | sudo tee /etc/apt/sources.list.d/graft.list
sudo apt update && sudo apt install graft
```
</details>

<details>
<summary><strong>Fedora/RHEL/Amazon Linux</strong></summary>

```bash
echo "[graft]
name=Graft Repository
baseurl=https://yum.fury.io/skssmd/
enabled=1
gpgcheck=0" | sudo tee /etc/yum.repos.d/graft.repo
sudo yum install graft
```
</details>

<details>
<summary><strong>Arch Linux (AUR)</strong></summary>

```bash
yay -S graft-bin
```
</details>

<details>
<summary><strong>Windows</strong></summary>

```powershell
powershell -ExecutionPolicy ByPass -Command "iwr -useb https://raw.githubusercontent.com/skssmd/Graft/main/bin/install.ps1 | iex"
```

Or via WinGet:
```bash
winget install graft
```
</details>

<details>
<summary><strong>From source</strong></summary>

```bash
git clone https://github.com/skssmd/Graft
cd Graft
go build -o graft cmd/graft/main.go
```

Requires Go 1.24+
</details>

---

## Who is this for?

**Graft is for small teams that don't need multi-server scalability** but want simple, reliable deployment without bloat.

| You are... | Graft helps you... |
|------------|-------------------|
| 🚀 **Solo developer** | Ship projects without platform bills or resource-hogging UIs |
| 👥 **Small team (2-10 people)** | Simple production setup everyone can use via CLI |
| 🏢 **Agency/Freelancer** | Manage 20+ client projects without server bloat |
| ☁️ **VPS optimizer** | Maximize server resources for apps, not management tools |
| 🧪 **Rapid prototyper** | VPS to live SSL URL in under 5 minutes |

### ❌ Not for you if...

- You need multi-region, multi-server orchestration (that's Kubernetes territory)
- You prefer clicking through web UIs over terminal commands
- You're already running enterprise-scale infrastructure

---

## Graft vs. the alternatives

| What | Server footprint | How you interact | Best for |
|------|-----------------|------------------|----------|
| **Graft** | ~2MB idle, ~15MB deploying | CLI from your machine | Developers who know Docker Compose |
| **Dokploy/Coolify** | 500MB-1GB+ (UI + agent) | Web interface on server | Teams that want UI-based management |
| **CapRover** | 300MB+ (UI + agent) | Web interface on server | Single-server apps with UI preference |
| **Railway/Render** | Nothing (fully managed) | Web dashboard | Teams with budget, want zero ops |
| **Manual setup** | Just your apps | SSH directly | Masochists with free time |

**The Graft difference:** Server resources go to **your applications**, not to management software you barely use.

---

## Documentation

**Full docs:** [graftdocs.vercel.app](https://graftdocs.vercel.app)

**Common commands:**

```bash
# Deployment
graft init                    # One-time setup
graft sync                    # Deploy/update
graft mode                    # Change deployment mode

# Management
graft ps                      # Container status
graft logs service -f         # Live logs
graft restart service         # Restart
graft exec service command    # Run commands

# Multi-project
graft -p project1 logs api    # Project-specific
graft -p project2 restart     # Switch contexts

# DNS & Servers
graft dns map                 # Update Cloudflare DNS
graft registry ls             # List servers
graft -sh                     # Direct SSH access

# Rollback
graft rollback               # Restore previous deployment
graft rollback config        # Configure retention
```

---

## Roadmap


- [ ] Slack/Discord deployment notifications
- [ ] Graft-Hook configurations


---

## Contributing

Graft is open source and contributions are welcome.

**Before submitting features:** Open an issue to discuss. Graft intentionally stays simple and lightweight—we evaluate new features against "does this solve a common problem without adding bloat?"

Bug reports, documentation improvements, and bug fixes are always appreciated.

---

## License

MIT License - see [LICENSE](LICENSE) file.

---

## Support

- **Issues:** Bug reports and feature requests
- **Discussions:** Questions and community chat
- **Star the repo** if you find it useful 🌟

---

**Built by developers tired of installing 500MB web UIs just to deploy a Docker Compose app.**

If you know Docker Compose and have a server with SSH, you can use Graft. That's the whole requirement. Your server's CPU and RAM stay focused on your actual applications, not on management software.


