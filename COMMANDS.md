# Graft CLI Commands Reference

Complete reference for all Graft commands.

---

## 🏗️ Architecture

Graft uses a **hybrid command architecture**:

1. **Native Commands** - Handled directly by Graft (init, host, sync, etc.)
2. **Passthrough Commands** - Any other command is forwarded to `docker compose` on the remote server

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

---

## Project Commands

### `graft init`
Initialize a new Graft project in the current directory and Configure server connection settings.


```bash
graft init [-f, --force]
```

**Interactive Setup:**
1. **Server Selection**: Select an existing server from your global registry or type `/new`.
2. **Conflict Check**: Graft checks for existing projects with the same name:
   - **Locally**: If found in your global registry, it prompts you with the existing path/host and asks for confirmation (y/n).
   - **Remotely**: It checks the server registry. If found, it will abort unless `-f` or `--force` is used.
2. **Host Configuration** (if new): Enter IP, port, user, and key path.
3. **Registry Name** (if new): Provide a unique name to save the server globally.
4. **Project Name**: Name of your project (normalized to lowercase/underscores).
5. **Domain Name**: The domain Traefik will use for routing.

**Creates:**
- `graft-compose.yml` - Docker Compose configuration
- `.graft/config.json` - Local server config (if using --local)
- `.graft/project.json` - Project metadata (name, remote path)

**Remote Structure:**
```
/opt/graft/projects/
└── <project-name>/
    ├── graft-compose.yml
```

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
graft pull                            # Pull all images
graft pull backend                    # Pull specific image

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
# 1. Configure server connection
graft config

# 2. Initialize server
graft host init

# 3. Initialize project
graft init

# 4. Setup database (optional)
graft db mydb init

# 5. Deploy
graft sync

# 6. Check status
graft ps

# 7. View logs
graft logs backend

# 8. Debug if needed
graft exec backend sh
```

---

## Command Summary

### Native Graft Commands
- `graft config` - Configure server
- `graft host init/clean` - Manage server
- `graft init` - Initialize project
- `graft db <name> init` - Create database
- `graft redis <name> init` - Create Redis instance
- `graft sync [service] [--no-cache] [-h] [--git] [--branch <name>] [--commit <hash>]` - Deploy
- `graft sync compose [-h]` - Update compose only
- `graft logs <service>` - Stream logs

### Passthrough Commands (via docker compose)
- `graft ps` - Container status
- `graft up/down` - Start/stop services
- `graft exec <service> <cmd>` - Execute in container
- `graft run <service> <cmd>` - Run in new container
- `graft restart/stop/start <service>` - Manage services
- `graft images/pull/build` - Manage images
- **...and any other docker compose command!**
