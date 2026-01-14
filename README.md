```text
 ██████  ██████   █████  ███████ ████████ 
██       ██   ██ ██   ██ ██         ██    
██   ███ ██████  ███████ █████      ██    
██    ██ ██   ██ ██   ██ ██         ██    
 ██████  ██   ██ ██   ██ ██         ██    
```

<p align="center">
  <strong>Deploy to production the same way you develop locally</strong>
</p>

<p align="center">
  <a href="https://graftdocs.vercel.app"><strong>Documentation</strong></a> |
  <a href="https://github.com/skssmd/Graft/issues"><strong>Support</strong></a>
</p>

---

## Your Docker Compose commands. Your production server. Zero friction.

```bash
# This works locally
docker compose up -d

# This works in production
graft up -d
```

**Same commands. Same workflow. Different server.**

No Kubernetes YAML. No proprietary DSLs. No monthly bills that scale with your traffic. Just SSH, Docker Compose, and automatic infrastructure setup.

---

## Why Graft exists

You've built something with Docker Compose. It works perfectly on your laptop. Now you need to deploy it.

Your options:
- **Learn Kubernetes** → Weeks of YAML hell for a simple app
- **Use a managed platform** → $50/month becomes $500/month when you succeed
- **Manual SSH deployment** → Spend 3 hours setting up Traefik, Let's Encrypt, networking...

**Or use Graft:** Point it at your server, run `graft sync`, and you're done.

```bash
graft init     # Configure once
graft sync     # Deploy everything
graft logs -f  # Check what's happening
```

Five minutes from bare VPS to live app with SSL, reverse proxy, and CI/CD.

---

## What you get

### 🎯 The core magic: Docker Compose, but remote

Every command you know works on production:

```bash
graft ps                          # What's running?
graft logs backend -f             # Follow logs in real-time
graft exec backend cat config.yml # Read files
graft restart frontend            # Bounce a service
graft pull && graft up -d         # Deploy updates
```

Your muscle memory still works. Your Stack Overflow bookmarks still work. The only difference is the server location.

### ⚡ Zero-config infrastructure

Point Graft at a clean Ubuntu/Debian server and it:
- Installs Docker automatically
- Configures Traefik reverse proxy
- Sets up SSL certificates via Let's Encrypt
- Creates isolated Docker networks
- Handles all the boring infrastructure work

You focus on shipping features. Graft handles the DevOps.

### 🚀 Deployment modes for every workflow

**Just ship it:** Direct sync mode
```bash
graft sync  # Rsync code → server builds → done
```

**Proper CI/CD:** GitHub Actions + GHCR
```bash
graft init  # Choose git-images mode
graft sync  # Auto-generates workflow, sets up webhooks, done
```

Graft writes production-ready GitHub Actions workflows with zero configuration. No copying from StackOverflow. No debugging YAML indentation at 2 AM.

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

```bash
# Install (Linux/macOS)
brew tap skssmd/tap && brew install graft

# Initialize project
graft init

# Deploy
graft sync

# Manage
graft ps                # Status check
graft logs backend -f   # Live logs
graft map               # Update DNS
graft rollback          # Undo deployment
```

**That's it.** Your app is live with SSL, reverse proxy, and CI/CD.

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

| You are... | Graft helps you... |
|------------|-------------------|
| 🚀 **Solo developer** | Ship side projects without platform bills or complex setups |
| 👥 **Small team** | Give everyone production access without scary manual SSH |
| 🏢 **Agency/Freelancer** | Manage 20+ client projects without losing your mind |
| ☁️ **Cloud optimizer** | Rotate between free tiers, avoid vendor lock-in |
| 🧪 **Rapid prototyper** | Go from VPS to live SSL URL in under 5 minutes |

### ❌ Not for you if...

- You need multi-region deployments across dozens of servers
- You're running Kubernetes already (and enjoying it?)
- You want a web UI instead of CLI

---

## Real-world comparison

| Task | Manual SSH | Dokku | Railway/Render | Graft |
|------|-----------|-------|----------------|-------|
| **Initial setup time** | 2-3 hours | 30 mins | 5 mins | 5 mins |
| **Learning curve** | High | Medium | Low | Minimal |
| **Monthly cost** | Server only | Server only | Scales with usage | Server only |
| **Multi-project management** | Nightmare | Manual | Vendor UI | `graft -p` |
| **Deploy mode flexibility** | Full control | Limited | None | Multiple modes |
| **Vendor lock-in** | None | None | High | None |
| **Works with existing Compose** | Yes | Needs buildpacks | No | Yes |

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

- [ ] Dev/staging environment separation
- [ ] Slack/Discord deployment notifications
- [ ] Built-in health checks and monitoring
- [ ] Multi-server orchestration

See [issues](https://github.com/skssmd/Graft/issues) for full roadmap.

---

## Contributing

Graft is open source and contributions are welcome.

**Before submitting features:** Open an issue to discuss. Graft intentionally stays simple—we evaluate new features against "does this solve a common problem without adding complexity?"

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

**Built by a developer tired of manually SSH-ing into servers to restart containers at 2 AM.**

If you know Docker Compose and have a server with SSH, you can use Graft. That's the whole requirement.
