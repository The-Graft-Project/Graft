package executors

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/deploy"
	"github.com/skssmd/graft/internal/git"
	"github.com/skssmd/graft/internal/hostinit"
	"github.com/skssmd/graft/internal/ssh"
)

type Executor struct {
	Env    string
	Server config.ServerConfig
}

func GetExecutor() *Executor {
	return &Executor{
		Env: "prod",
	}
}

func (e *Executor) RunMode() {
	reader := bufio.NewReader(os.Stdin)

	// Load project metadata
	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	// Display current mode
	currentMode := meta.DeploymentMode
	if currentMode == "" {
		currentMode = "direct-serverbuild (default)"
	}
	fmt.Printf("\n📦 Current deployment mode: %s\n", currentMode)

	// Display mode options
	fmt.Println("\n📦 Select New Deployment Mode:")
	fmt.Println("  Git-based modes:")
	fmt.Println("    [1] git-images (GitHub Actions → GHCR → automated deployment via graft-hook)")
	fmt.Println("    [2] git-repo-serverbuild (GitHub Actions → server build → automated deployment)")
	fmt.Println("    [3] git-manual (Git repo only, no CI/CD workflow provided)")
	fmt.Println("\n  Direct deployment modes:")
	fmt.Println("    [4] direct-serverbuild (upload source → build on server)")
	fmt.Println("    [5] direct-localbuild (build locally → upload image)")
	fmt.Print("\nSelect deployment mode [1-5]: ")

	modeInput, _ := reader.ReadString('\n')
	modeInput = strings.TrimSpace(modeInput)

	var newMode string
	switch modeInput {
	case "1":
		newMode = "git-images"
		fmt.Println("\n✅ Git-based image deployment selected (GHCR)")
	case "2":
		newMode = "git-repo-serverbuild"
		fmt.Println("\n✅ Git-based server build deployment selected")
	case "3":
		newMode = "git-manual"
		fmt.Println("\n✅ Git manual deployment selected")
	case "4":
		newMode = "direct-serverbuild"
		fmt.Println("\n✅ Direct server build mode selected")
	case "5":
		newMode = "direct-localbuild"
		fmt.Println("\n✅ Direct local build mode selected")
	default:
		fmt.Println("Invalid selection. Mode not changed.")
		return
	}

	var gitBranch string
	if strings.HasPrefix(newMode, "git") {
		branch, err := e.promptGitBranch(reader)
		if err != nil {
			fmt.Printf("\n❌ %v\n", err)
			return
		}
		gitBranch = branch
	}

	// Update project metadata
	meta.DeploymentMode = newMode
	meta.GitBranch = gitBranch
	meta.Initialized = false // Reset to false when mode changes
	if err := config.SaveProjectMetadata(e.Env, meta); err != nil {
		fmt.Printf("Error: Could not save project metadata: %v\n", err)
		return
	}

	// Regenerate compose file with new mode
	fmt.Println("\n🔄 Regenerating graft-compose.yml with new deployment mode...")

	// Load existing compose to get project name and domain
	p, err := deploy.LoadProject(e.Env, "graft-compose.yml")
	if err != nil {
		fmt.Printf("Warning: Could not load existing compose file: %v\n", err)
		fmt.Println("You may need to manually update graft-compose.yml labels.")
	} else {
		// Update deployment mode and save
		p.DeploymentMode = newMode
		if err := p.Save("."); err != nil {
			fmt.Printf("Error: Could not save compose file: %v\n", err)
			return
		}
	}

	fmt.Printf("\n✅ Deployment mode changed to: %s\n", newMode)
	fmt.Println("📝 Updated files:")
	fmt.Println("   - .graft/project.json")
	fmt.Println("   - graft-compose.yml")

	if newMode == "git-images" || newMode == "git-repo-serverbuild" {
		fmt.Println("\n💡 Don't forget to set up GitHub Actions workflow!")
		fmt.Println("   See: examples/github-actions-workflow.yml")
	}
}

func (e *Executor) RunInit(args []string) {
	reader := bufio.NewReader(os.Stdin)

	// Parse flags
	var force bool
	var input string
	for _, arg := range args {
		if arg == "-f" || arg == "--force" {
			force = true
		}
	}

	// Directory Check
	configPath := filepath.Join(".graft", "config.json")
	projectPath := filepath.Join(".graft", "project.json")
	if _, err := os.Stat(configPath); err == nil {
		if _, err := os.Stat(projectPath); err == nil {
			fmt.Print("\n⚠️  This directory is already initialized with Graft. Do you want to proceed? (y/n): ")
			input, _ := reader.ReadString('\n')
			input = strings.ToLower(strings.TrimSpace(input))
			if input != "y" && input != "yes" {
				fmt.Println("❌ Init aborted.")
				return
			}
			fmt.Println("✅ Proceeding with re-initialization...")
		}
	}

	// Load global registry
	gCfg, _ := config.LoadGlobalConfig()

	var host, user, keyPath string
	var port int
	var registryName string
	var gitBranch string

	if gCfg != nil && len(gCfg.Servers) > 0 {
		fmt.Println("\n📋 Available servers in registry:")
		var keys []string
		i := 1
		for name, srv := range gCfg.Servers {
			fmt.Printf("  [%d] %s (%s)\n", i, name, srv.Host)
			keys = append(keys, name)
			i++
		}
		fmt.Printf("\nSelect a server [1-%d] or type '/new' for a new connection: ", len(keys))

		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "/new" {
			host, port, user, keyPath = promptNewServer(reader)
			fmt.Print("Registry Name (e.g. prod-us): ")
			registryName, _ = reader.ReadString('\n')
			registryName = strings.TrimSpace(registryName)
		} else {
			idx, err := strconv.Atoi(input)
			if err == nil && idx > 0 && idx <= len(keys) {
				selected := gCfg.Servers[keys[idx-1]]
				host = selected.Host
				user = selected.User
				port = selected.Port
				keyPath = selected.KeyPath
				registryName = selected.RegistryName
				fmt.Printf("✅ Using server: %s\n", registryName)
			} else {
				fmt.Println("Invalid selection, entering new server details...")
				host, port, user, keyPath = promptNewServer(reader)
				fmt.Print("Registry Name (e.g. prod-us): ")
				registryName, _ = reader.ReadString('\n')
				registryName = strings.TrimSpace(registryName)
			}
		}
	} else {
		fmt.Println("No servers found in registry. Enter new server details:")
		host, port, user, keyPath = promptNewServer(reader)
		fmt.Print("Registry Name (e.g. prod-us): ")
		registryName, _ = reader.ReadString('\n')
		registryName = strings.TrimSpace(registryName)
	}

	// Update global registry with the selected/new server immediately
	if gCfg != nil {
		if gCfg.Servers == nil {
			gCfg.Servers = make(map[string]config.ServerConfig)
		}
		srv := gCfg.Servers[registryName]
		srv.RegistryName = registryName
		srv.Host = host
		srv.Port = port
		srv.User = user
		srv.KeyPath = keyPath
		gCfg.Servers[registryName] = srv
		config.SaveGlobalConfig(gCfg)
	}

	var projName string
	for {
		fmt.Print("Project Name: ")
		input, _ := reader.ReadString('\n')
		projName = config.NormalizeProjectName(input)

		if projName == "" {
			fmt.Println("❌ Project name cannot be empty and must contain alphanumeric characters")
			continue
		}

		if config.IsValidProjectName(projName) {
			if projName != strings.TrimSpace(strings.ToLower(input)) {
				fmt.Printf("📝 Normalized project name to: %s\n", projName)
			}
			break
		}
		fmt.Println("❌ Invalid project name. Use only letters, numbers, and underscores.")
	}

	var currentHookURL string
	if gCfg != nil {
		if srv, exists := gCfg.Servers[registryName]; exists {
			currentHookURL = srv.GraftHookURL
		}
	}

	// Local Conflict Check
	if gCfg != nil && gCfg.Projects != nil {
		if existingLocalPath, exists := gCfg.Projects[projName]; exists && !force {
			fmt.Printf("\n⚠️  Project '%s' already exists in your local registry:\n", projName)
			fmt.Printf("   Path: %s\n", existingLocalPath)

			// Try to get registry info from existing local path
			localMetaPath := filepath.Join(existingLocalPath, ".graft", "project.json")
			if data, err := os.ReadFile(localMetaPath); err == nil {
				var projectEnv config.ProjectEnv
				if err := json.Unmarshal(data, &projectEnv); err == nil {
					if prodMeta, exists := projectEnv.Env["prod"]; exists && prodMeta.Registry != "" {
						fmt.Printf("   Target Registry: %s\n", prodMeta.Registry)
					}
				}
			}

			fmt.Print("\nDo you want to overwrite this local registration? (y/n): ")
			confirm, _ := reader.ReadString('\n')
			confirm = strings.ToLower(strings.TrimSpace(confirm))
			if confirm != "y" && confirm != "yes" {
				fmt.Println("❌ Init aborted.")
				return
			}
			fmt.Println("✅ Local overwrite confirmed.")
		}
	}

	projFull := projName
	if !strings.HasSuffix(projFull, "-"+e.Env) {
		projFull = fmt.Sprintf("%s-%s", projFull, e.Env)
	}

	var versionToKeep int
	var remoteProjects map[string]interface{}

	// Remote Conflict Check
	fmt.Printf("🔍 Checking for conflicts on remote server '%s'...\n", host)
	client, err := ssh.NewClient(host, port, user, keyPath)
	if err != nil {
		fmt.Printf("⚠️  Warning: Could not connect to host to check for conflicts: %v\n", err)
	} else {
		defer client.Close()

		// Host Initialization Check
		if err := client.RunCommand("ls -d /opt/graft", nil, nil); err != nil {
			fmt.Print("\n⚠️  Host is not initialized. Do you want to initialize the host? (y/n): ")
			input, _ := reader.ReadString('\n')
			input = strings.ToLower(strings.TrimSpace(input))
			if input == "y" || input == "yes" {
				fmt.Println("🚀 Starting host initialization...")
				// We call a slim version of host init or prompt for infra
				setupPostgres := false
				setupRedis := false
				fmt.Print("  Setup shared Postgres? (y/n): ")
				input, _ = reader.ReadString('\n')
				setupPostgres = strings.ToLower(strings.TrimSpace(input)) == "y"
				fmt.Print("  Setup shared Redis? (y/n): ")
				input, _ = reader.ReadString('\n')
				setupRedis = strings.ToLower(strings.TrimSpace(input)) == "y"

				pgUser := strings.ToLower("graft_admin_" + config.GenerateRandomString(4))
				pgPass := config.GenerateRandomString(24)
				pgDB := strings.ToLower("graft_master_" + config.GenerateRandomString(4))

				if err := hostinit.InitHost(client, setupPostgres, setupRedis, false, false, pgUser, pgPass, pgDB, os.Stdout, os.Stderr); err != nil {
					fmt.Printf("❌ Host initialization failed: %v\n", err)
					return
				}
				fmt.Println("✅ Host initialized.")
			} else {
				fmt.Println("⏭️  Skipping host initialization. Some features may not work.")
			}
		}

		// Ensure config dir exists
		client.RunCommand("sudo mkdir -p /opt/graft/config && sudo chown $USER:$USER /opt/graft/config", os.Stdout, os.Stderr)

		tmpFile := filepath.Join(os.TempDir(), "remote_projects.json")
		remoteProjects = make(map[string]interface{})

		if err := client.DownloadFile(config.RemoteProjectsPath, tmpFile); err == nil {
			data, _ := os.ReadFile(tmpFile)
			json.Unmarshal(data, &remoteProjects)
			os.Remove(tmpFile)
		}

		if entry, exists := remoteProjects[projFull]; exists && !force {
			var path string
			if s, ok := entry.(string); ok {
				path = s
			} else if m, ok := entry.(map[string]interface{}); ok {
				path, _ = m["path"].(string)
			}
			fmt.Printf("❌ Conflict: Project '%s' already exists on this server at '%s'.\n", projFull, path)
			fmt.Println("👉 Use 'graft init -f' or '--force' to overwrite this registration.")
			return
		}

		// Update remote registry (local record for now, will upload after boilerplate generation)
		remoteProjects[projFull] = map[string]interface{}{
			"path": fmt.Sprintf("/opt/graft/projects/%s", projFull),
		}
	}

	fmt.Print("Domain (e.g. app.example.com): ")
	domain, _ := reader.ReadString('\n')
	domain = strings.TrimSpace(domain)

	// Rollback configurations
	fmt.Println("\n🔄 Rollback configurations")
	fmt.Print("Do you want to setup rollback configurations? (y/N): ")
	input, _ = reader.ReadString('\n')
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "y" || input == "yes" {
		fmt.Print("Enter the number of versions to keep: ")
		rollInput, _ := reader.ReadString('\n')
		versionToKeep, _ = strconv.Atoi(strings.TrimSpace(rollInput))
		if versionToKeep > 0 {
			fmt.Printf("✅ Rollback configured to keep %d versions\n", versionToKeep)
			// Update remote entry if available
			if remoteProjects != nil {
				if entry, ok := remoteProjects[projName].(map[string]interface{}); ok {
					entry["rollback_backups"] = versionToKeep
					remoteProjects[projName] = entry
				}
			}
		}
	} else {
		fmt.Println("⏭️  Skipping rollback configurations")
	}
	// Deployment mode selection
	var deploymentMode string
	for {
		fmt.Println("\n📦 Project Type / Deployment Mode:")
		fmt.Println("  Git-based modes:")
		fmt.Println("    [1] git-images (GitHub Actions → GHCR → automated deployment via graft-hook)")
		fmt.Println("    [2] git-repo-serverbuild (GitHub Actions → server build → automated deployment)")
		fmt.Println("    [3] git-manual (Git repo only, no CI/CD workflow provided)")
		fmt.Println("\n  Direct deployment modes:")
		fmt.Println("    [4] direct-serverbuild (upload source → build on server)")
		fmt.Print("\nSelect deployment mode [1-4]: ")

		modeInput, _ := reader.ReadString('\n')
		modeInput = strings.TrimSpace(modeInput)

		switch modeInput {
		case "1":
			deploymentMode = "git-images"
		case "2":
			deploymentMode = "git-repo-serverbuild"
		case "3":
			deploymentMode = "git-manual"
		case "4":
			deploymentMode = "direct-serverbuild"

		default:
			fmt.Println("Invalid selection, defaulting to direct-serverbuild")
			deploymentMode = "direct-serverbuild"
		}

		// Git validation for git modes
		if strings.HasPrefix(deploymentMode, "git") {
			branch, err := e.promptGitBranch(reader)
			if err != nil {
				fmt.Printf("\n❌ %v\n", err)
				continue
			}
			gitBranch = branch
			fmt.Printf("✅ Selected branch: %s\n", gitBranch)
		}
		break
	}

	switch deploymentMode {
	case "git-images":
		fmt.Println("\n✅ Git-based image deployment selected (GHCR)")
		fmt.Println("\n📦 This mode uses GitHub Actions to build images and push to GHCR.")
		fmt.Println("\n⚠️  IMPORTANT: Requires graft-hook webhook service for automated deployment")
	case "git-repo-serverbuild":
		fmt.Println("\n✅ Git-based server build deployment selected")
		fmt.Println("\n📦 This mode uses GitHub Actions to trigger server-side builds.")
	case "git-manual":
		fmt.Println("\n✅ Git manual deployment selected")
		fmt.Println("\n📦 This mode sets up the server for Git-based deployment without CI/CD.")
	case "direct-serverbuild":
		fmt.Println("\n✅ Direct server build mode selected")
	case "direct-localbuild":
		fmt.Println("\n✅ Direct local build mode selected")
	}

	// Update remote registry before hook detection/restart to ensure new config is loaded
	if client != nil && remoteProjects != nil {
		data, _ := json.MarshalIndent(remoteProjects, "", "  ")
		tmpPath := filepath.Join(os.TempDir(), "upload_projects.json")
		os.WriteFile(tmpPath, data, 0644)
		client.UploadFile(tmpPath, config.RemoteProjectsPath)
		os.Remove(tmpPath)
		fmt.Println("✅ Remote project registry updated")
	}

	// Graft-Hook detection and deployment for automated modes
	if deploymentMode == "git-images" || deploymentMode == "git-repo-serverbuild" || deploymentMode == "git-manual" {
		if client != nil {
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
						if srv, exists := gCfg.Servers[registryName]; exists {
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
									gCfg.Servers[registryName] = srv
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
				if srv, exists := gCfg.Servers[registryName]; exists {
					srv.GraftHookURL = currentHookURL
					gCfg.Servers[registryName] = srv
					if err := config.SaveGlobalConfig(gCfg); err != nil {
						fmt.Printf("⚠️  Warning: Could not save hook URL to global registry: %v\n", err)
					} else {
						fmt.Println("✅ Hook URL saved to global registry")
					}
				} else {
					fmt.Printf("⚠️  Warning: Server '%s' not found in global registry, cannot save hook URL\n", registryName)
				}
			}
		}
	}

	// Remote project directory setup
	remoteProjFull := projName
	if !strings.HasSuffix(remoteProjFull, "-"+e.Env) {
		remoteProjFull = fmt.Sprintf("%s-%s", remoteProjFull, e.Env)
	}
	remoteProjPath := fmt.Sprintf("/opt/graft/projects/%s", remoteProjFull)
	if client != nil {
		fmt.Printf("📂 Setting up remote project directory: %s\n", remoteProjPath)
		client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown $USER:$USER %s", remoteProjPath, remoteProjPath), nil, nil)

		if strings.HasPrefix(deploymentMode, "git") {
			// Ensure git is installed
			if err := client.RunCommand("git --version", nil, nil); err != nil {
				fmt.Println("📦 Installing git on remote server...")
				client.RunCommand("sudo yum install -y git || sudo apt-get install -y git", os.Stdout, os.Stderr)
			}

			// Get remote origin
			cmd := exec.Command("git", "remote", "get-url", "origin")
			out, _ := cmd.Output()
			gitRemote := strings.TrimSpace(string(out))

			// Init git repo on server
			fmt.Println("🔧 Initializing git repository on server...")
			client.RunCommand(fmt.Sprintf("cd %s && git init && git remote add origin %s", remoteProjPath, gitRemote), os.Stdout, os.Stderr)
		}
	}

	// Generate boilerplate
	p := deploy.GenerateBoilerplate(projName, domain, deploymentMode)
	p.Save(".")

	// Save project metadata
	meta := &config.ProjectMetadata{
		Name:            projName,
		RemotePath:      remoteProjPath,
		Domain:          domain,
		DeploymentMode:  deploymentMode,
		GitBranch:       gitBranch,
		GraftHookURL:    currentHookURL,
		RollbackBackups: versionToKeep,
		Registry:        registryName,
	}
	if err := config.SaveProjectMetadata(e.Env, meta); err != nil {
		fmt.Printf("Warning: Could not save project metadata: %v\n", err)
	}

	fmt.Printf("\n✨ Project '%s' initialized!\n", projName)
	fmt.Printf("Local config: .graft/config.json\n")
	fmt.Printf("Boilerplate: graft-compose.yml\n")
}

func (e *Executor) RunSync(args []string) {
	// Check if a specific service is specified
	var serviceName string
	var noCache bool
	var heave bool
	var useGit bool
	var gitBranch string
	var gitCommit string

	// Parse arguments: [service] [--no-cache] [-h|--heave] [--git] [--branch <name>] [--commit <hash>]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--no-cache" {
			noCache = true
		} else if arg == "-h" || arg == "--heave" {
			heave = true
		} else if arg == "--git" {
			useGit = true
		} else if arg == "--branch" && i+1 < len(args) {
			gitBranch = args[i+1]
			i++ // Skip next arg
		} else if arg == "--commit" && i+1 < len(args) {
			gitCommit = args[i+1]
			i++ // Skip next arg
		} else if serviceName == "" {
			serviceName = arg
		}
	}

	cfg := e

	// Find project file
	localFile := "graft-compose.yml"
	if _, err := os.Stat(localFile); err != nil {
		fmt.Println("Error: graft-compose.yml not found. Run 'graft init' first.")
		return
	}

	p, err := deploy.LoadProject(e.Env, localFile)
	if err != nil {
		fmt.Printf("Error loading project: %v\n", err)
		return
	}

	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Warning: Could not load project metadata. Run 'graft init' first.")
	} else {
		p.DeploymentMode = meta.DeploymentMode
		p.RollbackBackups = meta.RollbackBackups
	}

	reader := bufio.NewReader(os.Stdin)

	// New Initialization flow for Git modes
	if !meta.Initialized && strings.HasPrefix(meta.DeploymentMode, "git") {
		fmt.Println("\n📦 Git-based project detected. Setting up CI/CD workflows...")

		remoteURL, err := git.GetRemoteURL(".", "origin")
		if err != nil {
			fmt.Printf("Error: Could not get git remote URL: %v\n", err)
			return
		}

		// Generate Workflows
		if err := deploy.GenerateWorkflows(p, e.Env, remoteURL, meta.DeploymentMode, meta.GraftHookURL); err != nil {
			fmt.Printf("Error generating workflows: %v\n", err)
			return
		}

		fmt.Println("✅ GitHub Workflows created in .github/workflows/")

		// Ask for compose generation and transfer

		client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		defer client.Close()

		fmt.Println("📤 Transferring files to server...")
		if err := deploy.SyncComposeOnly(e.Env, client, p, true, os.Stdout, os.Stderr, true, true); err != nil {
			fmt.Printf("Error syncing compose: %v\n", err)
		}

		// Update initialized status
		meta.Initialized = true
		config.SaveProjectMetadata(e.Env, meta)

		fmt.Println("\n✅ Project initialized! Next steps:")
		fmt.Println("1. Review .github/workflows/ci.yml and deploy.yml")
		if meta.DeploymentMode == "git-images" {
			fmt.Println("2. Your server is set up to receive images from GHCR.")
		}
		fmt.Println("3. Run: git add . && git commit -m \"Initial Graft setup\" && git push")
		fmt.Println("\n🚀 Your project is ready to be updated with git push!")
		return

	} else if meta.Initialized {
		fmt.Printf("\n⚠️  Project '%s' is already initialized.\n", p.Name)

		fmt.Print("❓ Do you want to re-generate and transfer the compose file? (y/n): ")
		ansCompose, _ := reader.ReadString('\n')

		fmt.Print("❓ Do you want to transfer the environment files (env/)? (y/n): ")
		ansEnv, _ := reader.ReadString('\n')

		doCompose := strings.TrimSpace(strings.ToLower(ansCompose)) == "y"
		doEnv := strings.TrimSpace(strings.ToLower(ansEnv)) == "y"

		if doCompose || doEnv {
			client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				return
			}
			defer client.Close()

			if err := deploy.SyncComposeOnly(e.Env, client, p, true, os.Stdout, os.Stderr, doCompose, doEnv); err != nil {
				fmt.Printf("Error during sync: %v\n", err)
				return
			}

			fmt.Println("\n✅ Completed! Please do git push to update/restart the server through CI/CD or run native docker commands with graft.")
			return
		}
	}

	// For automated git modes, don't allow manual sync as it should be done via git push
	if meta.Initialized && (meta.DeploymentMode == "git-images" || meta.DeploymentMode == "git-repo-serverbuild") {
		fmt.Printf("\nℹ️  Project '%s' is in Git-automated mode (%s).\n", p.Name, meta.DeploymentMode)
		fmt.Println("🚀 Please do 'git push' to trigger deployment via GitHub Actions and webhooks.")
		fmt.Println("💡 To force a manual sync (upload source), use a different deployment mode with 'graft mode'.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if serviceName != "" {
		fmt.Printf("🎯 Syncing service: %s\n", serviceName)
		if useGit {
			fmt.Println("📦 Git mode enabled")
		}
		if noCache {
			fmt.Println("🔥 No-cache mode enabled")
		}
		if heave {
			fmt.Println("📦 Heave sync enabled (upload only)")
		}
		err = deploy.SyncService(e.Env, client, p, serviceName, noCache, heave, useGit, gitBranch, gitCommit, os.Stdout, os.Stderr)
	} else {
		if useGit {
			fmt.Println("📦 Git mode enabled")
		}
		if noCache {
			fmt.Println("🔥 No-cache mode enabled")
		}
		if heave {
			fmt.Println("🚀 Heave sync enabled (upload only)")
		}
		err = deploy.Sync(e.Env, client, p, noCache, heave, useGit, gitBranch, gitCommit, os.Stdout, os.Stderr)
	}

	if err != nil {
		fmt.Printf("Error during sync: %v\n", err)
		return
	}

	if !heave {
		fmt.Println("\n✅ Sync complete!")
	}
}

func (e *Executor) RunSyncCompose(args []string) {
	var heave bool
	// Parse arguments: compose [-h|--heave]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--heave" {
			heave = true
		}
	}

	cfg := e

	// Find project file
	localFile := "graft-compose.yml"
	if _, err := os.Stat(localFile); err != nil {
		fmt.Println("Error: graft-compose.yml not found. Run 'graft init' first.")
		return
	}

	p, err := deploy.LoadProject(e.Env, localFile)
	if err != nil {
		fmt.Printf("Error loading project: %v\n", err)
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if heave {
		fmt.Println("📄 Heave sync enabled (config upload only)")
	}

	err = deploy.SyncComposeOnly(e.Env, client, p, heave, os.Stdout, os.Stderr, true, true)
	if err != nil {
		fmt.Printf("Error during sync: %v\n", err)
		return
	}

	if !heave {
		fmt.Println("\n✅ Compose sync complete!")
	}
}

func (e *Executor) RunLogs(serviceName string) {
	cfg := e

	// Load project metadata to get remote path
	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Printf("📋 Streaming logs for service: %s\n", serviceName)
	fmt.Println("Press Ctrl+C to stop")
	fmt.Println("---")

	// Run docker compose logs with follow flag
	logsCmd := fmt.Sprintf("cd %s && sudo docker compose logs -f --tail=100 %s", meta.RemotePath, serviceName)
	if err := client.RunCommand(logsCmd, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("\nError: %v\n", err)
	}
}

func (e *Executor) RunDockerCompose(args []string) {
	cfg := e

	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// Build the docker compose command
	cmdStr := strings.Join(args, " ")
	composeCmd := fmt.Sprintf("cd %s && sudo docker compose %s", meta.RemotePath, cmdStr)

	if err := client.RunCommand(composeCmd, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("\nError: %v\n", err)
	}
}

func (e *Executor) RunHook(args []string) {
	cfg := e

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// Build the docker compose command
	cmdStr := strings.Join(args, " ")
	composeCmd := fmt.Sprintf("cd %s && sudo docker compose %s", "/opt/graft/webhook/", cmdStr)

	if err := client.RunCommand(composeCmd, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("\nError: %v\n", err)
	}
}

func (e *Executor) RunPull(registryName, projectName string) {
	gCfg, err := config.LoadGlobalConfig()
	if err != nil || gCfg == nil {
		fmt.Println("Error loading global registry.")
		return
	}

	srv, exists := gCfg.Servers[registryName]
	if !exists {
		fmt.Printf("Error: Registry '%s' not found.\n", registryName)
		return
	}

	fmt.Printf("\n📥 Pulling project '%s' from '%s'...\n", projectName, registryName)
	client, err := ssh.NewClient(srv.Host, srv.Port, srv.User, srv.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	tmpFile := filepath.Join(os.TempDir(), "remote_projects_pull.json")
	if err := client.DownloadFile(config.RemoteProjectsPath, tmpFile); err != nil {
		fmt.Println("Error: Could not retrieve remote project registry.")
		return
	}
	defer os.Remove(tmpFile)

	data, _ := os.ReadFile(tmpFile)
	var remoteProjects map[string]interface{}
	json.Unmarshal(data, &remoteProjects)

	fullProjectName := projectName
	var remotePath string
	
	entry, exists := remoteProjects[fullProjectName]
	if !exists {
		fullProjectName = projectName + "-" + registryName
		entry, exists = remoteProjects[fullProjectName]
	}

	if exists {
		if s, ok := entry.(string); ok {
			remotePath = s
		} else if m, ok := entry.(map[string]interface{}); ok {
			remotePath, _ = m["path"].(string)
		}
	}

	if remotePath == "" {
		fmt.Printf("Error: Project '%s' not found on remote server.\n", projectName)
		return
	}

	projectName = fullProjectName // Use the full name for local directory too if found as full name


	home, _ := os.UserHomeDir()
	localBase := filepath.Join(home, "graft", projectName)
	if err := os.MkdirAll(localBase, 0755); err != nil {
		fmt.Printf("Error: Could not create local directory: %v\n", err)
		return
	}

	fmt.Printf("🚀 Syncing files to %s...\n", localBase)
	if err := client.PullRsync(remotePath, localBase, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("Error during pull: %v\n", err)
		return
	}

	fmt.Println("🔧 Re-initializing local configuration...")
	os.MkdirAll(filepath.Join(localBase, ".graft"), 0755)

	meta := &config.ProjectMetadata{
		Name:       projectName,
		RemotePath: remotePath,
		Registry:   registryName,
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(localBase, ".graft", "project.json"), metaData, 0644)

	absPath, _ := filepath.Abs(localBase)
	if gCfg.Projects == nil {
		gCfg.Projects = make(map[string]string)
	}
	gCfg.Projects[projectName] = absPath
	config.SaveGlobalConfig(gCfg)

	fmt.Printf("\n✨ Project '%s' pulled successfully to %s\n", projectName, localBase)
	fmt.Printf("👉 Use 'graft -p %s <command>' to manage it.\n", projectName)
}

func (e *Executor) promptGitBranch(reader *bufio.Reader) (string, error) {
	// Check for git repository
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		return "", fmt.Errorf("no .git directory found in project root. Git modes require a git repository")
	}

	// Get remote origin
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no git remote 'origin' found. Git modes require a remote 'origin' for deployment")
	}
	gitRemote := strings.TrimSpace(string(out))
	fmt.Printf("✅ Found git remote: %s\n", gitRemote)

	// Get all branches
	branchCmd := exec.Command("git", "branch", "-a", "--format=%(refname:short)")
	branchOut, err := branchCmd.Output()
	if err != nil {
		fmt.Printf("⚠️  Warning: Could not fetch git branches: %v. Defaulting to 'main'\n", err)
		return "main", nil
	}

	lines := strings.Split(string(branchOut), "\n")
	var branches []string
	seen := make(map[string]bool)
	for _, line := range lines {
		branch := strings.TrimSpace(line)
		if branch == "" || strings.Contains(branch, "HEAD") {
			continue
		}
		// Clean up remote prefixes
		cleanBranch := branch
		if strings.HasPrefix(cleanBranch, "remotes/origin/") {
			cleanBranch = strings.TrimPrefix(cleanBranch, "remotes/origin/")
		} else if strings.HasPrefix(cleanBranch, "origin/") {
			cleanBranch = strings.TrimPrefix(cleanBranch, "origin/")
		}

		if !seen[cleanBranch] {
			branches = append(branches, cleanBranch)
			seen[cleanBranch] = true
		}
	}

	if len(branches) == 0 {
		return "main", nil
	}

	fmt.Println("\n🌿 Available branches:")
	for i, b := range branches {
		fmt.Printf("  [%d] %s\n", i+1, b)
	}
	fmt.Printf("\nSelect branch [1-%d] (default: 1): ", len(branches))
	bInput, _ := reader.ReadString('\n')
	bInput = strings.TrimSpace(bInput)
	if bInput == "" {
		return branches[0], nil
	}

	idx, err := strconv.Atoi(bInput)
	if err != nil || idx < 1 || idx > len(branches) {
		fmt.Printf("⚠️  Invalid selection, using default: %s\n", branches[0])
		return branches[0], nil
	}

	return branches[idx-1], nil
}

