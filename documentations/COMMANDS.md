# Graft CLI Commands Reference

Complete reference for all Graft commands.

---

## 🏗️ Architecture

Graft uses a **hybrid command architecture**:

1. **Native Commands** - Handled directly by Graft (init, host, sync, etc.)
2. **Passthrough Commands** - Any other command is forwarded to `docker compose` on the remote server
3. **How Commands Work** - For a project graft can be used in two ways.
    - **Current Project Folder(normal graft command)** - Graft will have context for the current project folder and communicate with the host(remote) server.
    - **Global (with -p flag)** - Graft will find the context for the project and communicate with the host(remote) server.
    - **Registry (with -r flag)** - Graft will find the registry(host) communicate directly.
---


## Project Commands

### `graft init`
Initialize a new Graft project in the current directory and configure server connection settings.

```bash
graft init [-f, --force]
```

**Interactive Setup Flow:**
1. **Server Selection**: Select an existing server from your global registry or type `/new`.
2. **Host Status Check**: Automatically checks if the remote host is initialized.
   - If `/opt/graft` is missing, prompts: `⚠️  Host is not initialized. Do you want to initialize the host? (y/n)`.
   - If "y", runs the complete host initialization process (`InitHost`).
3. **Registry Name** (if new): Provide a unique name to save the server globally.
4. **Project Name**: Name of your project (normalized to lowercase/underscores).
5. **Conflict Check**: Graft checks for existing projects with the same name:
   - **Locally**: If found, it prompts for confirmation.
   - **Remotely**: If found on the server, it will abort unless `-f` or `--force` is used.
6. **Domain Name**: The domain used for Traefik routing.
7. **Deployment Mode Selection**: Select from 5 modes (3 Git-based, 2 Direct).
   - For **Git-based modes**, Graft validates that a local git repository exists with a remote `origin`.
8. **Automated Service Setup**:
   - For automated Git modes (`git-images`, `git-repo-serverbuild`), Graft checks if the `graft-hook` service is running on the server.
   - If missing, it prompts for a webhook domain and deploys `graft-hook` automatically.
9. **Remote Environment Setup**:
   - For **Git modes**, Graft creates the remote project directory, ensures `git` is installed on the server, and initializes a git repo with your local remote.
   - For **Direct modes**, it simply creates the project directory.

**Creates:**
- `graft-compose.yml` - Docker Compose configuration
- `.graft/config.json` - Local server config
- `.graft/project.json` - Project metadata (name, remote path, deployment mode)
- `examples/github-actions-workflow.yml` - (For Git modes) Workflow template

**Remote Structure:**
```
/opt/graft/projects/
└── <project-name>/
    ├── graft-compose.yml
```

---

### `graft mode`
Change the deployment mode of an existing project.

```bash
graft mode
```

**Interactive Selection:**
Displays current deployment mode and prompts you to select a new one from 5 available modes.

**Deployment Modes:**

**Git-based modes:**
1. **git-images** - GitHub Actions builds images and pushes to GHCR, automated deployment via graft-hook webhook
   - Requires: GitHub Actions workflow, graft-hook service, GHCR authentication
   - Best for: Teams using GitHub Container Registry with automated deployments
   
2. **git-repo-serverbuild** - GitHub Actions triggers server to pull and build from repository
   - Requires: GitHub Actions workflow, graft-hook service, server Git access
   - Best for: Automated deployments with server-side builds
   
3. **git-manual** - Git repository setup without CI/CD workflow
   - Requires: Git repository, server Git access
   - Best for: Manual control with Git version tracking
   - Deploy with: `graft sync` when ready

**Direct deployment modes:**
4. **direct-serverbuild** - Upload source code, build on server (default)
   - Requires: Nothing extra
   - Best for: Simple projects, quick iterations
   
5. **direct-localbuild** - Build Docker images locally, upload to server
   - Requires: Docker on local machine
   - Best for: Complex builds, testing locally before deployment

**What it does:**
1. Shows current deployment mode
2. Prompts for new mode selection
3. Updates `.graft/project.json` with new mode
4. Regenerates `graft-compose.yml` with correct labels
5. Sets `initialized: false` (requires re-deployment)

**Example:**
```bash
$ graft mode

📦 Current deployment mode: direct-serverbuild

📦 Select New Deployment Mode:
  Git-based modes:
    [1] git-images (GitHub Actions → GHCR → automated deployment via graft-hook)
    [2] git-repo-serverbuild (GitHub Actions → server build → automated deployment)
    [3] git-manual (Git repo only, no CI/CD workflow provided)

  Direct deployment modes:
    [4] direct-serverbuild (upload source → build on server)
    [5] direct-localbuild (build locally → upload image)

Select deployment mode [1-5]: 1

✅ Git-based image deployment selected (GHCR)

🔄 Regenerating graft-compose.yml with new deployment mode...

✅ Deployment mode changed to: git-images
📝 Updated files:
   - .graft/project.json
   - graft-compose.yml

💡 Don't forget to set up GitHub Actions workflow!
   See: examples/github-actions-workflow.yml
```

**After changing mode:**
- Run `graft sync` to deploy with the new mode
- For git-images or git-repo-serverbuild: Set up GitHub Actions workflow
- For git modes: Configure server access to your Git repository

---

### `graft host init`
Initialize the remote server with Docker, Docker Compose, Traefik, and optionally shared infrastructure.

```bash
graft host init
```

**What it does:**
- Detects OS (Amazon Linux, Ubuntu/Debian, or generic Linux)
- Installs Docker and Docker Compose v2 (as plugin)
- Installs Docker Buildx for multi-platform builds
- Creates `graft-public` network
- Sets up Traefik reverse proxy with:
  - ✅ HTTPS/Let's Encrypt support
  - ✅ HTTP to HTTPS redirect
  - ✅ Automatic SSL certificate management
- Optionally sets up shared Postgres and Redis (separate prompts for each)

**Features:**
- ✅ OS-agnostic installation
- ✅ Automatic HTTPS with Let's Encrypt
- ✅ HTTP to HTTPS redirect
- ✅ Skips already completed steps

---

### `graft host clean`
Clean up Docker resources on the server.

```bash
graft host clean
```

**Cleans:**
- Stopped containers
- Unused images
- Build cache
- Unused volumes
- Unused networks

---

### `graft host self-destruct`
**⚠️ DESTRUCTIVE OPERATION ⚠️** - Completely tear down all Graft infrastructure on the server.

```bash
graft host self-destruct
```

**What it does:**
1. Discovers all projects on the server
2. Tears down all projects (stops containers, removes volumes)
3. Tears down infrastructure (Postgres, Redis with ALL DATA)
4. Tears down gateway (Traefik, including SSL certificates)
5. Removes all Docker images
6. Removes Graft networks
7. Deletes all files in `/opt/graft/`

**Safety features:**
- Requires typing `DESTROY` to confirm
- Requires typing `YES` for final confirmation
- Shows exactly what will be deleted
- Cannot be undone

**Use when:**
- Completely removing Graft from a server
- Starting fresh with a clean slate
- Decommissioning a server
- Testing/development cleanup

**Note:** Docker and Docker Compose remain installed. You can run `graft host init` afterwards to set up a fresh environment.

---



## Infrastructure Commands

### `graft db <name> init`
Initialize a managed Postgres database.

```bash
graft db mydb init
```

**What it does:**
- Creates database in shared Postgres container
- Generates connection URL
- Saves secret as `GRAFT_POSTGRES_<NAME>_URL`

**Usage in graft-compose.yml:**
```yaml
environment:
  - DATABASE_URL=${GRAFT_POSTGRES_MYDB_URL}
```

---

### `graft redis <name> init`
Initialize a managed Redis instance.

```bash
graft redis mycache init
```

**What it does:**
- Creates Redis database (separate DB number)
- Generates connection URL
- Saves secret as `GRAFT_REDIS_<NAME>_URL`

**Usage in graft-compose.yml:**
```yaml
environment:
  - REDIS_URL=${GRAFT_REDIS_MYCACHE_URL}
```

---

### `graft infra [db|redis] ports:<value>`
Manage port visibility for shared infrastructure services.

```bash
# Reveal Postgres port 5432 to the internet
graft infra db ports:5432

# Hide Postgres port from the internet
graft infra db ports:null

# Reveal Redis port 6379 to the internet
graft infra redis ports:6379

# Hide Redis port from the internet
graft infra redis ports:null
```

**What it does:**
- Updates the remote infrastructure configuration.
- Modifies the host port mapping in the shared `docker-compose.yml`.
- Restarts the infrastructure stack to apply changes.
- Syncs the port setting to your local project configuration.

---

### `graft infra reload`
Pull and reload infrastructure services (Postgres and Redis).

```bash
graft infra reload
```

**What it does:**
- Pulls the latest images for infrastructure services (Postgres, Redis).
- Restarts the infrastructure stack with the latest images.
- Uses `docker compose up -d --pull always` to ensure latest versions.

**Use when:**
- You want to update Postgres or Redis to the latest version
- After infrastructure configuration changes
- To ensure infrastructure is running the latest stable images

---

### `graft infra db backup`
Schedule or run an automated database backup to an S3-compatible service.

```bash
graft infra db backup
```

**What it does:**
- Interactively prompts for S3 credentials (if not configured).
- Offers to setup a daily cron job (2 AM) for automated backups.
- Generates and uploads a `backup.sh` script to the server.
- Uses a Docker container (`amazon/aws-cli`) to perform the upload safely.
- Optionally saves S3 credentials on the server for automated tasks.

**Interactive Flow:**
1. **S3 Config**: Provide Endpoint, Region, Bucket, Access Key, and Secret Key.
2. **Scheduling**: Choose to enable daily backups via cron.
3. **Save Info**: Choose to persist credentials on the server.
4. **Immediate Backup**: Option to run the first backup immediately to verify setup.

---

## Deployment Commands

### `graft sync`
Deploy all services to the server.

```bash
graft sync                    # Deploy all services
graft sync --no-cache         # Force fresh build (clears cache)
graft sync -h                 # Heave sync (upload only, no build)
```

**What it does:**
1. Updates project metadata (`.graft/project.json`)
2. Creates remote directory `/opt/graft/projects/<project-name>/`
3. Uploads source code for `serverbuild` services
4. Injects secrets from `.graft/secrets.env`
5. Uploads `docker-compose.yml`
6. Builds and starts all services (skipped if -h is used)
7. Cleans up old images (skipped if -h is used)

**Modes:**
- **Normal:** Uses Docker cache for faster builds
- **--no-cache:** Clears build cache and forces fresh build
- **-h, --heave:** Heave sync. Performs uploads but skips the build and start steps on the server. Useful for stage-building or manual verification.

**Service Types:**
- **Build-based services** (with `build` context): Source code is uploaded and built on the server
- **Image-based services** (with `image` only): Latest image is pulled from registry before starting
  - ✅ Automatically pulls latest image version
  - ✅ No source code upload needed
  - ✅ Perfect for using pre-built images from Docker Hub or private registries

**Git-Based Sync:**

Deploy from specific git commits or branches instead of your working directory:

```bash
# Deploy latest commit on current branch
graft sync --git

# Deploy from specific branch
graft sync --git --branch develop

# Deploy specific commit
graft sync --git --commit abc1234

# Combine with other flags
graft sync --git --branch main --no-cache
```

**Git Flags:**
- `--git` - Enable git-based deployment
- `--branch <name>` - Deploy from specific branch (default: current branch)
- `--commit <hash>` - Deploy specific commit (default: latest on branch)

**How it works:**
1. Checks for `.git` directory
2. Exports specified commit using `git archive` (doesn't modify working directory)
3. Uploads exported files to server
4. Builds and deploys normally

**Benefits:**
- ✅ Deploy exact commit state (ignores uncommitted changes)
- ✅ Working directory remains unchanged
- ✅ Perfect for CI/CD pipelines
- ✅ Deploy historical commits for rollback

---

### `graft sync <service>`
Deploy a specific service only.

```bash
graft sync backend            # Deploy only backend
graft sync frontend           # Deploy only frontend
graft sync backend --no-cache # Force fresh build
graft sync backend -h         # Upload code for backend ONLY (no build)

# Git-based service sync
graft sync backend --git                    # Deploy backend from latest commit
graft sync frontend --git --branch develop  # Deploy frontend from develop branch
```

**What it does:**
1. Updates project metadata
2. Stops and removes old container (skipped if -h is used)
3. Uploads source code for that service
4. Rebuilds only that service (skipped if -h is used)
5. Starts the updated container (skipped if -h is used)
6. Cleans up old images (skipped if -h is used)

**Benefits:**
- ✅ Much faster than full sync
- ✅ Other services keep running
- ✅ Perfect for iterative development

---

### `graft sync compose`
Update only the docker-compose.yml without rebuilding images.

```bash
graft sync compose              # Update and restart
graft sync compose -h           # Upload only (no restart)
```

**What it does:**
1. Uploads updated `graft-compose.yml`
2. Restarts services with new configuration (skipped if -h is used)
3. Skips image building

**Use for quick changes to:**
- Environment variables
- Port mappings
- Network configurations
- Volume mounts
- Traefik labels/routing

---

## Rollback Commands

Manage project versioning and rollbacks. Graft automatically creates a backup of your configuration and images during every `sync`.

### `graft rollback`
Roll back the entire project to a previous backup version.

```bash
graft rollback
```

**What it does:**
1. Lists available backups with timestamps.
2. Interactively prompts you to select a version.
3. Restores `docker-compose.yml` and environment files.
4. Stops current services and clearing images to prevent tag conflicts.
5. Loads and re-tags compressed images from the backup.
6. Restarts all services using the restored versions with `--pull never`.

---

### `graft rollback service <name>`
Roll back only a specific service to a previous version.

```bash
graft rollback service backend
```

**What it does:**
1. Lists available backups.
2. Extracts the specific service configuration from the selected backup.
3. Updates the remote `docker-compose.yml` for only that service.
4. Stops the service and restores its specific image.
5. Restarts the targeted service without affecting others using `--pull never`.

---

### `graft rollback config`
Configure how many rollback versions Graft should keep on the server.

```bash
graft rollback config
```

**Interactive Options:**
- **Change Limit**: Set how many historical versions to retain (default: 3).
- **Disable Rollbacks**: Set limit to `0` to stop creating backups.
- **Remove Config**: Delete the rollback configuration from the project.

**Note:** Changes are synced to both your local `.graft/project.json` and the remote `graft-hook` configuration.

---

## DNS Mapping Commands

### `graft map`
Automatically map all service domains to Cloudflare DNS records.

```bash
graft map
```

**What it does:**
1. Parses `graft-compose.yml` to extract all Traefik `Host()` rules
2. Detects the server's public IP address
3. Prompts for Cloudflare API credentials (saved for future use)
4. Verifies DNS ownership via Cloudflare API
5. For each domain:
   - Checks if DNS record exists
   - Creates new A record if missing
   - Prompts to update if existing record points to different IP
6. Displays summary of changes

**Requirements:**
- Cloudflare API Token with DNS edit permissions
- Zone ID for your domain
- Services must have Traefik labels with `Host()` rules

**Example:**
```bash
$ graft map

🔍 Parsing docker-compose file...
📋 Found 3 service(s) with domains:
  - frontend: app.example.com, www.example.com
  - backend: api.example.com
  - admin: admin.example.com

🌐 Detecting server IP...
Detected IP: 203.0.113.42

🔐 Cloudflare API Token: ****
🔐 Zone ID: ****
✅ Cloudflare credentials saved

🔐 Verifying DNS ownership...
✅ DNS ownership verified

📍 Checking DNS records...
  ✅ app.example.com → 203.0.113.42 (already correct)
  ⚠️  www.example.com → 198.51.100.10 (exists, current IP)
      Overwrite with 203.0.113.42? (y/n): y
      ✅ Updated
  ➕ api.example.com → Creating new record...
      ✅ Created
  ➕ admin.example.com → Creating new record...
      ✅ Created

✅ DNS mapping complete!
  - 1 unchanged
  - 1 updated
  - 2 created
```

---

### `graft map service <service-name>`
Map DNS records for a specific service only.

```bash
graft map service backend
graft map service frontend
```

**What it does:**
Same as `graft map` but only processes the specified service.

**Example:**
```bash
$ graft map service backend

🔍 Mapping service: backend
📋 Found domain(s): api.example.com

🌐 Detecting server IP...
Server IP: 203.0.113.42

🔐 Verifying DNS ownership...
✅ DNS ownership verified

📍 Processing DNS records...
➕ api.example.com → Creating new record...
    ✅ Created: api.example.com → 203.0.113.42

✅ DNS mapping complete for service: backend
```

**Benefits:**
- ✅ Automates DNS record creation
- ✅ Prevents manual DNS configuration errors
- ✅ Saves Cloudflare credentials for future use
- ✅ Safe overwrite prompts for existing records
- ✅ Works with multiple domains per service

---

## Monitoring Commands

### `graft logs <service>`
Stream live logs from a service.

```bash
graft logs backend
graft logs frontend
```

**Features:**
- ✅ Live streaming (follow mode)
- ✅ Shows last 100 lines
- ✅ Press Ctrl+C to stop

---

### `graft hook logs`
Monitor the `graft-hook` service logs, including build errors and deployment events.

```bash
graft hook logs graft-hook
```

**Features:**
- ✅ Real-time monitoring of webhook events
- ✅ View detailed build and deployment errors
- ✅ Perfect for debugging CI/CD pipeline failures
- ✅ Press Ctrl+C to stop

---

## Docker Compose Passthrough

**Any command not listed above is automatically passed to `docker compose` on the remote server!**

This means you can use **any** Docker Compose command through Graft:

### Container Management

```bash
# View container status
graft ps

# Start/stop services
graft up                              # Start all services
graft up backend                      # Start specific service
graft down                            # Stop all services
graft stop backend                    # Stop specific service
graft start frontend                  # Start specific service
graft restart backend                 # Restart specific service

# Remove containers
graft rm backend                      # Remove stopped container
graft rm -f backend                   # Force remove container
```

### Debugging & Inspection

```bash
# Execute commands in running containers
graft exec backend ls -la /app
graft exec backend /app/main --version
graft exec backend sh                 # Interactive shell

# Run one-off commands in new containers
graft run backend ls -la /app
graft run backend /app/main
graft run backend sh                  # Interactive shell (new container)

# View logs (alternative to graft logs)
graft logs backend --tail=50
graft logs backend --follow
graft logs backend --since 1h

# Inspect containers
graft top                             # Show running processes
graft top backend                     # Show processes in specific service
graft port backend 5000               # Show port mapping
```

### Images & Builds

```bash
# View images
graft images

# Pull images
graft pullfromhost                          # Pull all images
graft pullfromhost backend                    # Pull specific image

# Build images
graft build                           # Build all images
graft build backend                   # Build specific image
graft build --no-cache backend        # Build without cache
```

### Configuration

```bash
# View merged configuration
graft config                          # Show final docker-compose config

# Validate configuration
graft config --quiet                  # Validate without output
```

### Resource Management

```bash
# Pause/unpause services
graft pause backend
graft unpause backend

# Scale services
graft up --scale backend=3            # Run 3 backend instances
```

---

## 📂 Project & Registry Management

### `graft registry ls`
List all servers stored in your global registry (`~/.graft/registry.json`).

### `graft registry add`
Interactively add a new server to your global registry without initializing a project.

### `graft registry <name> del`
Remove a server from your global registry.

### `graft projects ls`
List all projects registered on your local system, including their bound server names and local paths.

### `graft -r <name> projects ls`
List all projects currently registered on the target remote server.

### `graft -r <name> pull <project>`
Download an existing project from a remote server to your local machine.
- Creates a new directory at `~/graft/<project>`.
- Syncs all project files using `rsync`.
- Automatically initializes the local `.graft/` configuration.
- Registers the project in your local global registry for immediate use with `-p`.

---

## Global Context Flag

### `-p, --project <name>`
Run any Graft command for a specific project from **any directory**.

```bash
graft -p my-project sync -h
graft -p my-project ps
graft -p my-project logs backend
```

**How it works:**
- Graft looks for the project name in the global registry (`~/.graft/registry.json`).
- If found, it automatically changes the working directory to the project's absolute path.
- The command is then executed as if you were inside that directory.
- This works for both native and passthrough commands.

```bash
graft -r prod-us projects ls
graft -r prod-us pull my-project
graft -r prod-us -sh uname -a
graft -r prod-us -sh
```

### `-sh, --sh [command]`
Execute a shell command on the target or start an interactive SSH session.
- If a command is provided, it executes non-interactively.
- If no command is provided, it starts an **interactive SSH session** (TTY).
- Works with both `-r <registry>` and `-p <project>` flags.

---


## 💻 Shell & SSH Access

### `graft -sh`
Open an interactive SSH session to the server in the current project context.

### `graft -sh <command>`
Execute a single command on the remote server.

### `graft host sh [command]`
Alias for shell access to the current project's host.
- `graft host sh` - Interactive session.
- `graft host sh df -h` - Execute command.

### `graft -r <srv> -sh`
Open an interactive SSH session to a specific server from the registry.

### `graft -p <proj> host sh`
Open an interactive SSH session to a specific project's host.

---


## Common Workflows

### Development Workflow
```bash
# 1. Make changes locally
# 2. Deploy specific service
graft sync backend

# 3. Check logs
graft logs backend

# 4. Debug if needed
graft exec backend sh

# 5. Check status
graft ps
```

### Debugging Failed Deployments
```bash
# 1. Check container status
graft ps

# 2. View logs (if running)
graft logs backend

# 3. Test in new container (if failed to start)
graft run backend ls -la /app
graft run backend ldd /app/main
graft run backend /app/main

# 4. Interactive debugging
graft run backend sh

# 5. Redeploy with fresh build
graft sync backend --no-cache
```

### Quick Compose Updates
```bash
# Update environment variables or labels
# Edit graft-compose.yml locally, then:
graft sync compose

# Restart to apply changes
graft restart backend
```

### Git-Based Deployment
```bash
# Deploy specific commit (e.g., for rollback)
graft sync --git --commit abc1234

# Deploy from feature branch for testing
graft sync --git --branch feature/new-api

# Deploy specific service from git
graft sync backend --git --branch develop

# CI/CD pipeline usage
graft sync --git --commit $CI_COMMIT_SHA --no-cache
```

---

## File Structure

> [!NOTE]
> The `frontend` and `backend` directories shown below are just for reference. You can set up your own folder structure and services exactly as you would with standard Docker Compose.

### Local Project
```
your-project/
├── graft-compose.yml          # Docker Compose configuration
├── .graft/
│   ├── config.json           # Server connection config
│   ├── project.json          # Project metadata (name, remote path)
│   └── secrets.env           # Injected secrets (DB URLs, etc.)
├── backend/
│   └── dockerfile
└── frontend/
    └── dockerfile
```

### Remote Server
```
/opt/graft/
├── gateway/
│   ├── docker-compose.yml    # Traefik configuration
│   └── letsencrypt/          # SSL certificates
├── infra/
│   └── docker-compose.yml    # Shared Postgres & Redis
└── projects/
    └── <project-name>/       # Your project (isolated directory)
        ├── docker-compose.yml
        ├── backend/          # Uploaded source code
        └── frontend/         # Uploaded source code
```

---

## Environment Variables

Graft automatically injects secrets from `.graft/secrets.env` into your `graft-compose.yml`.

**Example `.graft/secrets.env`:**
```env
GRAFT_POSTGRES_MYDB_URL=postgres://graft:password@graft-postgres:5432/mydb
GRAFT_REDIS_MYCACHE_URL=redis://graft-redis:6379/0
```

**Usage in graft-compose.yml:**
```yaml
services:
  backend:
    environment:
      - DATABASE_URL=${GRAFT_POSTGRES_MYDB_URL}
      - REDIS_URL=${GRAFT_REDIS_MYCACHE_URL}
```

---

## Tips & Best Practices

### When to Use --no-cache
- ✅ After major dependency changes
- ✅ When cache is corrupted/stale
- ✅ For production deployments
- ✅ When troubleshooting build issues

### Quick Compose Updates
Use `graft sync compose` for:
- Changing environment variables
- Updating port mappings
- Modifying Traefik labels
- Adjusting resource limits

### Project Naming
- Avoid naming your project "projects" (confusing path: `/opt/graft/projects/projects/`)
- Use descriptive names like "myapp", "api", "web", etc.
- Project name determines remote path: `/opt/graft/projects/<name>/`

### Passthrough Commands
- Any command not recognized by Graft is passed to `docker compose`
- This means new Docker Compose features work automatically
- Use `docker compose --help` to see all available commands

---

## Common Issues

### "No config found"
**Solution:** Run `graft config` to set up server connection.

### "graft-compose.yml not found"
**Solution:** Run `graft init` to initialize the project.

### "Could not load project metadata"
**Solution:** Run `graft sync` to create/update `.graft/project.json`.

### Build cache issues
**Solution:** Use `graft sync --no-cache` to force fresh build.

### Container won't start
**Debug steps:**
1. Check logs: `graft logs <service>`
2. Debug: `graft run <service> sh`
3. Check binary: `graft run <service> ls -la /app`
4. Test execution: `graft run <service> /app/main`

### Wrong remote path
**Solution:** Run `graft sync` to update project metadata with correct path.

---

## Quick Start

```bash
# 1. Configure server connection and Initialize project
graft init

# 2. Initialize server
graft host init

# 3. Setup database (optional)
graft db mydb init

# 4. Deploy
graft sync

# 5. Check status
graft ps

# 6. View logs
graft logs backend

# 7. Debug if needed
graft exec backend sh
```

---

## Command Summary

### Native Graft Commands
- `graft init [-f]` - Initialize project (configures server & project)
- `graft mode` - Change project deployment mode
- `graft registry [ls|add|del]` - Manage registered servers
- `graft projects ls` - List local projects
- `graft -p <name> host [sh]` - Project-bound shell access
- `graft -r <srv> [projects ls|pull|-sh]` - Server-context commands
- `graft -sh [cmd]` - Execute directly on target server
- `graft host init/clean/sh` - Manage current server context
- `graft infra [db|redis] ports:<v>` - Manage infra ports
- `graft db <name> init` - Create database
- `graft redis <name> init` - Create Redis instance
- `graft sync [service] [-h] [--git] [--branch <name>] [--commit <hash>]` - Deploy
- `graft sync compose [-h]` - Update compose only
- `graft logs <service>` - Stream logs
- `graft map` - Map all service domains to Cloudflare DNS
- `graft map service <name>` - Map specific service domain to Cloudflare DNS

### Passthrough Commands (via docker compose)
- `graft ps` - Container status
- `graft up/down` - Start/stop services
- `graft exec <service> <cmd>` - Execute in container
- `graft run <service> <cmd>` - Run in new container
- `graft restart/stop/start <service>` - Manage services
- `graft images/pull/build` - Manage images
- **...and any other docker compose command!**
