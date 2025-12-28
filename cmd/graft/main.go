package main

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
	"github.com/skssmd/graft/internal/infra"
	"github.com/skssmd/graft/internal/ssh"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	args := os.Args[1:]

	// Handle target registry flag: graft -r registryname ...
	var registryContext string
	if args[0] == "-r" || args[0] == "--registry" {
		if len(args) < 2 {
			fmt.Println("Usage: graft -r <registryname> <command>")
			return
		}
		registryContext = args[1]
		args = args[2:]

		// Handle shell directly after -r: graft -r name -sh ...
		if len(args) > 0 && (args[0] == "-sh" || args[0] == "--sh") {
			runRegistryShell(registryContext, args[1:])
			return
		}
	}

	// Handle project context flag: graft -p projectname ...
	if args[0] == "-p" || args[0] == "--project" {
		if len(args) < 3 {
			fmt.Println("Usage: graft -p <projectname> <command>")
			return
		}
		projectName := args[1]
		args = args[2:]

		// Lookup project path
		gCfg, _ := config.LoadGlobalConfig()
		if gCfg == nil || gCfg.Projects == nil || gCfg.Projects[projectName] == "" {
			fmt.Printf("Error: Project '%s' not found in global registry\n", projectName)
			return
		}

		projectPath := gCfg.Projects[projectName]
		if err := os.Chdir(projectPath); err != nil {
			fmt.Printf("Error: Could not enter project directory: %v\n", err)
			return
		}
		fmt.Printf("📂 Context: %s (%s)\n", projectName, projectPath)
	}

	command := args[0]

	switch command {
	case "init":
		runInit(args[1:])
	case "hook":
		runHook(args[1:])
	case "host":
		if len(args) < 2 {
			fmt.Println("Usage: graft host [init|clean|sh|self-destruct]")
			return
		}
		switch args[1] {
		case "init":
			runHostInit()
		case "clean":
			runHostClean()
		case "sh", "-sh", "--sh":
			runHostShell(args[2:])
		case "self-destruct":
			runHostSelfDestruct()
		default:
			fmt.Println("Usage: graft host [init|clean|sh|self-destruct]")
		}
	case "db":
		if len(args) < 3 || args[2] != "init" {
			fmt.Println("Usage: graft db <name> init")
			return
		}
		runInfraInit("postgres", args[1])
	case "redis":
		if len(args) < 3 || args[2] != "init" {
			fmt.Println("Usage: graft redis <name> init")
			return
		}
		runInfraInit("redis", args[1])
	case "infra":
		if len(args) < 2 {
			fmt.Println("Usage: graft infra [db|redis] ports:<value> | graft infra reload")
			return
		}
		if args[1] == "reload" {
			runInfraReload()
		} else {
			runInfra(args[1:])
		}
	case "logs":
		if len(args) < 2 {
			fmt.Println("Usage: graft logs <service>")
			return
		}
		runLogs(args[1])
	case "sync":
		// Check if "compose" subcommand is specified
		if len(args) > 1 && args[1] == "compose" {
			runSyncCompose(args[1:])
		} else {
			runSync(args[1:])
		}
	case "registry":
		if len(args) < 2 {
			fmt.Println("Usage: graft registry [ls|add|del]")
			return
		}
		switch args[1] {
		case "ls":
			runRegistryLs()
		case "add":
			runRegistryAdd()
		case "del":
			if len(args) < 3 {
				fmt.Println("Usage: graft registry del <name>")
				return
			}
			runRegistryDel(args[2])
		default:
			fmt.Println("Usage: graft registry [ls|add|del]")
		}
	case "projects":
		if len(args) > 1 && args[1] == "ls" {
			runProjectsLs(registryContext)
		} else {
			fmt.Println("Usage: graft projects ls")
		}
	case "pullfromhost":
		if registryContext == "" {
			fmt.Println("Error: Pulling requires a registry context. Use 'graft -r <registry> pull <project>'")
			return
		}
		if len(args) < 2 {
			fmt.Println("Usage: graft -r <registry> pullfromhost <project>")
			return
		}
		runPull(registryContext, args[1])
	case "mode":
		runMode()
	default:
		// Handle the --pull flag as requested in the specific format
		// foundPull := false
		// for i, arg := range os.Args {
		// 	if arg == "--pull" && i+1 < len(os.Args) {
		// 		if registryContext == "" {
		// 			fmt.Println("Error: Pulling requires a registry context. Use 'graft -r <registry> --pull <project>'")
		// 			return
		// 		}
		// 		runPull(registryContext, os.Args[i+1])
		// 		foundPull = true
		// 		break
		// 	}
		// }
		// if foundPull { return }

		// Pass through to docker compose for any other command
		runDockerCompose(args)
	}
}

func printUsage() {
	fmt.Println("Graft CLI - Interactive Deployment Tool")
	fmt.Println("\nUsage:")
	fmt.Println("  graft [flags] <command> [args]")
	fmt.Println("\nFlags:")
	fmt.Println("  -p, --project <name>      Run command in specific project context")
	fmt.Println("  -r, --registry <name>     Target a specific server context")
	fmt.Println("  -sh, --sh [cmd]           Execute shell command on target (or start SSH session)")
	fmt.Println("\nCommands:")
	fmt.Println("  init [-f]                 Initialize a new project")
	fmt.Println("  registry [ls|add|del]     Manage registered servers")
	fmt.Println("  projects ls               List local projects")
	fmt.Println("  pull <project>            Pull/Clone project from remote")
	fmt.Println("  host [init|clean|sh|self-destruct]  Manage current project's host context")
	fmt.Println("  infra [db|redis] ports:<v> Change infra port mapping (null to hide)")
	fmt.Println("  infra reload              Pull and reload infrastructure services")
	fmt.Println("  db/redis <name> init      Initialize shared infrastructure")
	fmt.Println("  sync [service] [-h]       Deploy project to server")
	fmt.Println("  logs <service>            Stream service logs")
	fmt.Println("  mode                      Change project deployment mode")
}
func runMode() {
	reader := bufio.NewReader(os.Stdin)
	
	// Load project metadata
	meta, err := config.LoadProjectMetadata()
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

	// Update project metadata
	meta.DeploymentMode = newMode
	meta.Initialized = false // Reset to false when mode changes
	if err := config.SaveProjectMetadata(meta); err != nil {
		fmt.Printf("Error: Could not save project metadata: %v\n", err)
		return
	}

	// Regenerate compose file with new mode
	fmt.Println("\n🔄 Regenerating graft-compose.yml with new deployment mode...")
	
	// Load existing compose to get project name and domain
	p, err := deploy.LoadProject("graft-compose.yml")
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


func runInit(args []string) {
	reader := bufio.NewReader(os.Stdin)

	// Parse flags
	var force bool
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
		gCfg.Servers[registryName] = config.ServerConfig{
			RegistryName: registryName,
			Host:         host,
			Port:         port,
			User:         user,
			KeyPath:      keyPath,
		}
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

	// Local Conflict Check
	if gCfg != nil && gCfg.Projects != nil {
		if existingLocalPath, exists := gCfg.Projects[projName]; exists && !force {
			fmt.Printf("\n⚠️  Project '%s' already exists in your local registry:\n", projName)
			fmt.Printf("   Path: %s\n", existingLocalPath)
			
			// Try to get host info from existing local path
			localCfgPath := filepath.Join(existingLocalPath, ".graft", "config.json")
			if data, err := os.ReadFile(localCfgPath); err == nil {
				var exCfg config.GraftConfig
				if err := json.Unmarshal(data, &exCfg); err == nil {
					fmt.Printf("   Target Host: %s (%s)\n", exCfg.Server.RegistryName, exCfg.Server.Host)
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
		var remoteProjects map[string]string // Name -> Path
		
		if err := client.DownloadFile(config.RemoteProjectsPath, tmpFile); err == nil {
			data, _ := os.ReadFile(tmpFile)
			json.Unmarshal(data, &remoteProjects)
			os.Remove(tmpFile)
		}
		
		if remoteProjects == nil {
			remoteProjects = make(map[string]string)
		}

		if existingPath, exists := remoteProjects[projName]; exists && !force {
			fmt.Printf("❌ Conflict: Project '%s' already exists on this server at '%s'.\n", projName, existingPath)
			fmt.Println("👉 Use 'graft init -f' or '--force' to overwrite this registration.")
			return
		}

		// Update remote registry (local record for now, will upload after boilerplate generation)
		remoteProjects[projName] = fmt.Sprintf("/opt/graft/projects/%s", projName)
		
		// Pre-cache the remote project list for upload later
		defer func() {
			data, _ := json.MarshalIndent(remoteProjects, "", "  ")
			tmpPath := filepath.Join(os.TempDir(), "upload_projects.json")
			os.WriteFile(tmpPath, data, 0644)
			client.UploadFile(tmpPath, config.RemoteProjectsPath)
			os.Remove(tmpPath)
			fmt.Println("✅ Remote project registry updated")
		}()
	}

	fmt.Print("Domain (e.g. app.example.com): ")
	domain, _ := reader.ReadString('\n')
	domain = strings.TrimSpace(domain)

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
		fmt.Println("    [5] direct-localbuild (build locally → upload image)")
		fmt.Print("\nSelect deployment mode [1-5]: ")
		
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
		case "5":
			deploymentMode = "direct-localbuild"
		default:
			fmt.Println("Invalid selection, defaulting to direct-serverbuild")
			deploymentMode = "direct-serverbuild"
		}

		// Git validation for git modes
		if strings.HasPrefix(deploymentMode, "git") {
			// Check for git repository and remote origin
			if _, err := os.Stat(".git"); os.IsNotExist(err) {
				fmt.Println("\n❌ Error: No .git directory found in project root.")
				fmt.Println("   Git modes require a git repository.")
				continue
			}
			
			// Get remote origin
			cmd := exec.Command("git", "remote", "get-url", "origin")
			out, err := cmd.Output()
			if err != nil {
				fmt.Println("\n❌ Error: No git remote 'origin' found.")
				fmt.Println("   Git modes require a remote 'origin' for deployment.")
				continue
			}
			gitRemote := strings.TrimSpace(string(out))
			fmt.Printf("✅ Found git remote: %s\n", gitRemote)
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

	// Graft-Hook detection and deployment for automated modes
	var currentHookURL string
	if deploymentMode == "git-images" || deploymentMode == "git-repo-serverbuild" || deploymentMode == "git-manual" {
		if client != nil {
			installHook := false
			if deploymentMode == "git-manual" {
				fmt.Print("\n❓ Do you want to install graft-hook for CI/CD automation? (y/n): ")
				input, _ := reader.ReadString('\n')
				installHook = strings.ToLower(strings.TrimSpace(input)) == "y"
			} else {
				// Check if already installed
				if err := client.RunCommand("ls /opt/graft/webhook/docker-compose.yml", nil, nil); err != nil {
					fmt.Println("\n🔍 graft-hook is not installed on the server.")
					installHook = true
				} else {
					fmt.Println("\n✅ graft-hook is already installed on the server.")
					// Fetch existing hook URL from global registry if available
					if srv, exists := gCfg.Servers[registryName]; exists {
						currentHookURL = srv.GraftHookURL
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
    image: ghcr.io/skssmd/graft-hook:latest
    environment:
      - configpath=/opt/graft/config/projects.json
      - RUST_LOG=info
    labels:
      - "graft.mode=serverbuild"
      - "traefik.enable=true"
      - "traefik.http.routers.graft-hook.rule=Host(` + "`" + `%s` + "`" + `)"
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
					config.SaveGlobalConfig(gCfg)
				}
			}
		}
	}

	// Remote project directory setup
	if client != nil {
		remoteProjPath := fmt.Sprintf("/opt/graft/projects/%s", projName)
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

	// Save local config
	cfg := &config.GraftConfig{
		Server: config.ServerConfig{
			RegistryName: registryName,
			Host:         host, Port: port, User: user, KeyPath: keyPath,
		},
	}
	config.SaveConfig(cfg, true) // local


	// Generate boilerplate
	p := deploy.GenerateBoilerplate(projName, domain, deploymentMode)
	p.Save(".")

	// Save project metadata
	meta := &config.ProjectMetadata{
		Name:           projName,
		RemotePath:     fmt.Sprintf("/opt/graft/projects/%s", projName),
		DeploymentMode: deploymentMode,
		GraftHookURL:   currentHookURL,
	}
	if err := config.SaveProjectMetadata(meta); err != nil {
		fmt.Printf("Warning: Could not save project metadata: %v\n", err)
	}

	fmt.Printf("\n✨ Project '%s' initialized!\n", projName)
	fmt.Printf("Local config: .graft/config.json\n")
	fmt.Printf("Boilerplate: graft-compose.yml\n")
	if deploymentMode == "git-images" || deploymentMode == "git-repo-serverbuild" {
		fmt.Printf("GitHub Actions Example: examples/github-actions-workflow.yml\n")
	}
}

func runHostInit() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found. Run 'graft init' first.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	reader := bufio.NewReader(os.Stdin)

	// Save or update registry name
	if cfg.Server.RegistryName == "" {
		fmt.Print("Enter a Registry Name for this server (e.g. prod-us): ")
		name, _ := reader.ReadString('\n')
		cfg.Server.RegistryName = strings.TrimSpace(name)
		config.SaveConfig(cfg, true) // Update local
	}

	// Register in global registry
	gCfg, _ := config.LoadGlobalConfig()
	if gCfg != nil {
		if gCfg.Servers == nil { gCfg.Servers = make(map[string]config.ServerConfig) }
		gCfg.Servers[cfg.Server.RegistryName] = cfg.Server
		config.SaveGlobalConfig(gCfg)
	}

	// Ask about shared infrastructure
	fmt.Println("\n🗄️  Shared Infrastructure Setup")
	
	fmt.Print("Setup shared Postgres instance? (y/n): ")
	confirmPG, _ := reader.ReadString('\n')
	confirmPG = strings.ToLower(strings.TrimSpace(confirmPG))
	setupPostgres := confirmPG == "y" || confirmPG == "yes"

	var exposePostgres bool
	if setupPostgres {
		fmt.Print("  Expose Postgres port (5432) to the internet? (y/n): ")
		input, _ := reader.ReadString('\n')
		input = strings.ToLower(strings.TrimSpace(input))
		exposePostgres = input == "y" || input == "yes"
	}

	fmt.Print("Setup shared Redis instance? (y/n): ")
	confirmRedis, _ := reader.ReadString('\n')
	confirmRedis = strings.ToLower(strings.TrimSpace(confirmRedis))
	setupRedis := confirmRedis == "y" || confirmRedis == "yes"

	var exposeRedis bool
	if setupRedis {
		fmt.Print("  Expose Redis port (6379) to the internet? (y/n): ")
		input, _ := reader.ReadString('\n')
		input = strings.ToLower(strings.TrimSpace(input))
		exposeRedis = input == "y" || input == "yes"
	}

	// Secure credentials for infrastructure
	if setupPostgres && cfg.Infra.PostgresPassword == "" {
		// Try to pull existing from remote server first
		fmt.Fprintln(os.Stdout, "🔍 Checking for existing infrastructure credentials on remote server...")
		tmpFile := filepath.Join(os.TempDir(), "host_infra.config")
		if err := client.DownloadFile(config.RemoteInfraPath, tmpFile); err == nil {
			data, _ := os.ReadFile(tmpFile)
			var infraCfg config.InfraConfig
			if err := json.Unmarshal(data, &infraCfg); err == nil {
				cfg.Infra.PostgresUser = infraCfg.PostgresUser
				cfg.Infra.PostgresPassword = infraCfg.PostgresPassword
				cfg.Infra.PostgresDB = infraCfg.PostgresDB
				cfg.Infra.PostgresPort = infraCfg.PostgresPort
				cfg.Infra.RedisPort = infraCfg.RedisPort
				fmt.Fprintln(os.Stdout, "✅ Existing infrastructure config found and loaded from remote server")
			}
			os.Remove(tmpFile)
		} else {
			fmt.Println("🔐 Generating new secure credentials for Postgres...")
			cfg.Infra.PostgresUser = strings.ToLower("graft_admin_" + config.GenerateRandomString(4))
			cfg.Infra.PostgresPassword = config.GenerateRandomString(24)
			cfg.Infra.PostgresDB = strings.ToLower("graft_master_" + config.GenerateRandomString(4))
		}
	}

	err = hostinit.InitHost(client, setupPostgres, setupRedis, exposePostgres, exposeRedis,
		cfg.Infra.PostgresUser, cfg.Infra.PostgresPassword, cfg.Infra.PostgresDB, 
		os.Stdout, os.Stderr)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("\n✅ Host initialized successfully!")
}

func runHostClean() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("🧹 Cleaning Docker caches and unused resources...")
	
	cleanupCmds := []struct{
		name string
		cmd  string
	}{
		{"Stopped containers", "sudo docker container prune -f"},
		{"Dangling images", "sudo docker image prune -f"},
		{"Build cache", "sudo docker builder prune -f"},
		{"Unused volumes", "sudo docker volume prune -f"},
		{"Unused networks", "sudo docker network prune -f"},
	}

	for _, cleanup := range cleanupCmds {
		fmt.Printf("  Cleaning %s...\n", cleanup.name)
		if err := client.RunCommand(cleanup.cmd, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("  ⚠️  Warning: %v\n", err)
		}
	}

	fmt.Println("\n✅ Cleanup complete!")
}

func runInfraInit(typ, name string) {
	name = config.NormalizeProjectName(name)
	if name == "" {
		fmt.Printf("Error: Invalid %s name. Use only letters, numbers, and underscores.\n", typ)
		return
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	var url string
	if typ == "postgres" {
		url, err = infra.InitPostgres(client, name, cfg, os.Stdout, os.Stderr)
	} else {
		url, err = infra.InitRedis(client, name, os.Stdout, os.Stderr)
	}

	if err != nil {
		fmt.Printf("Error initializing %s: %v\n", typ, err)
		return
	}

	secretKey := fmt.Sprintf("GRAFT_%s_%s_URL", strings.ToUpper(typ), strings.ToUpper(name))
	if err := config.SaveSecret(secretKey, url); err != nil {
		fmt.Printf("Warning: Could not save secret locally: %v\n", err)
	}

	fmt.Printf("\n✅ %s '%s' initialized!\n", typ, name)
	fmt.Printf("Secret saved: %s\n", secretKey)
	fmt.Printf("Connection URL: %s\n", url)
}

func runSync(args []string) {
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

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	// Find project file
	localFile := "graft-compose.yml"
	if _, err := os.Stat(localFile); err != nil {
		fmt.Println("Error: graft-compose.yml not found. Run 'graft init' first.")
		return
	}

	p, err := deploy.LoadProject(localFile)
	if err != nil {
		fmt.Printf("Error loading project: %v\n", err)
		return
	}

	meta, err := config.LoadProjectMetadata()
	if err != nil {
		fmt.Println("Warning: Could not load project metadata. Run 'graft init' first.")
	} else {
		p.DeploymentMode = meta.DeploymentMode
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
		if err := deploy.GenerateWorkflows(p, remoteURL, meta.DeploymentMode, meta.GraftHookURL); err != nil {
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
			if err := deploy.SyncComposeOnly(client, p, true, os.Stdout, os.Stderr,true, true); err != nil {
				fmt.Printf("Error syncing compose: %v\n", err)
			}
			
			// Update initialized status
			meta.Initialized = true
			config.SaveProjectMetadata(meta)

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

			if err := deploy.SyncComposeOnly(client, p, true, os.Stdout, os.Stderr ,doCompose, doEnv ); err != nil {
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
		err = deploy.SyncService(client, p, serviceName, noCache, heave, useGit, gitBranch, gitCommit, os.Stdout, os.Stderr)
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
		err = deploy.Sync(client, p, noCache, heave, useGit, gitBranch, gitCommit, os.Stdout, os.Stderr)
	}

	if err != nil {
		fmt.Printf("Error during sync: %v\n", err)
		return
	}

	if !heave {
		fmt.Println("\n✅ Sync complete!")
	}
}

func runSyncCompose(args []string) {
	var heave bool
	// Parse arguments: compose [-h|--heave]
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--heave" {
			heave = true
		}
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	// Find project file
	localFile := "graft-compose.yml"
	if _, err := os.Stat(localFile); err != nil {
		fmt.Println("Error: graft-compose.yml not found. Run 'graft init' first.")
		return
	}

	p, err := deploy.LoadProject(localFile)
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

	err = deploy.SyncComposeOnly(client, p, heave, os.Stdout, os.Stderr,true, true)
	if err != nil {
		fmt.Printf("Error during sync: %v\n", err)
		return
	}

	if !heave {
		fmt.Println("\n✅ Compose sync complete!")
	}
}

func runLogs(serviceName string) {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	// Load project metadata to get remote path
	meta, err := config.LoadProjectMetadata()
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

func runDockerCompose(args []string) {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	meta, err := config.LoadProjectMetadata()
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

func runHook(args []string) {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
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
	composeCmd := fmt.Sprintf("cd %s && sudo docker compose %s", "/opt/graft/webhook/", cmdStr)
	
	if err := client.RunCommand(composeCmd, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("\nError: %v\n", err)
	}
}
func promptNewServer(reader *bufio.Reader) (string, int, string, string) {
	fmt.Print("Host IP: ")
	host, _ := reader.ReadString('\n')
	host = strings.TrimSpace(host)

	fmt.Print("Port (22): ")
	portStr, _ := reader.ReadString('\n')
	port, _ := strconv.Atoi(strings.TrimSpace(portStr))
	if port == 0 { port = 22 }

	fmt.Print("User: ")
	user, _ := reader.ReadString('\n')
	user = strings.TrimSpace(user)

	fmt.Print("Key Path: ")
	keyPath, _ := reader.ReadString('\n')
	keyPath = strings.TrimSpace(keyPath)

	return host, port, user, keyPath
}

func runRegistryLs() {
	gCfg, err := config.LoadGlobalConfig()
	if err != nil || gCfg == nil || len(gCfg.Servers) == 0 {
		fmt.Println("No servers found in global registry.")
		return
	}

	fmt.Println("\n📋 Registered Servers:")
	fmt.Printf("%-15s %-20s %-10s %-10s\n", "Name", "Host", "User", "Port")
	fmt.Println(strings.Repeat("-", 60))
	for name, srv := range gCfg.Servers {
		fmt.Printf("%-15s %-20s %-10s %-10d\n", name, srv.Host, srv.User, srv.Port)
	}
	fmt.Println()
}

func runProjectsLs(registryName string) {
	gCfg, err := config.LoadGlobalConfig()
	if err != nil || gCfg == nil {
		fmt.Println("Error loading global registry.")
		return
	}

	if registryName != "" {
		// Remote listing
		srv, exists := gCfg.Servers[registryName]
		if !exists {
			fmt.Printf("Error: Registry '%s' not found.\n", registryName)
			return
		}

		fmt.Printf("\n🔍 Fetching projects from remote server '%s' (%s)...\n", registryName, srv.Host)
		client, err := ssh.NewClient(srv.Host, srv.Port, srv.User, srv.KeyPath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		defer client.Close()

		tmpFile := filepath.Join(os.TempDir(), "remote_projects_ls.json")
		if err := client.DownloadFile(config.RemoteProjectsPath, tmpFile); err != nil {
			fmt.Println("No projects found on remote server or registry file missing.")
			return
		}
		defer os.Remove(tmpFile)

		data, _ := os.ReadFile(tmpFile)
		var remoteProjects map[string]string // Name -> Path
		json.Unmarshal(data, &remoteProjects)

		if len(remoteProjects) == 0 {
			fmt.Println("No projects registered on this server.")
			return
		}

		fmt.Printf("\n📂 Remote Projects on '%s':\n", registryName)
		fmt.Printf("%-20s %-40s\n", "Name", "Remote Path")
		fmt.Println(strings.Repeat("-", 65))
		for name, path := range remoteProjects {
			fmt.Printf("%-20s %-40s\n", name, path)
		}
		fmt.Println()
	} else {
		// Local listing
		if len(gCfg.Projects) == 0 {
			fmt.Println("No local projects found in registry.")
			return
		}

		fmt.Println("\n📂 Local Projects:")
		fmt.Printf("%-20s %-15s %-40s\n", "Name", "Server", "Local Path")
		fmt.Println(strings.Repeat("-", 80))
		for name, path := range gCfg.Projects {
			serverName := "unknown"
			localCfgPath := filepath.Join(path, ".graft", "config.json")
			if data, err := os.ReadFile(localCfgPath); err == nil {
				var lCfg config.GraftConfig
				if err := json.Unmarshal(data, &lCfg); err == nil {
					serverName = lCfg.Server.RegistryName
				}
			}
			fmt.Printf("%-20s %-15s %-40s\n", name, serverName, path)
		}
		fmt.Println()
	}
}

func runPull(registryName, projectName string) {
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
	var remoteProjects map[string]string
	json.Unmarshal(data, &remoteProjects)

	remotePath, exists := remoteProjects[projectName]
	if !exists {
		fmt.Printf("Error: Project '%s' not found on remote server.\n", projectName)
		return
	}

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
	cfg := &config.GraftConfig{
		Server: srv,
	}
	
	os.MkdirAll(filepath.Join(localBase, ".graft"), 0755)
	cfgData, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(filepath.Join(localBase, ".graft", "config.json"), cfgData, 0644)

	meta := &config.ProjectMetadata{
		Name: projectName,
		RemotePath: remotePath,
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	os.WriteFile(filepath.Join(localBase, ".graft", "project.json"), metaData, 0644)

	absPath, _ := filepath.Abs(localBase)
	if gCfg.Projects == nil { gCfg.Projects = make(map[string]string) }
	gCfg.Projects[projectName] = absPath
	config.SaveGlobalConfig(gCfg)

	fmt.Printf("\n✨ Project '%s' pulled successfully to %s\n", projectName, localBase)
	fmt.Printf("👉 Use 'graft -p %s <command>' to manage it.\n", projectName)
}

func runRegistryAdd() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\n➕ Add New Server to Global Registry")
	host, port, user, keyPath := promptNewServer(reader)
	
	fmt.Print("Registry Name (e.g. prod-us): ")
	registryName, _ := reader.ReadString('\n')
	registryName = strings.TrimSpace(registryName)
	
	if registryName == "" {
		fmt.Println("Error: Registry name cannot be empty.")
		return
	}

	gCfg, _ := config.LoadGlobalConfig()
	if gCfg == nil {
		gCfg = &config.GlobalConfig{
			Servers: make(map[string]config.ServerConfig),
			Projects: make(map[string]string),
		}
	}
	
	if gCfg.Servers == nil { gCfg.Servers = make(map[string]config.ServerConfig) }
	
	gCfg.Servers[registryName] = config.ServerConfig{
		RegistryName: registryName,
		Host:         host,
		Port:         port,
		User:         user,
		KeyPath:      keyPath,
	}
	
	if err := config.SaveGlobalConfig(gCfg); err != nil {
		fmt.Printf("Error saving registry: %v\n", err)
		return
	}
	
	fmt.Printf("✅ Server '%s' added to registry.\n", registryName)
}

func runRegistryDel(name string) {
	gCfg, err := config.LoadGlobalConfig()
	if err != nil || gCfg == nil {
		fmt.Println("Error: Could not load global registry.")
		return
	}
	
	if _, exists := gCfg.Servers[name]; !exists {
		fmt.Printf("Error: Registry '%s' not found.\n", name)
		return
	}
	
	fmt.Printf("Are you sure you want to delete registry '%s'? (y/n): ", name)
	reader := bufio.NewReader(os.Stdin)
	confirm, _ := reader.ReadString('\n')
	confirm = strings.ToLower(strings.TrimSpace(confirm))
	
	if confirm != "y" && confirm != "yes" {
		fmt.Println("Delete aborted.")
		return
	}
	
	delete(gCfg.Servers, name)
	if err := config.SaveGlobalConfig(gCfg); err != nil {
		fmt.Printf("Error saving registry: %v\n", err)
		return
	}
	
	fmt.Printf("✅ Registry '%s' deleted.\n", name)
}

func runRegistryShell(registryName string, commandArgs []string) {
	gCfg, _ := config.LoadGlobalConfig()
	if gCfg == nil {
		fmt.Println("Error: Could not load global registry.")
		return
	}
	srv, exists := gCfg.Servers[registryName]
	if !exists {
		fmt.Printf("Error: Registry '%s' not found.\n", registryName)
		return
	}

	client, err := ssh.NewClient(srv.Host, srv.Port, srv.User, srv.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if len(commandArgs) == 0 {
		// Interactive SSH
		fmt.Printf("💻 Starting interactive SSH session on '%s' (%s)...\n", registryName, srv.Host)
		if err := client.InteractiveSession(); err != nil {
			fmt.Printf("SSH session error: %v\n", err)
		}
	} else {
		// Non-interactive command
		cmdStr := strings.Join(commandArgs, " ")
		fmt.Printf("🚀 Executing on '%s': %s\n", registryName, cmdStr)
		if err := client.RunCommand(cmdStr, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}

func runHostShell(commandArgs []string) {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if len(commandArgs) == 0 {
		// Interactive SSH
		fmt.Printf("💻 Starting interactive SSH session on '%s' (%s)...\n", cfg.Server.RegistryName, cfg.Server.Host)
		if err := client.InteractiveSession(); err != nil {
			fmt.Printf("SSH session error: %v\n", err)
		}
	} else {
		// Non-interactive command
		cmdStr := strings.Join(commandArgs, " ")
		fmt.Printf("🚀 Executing on '%s': %s\n", cfg.Server.RegistryName, cmdStr)
		if err := client.RunCommand(cmdStr, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}
func runInfra(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: graft infra [db|redis] ports:<value>")
		return
	}

	typ := args[0]
	if typ != "db" && typ != "redis" {
		fmt.Println("Error: First argument must be 'db' or 'redis'")
		return
	}

	var portVal string
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "ports:") {
			portVal = strings.TrimPrefix(arg, "ports:")
			break
		}
	}

	if portVal == "" {
		fmt.Println("Usage: graft infra [db|redis] ports:<value> (use 'ports:null' to hide)")
		return
	}

	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// Update port in config
	if typ == "db" {
		cfg.Infra.PostgresPort = portVal
	} else {
		cfg.Infra.RedisPort = portVal
	}

	// Re-run infra setup
	fmt.Printf("🔄 Updating %s port to: %s\n", typ, portVal)
	
	setupPG := cfg.Infra.PostgresUser != ""
	setupRedis := true // Assume redis exists if we are here, or based on previous host init
	
	// We need to know if redis was setup. Usually both are.
	// For now, assume both if they have been initialized.
	
	err = hostinit.SetupInfra(client, setupPG, setupRedis, cfg.Infra, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Printf("Error updating infrastructure: %v\n", err)
		return
	}

	// Save updated config locally
	config.SaveConfig(cfg, true)
	fmt.Println("\n✅ Infrastructure updated successfully!")
}

func runInfraReload() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("🔄 Reloading infrastructure (pulling latest images)...")
	
	// Use docker compose up -d --pull always to pull and reload
	reloadCmd := "cd /opt/graft/infra && sudo docker compose up -d --pull always"
	if err := client.RunCommand(reloadCmd, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("Error reloading infrastructure: %v\n", err)
		return
	}

	fmt.Println("\n✅ Infrastructure reloaded successfully!")
}

func runHostSelfDestruct() {
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Println("Error: No config found.")
		return
	}

	reader := bufio.NewReader(os.Stdin)
	
	fmt.Println("\n⚠️  WARNING: DESTRUCTIVE OPERATION ⚠️")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("This will PERMANENTLY DELETE all Graft infrastructure on:\n")
	fmt.Printf("  Host: %s\n", cfg.Server.Host)
	fmt.Printf("  Registry: %s\n\n", cfg.Server.RegistryName)
	fmt.Println("The following will be destroyed:")
	fmt.Println("  • Gateway (Traefik) - including SSL certificates")
	fmt.Println("  • Infrastructure (Postgres, Redis) - including ALL DATA")
	fmt.Println("  • All Projects - including volumes and images")
	fmt.Println("  • All Docker networks created by Graft")
	fmt.Println("  • All files in /opt/graft/")
	fmt.Println("\n⚠️  THIS CANNOT BE UNDONE! ⚠️")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	
	fmt.Print("\nType 'DESTROY' (all caps) to confirm: ")
	confirm, _ := reader.ReadString('\n')
	confirm = strings.TrimSpace(confirm)
	
	if confirm != "DESTROY" {
		fmt.Println("❌ Self-destruct aborted. No changes made.")
		return
	}
	
	fmt.Print("\nAre you absolutely sure? Type 'YES' to proceed: ")
	finalConfirm, _ := reader.ReadString('\n')
	finalConfirm = strings.TrimSpace(finalConfirm)
	
	if finalConfirm != "YES" {
		fmt.Println("❌ Self-destruct aborted. No changes made.")
		return
	}

	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("\n💥 Initiating self-destruct sequence...")
	
	// Step 1: Get list of all projects
	fmt.Println("\n[1/7] 📋 Discovering projects...")
	tmpFile := filepath.Join(os.TempDir(), "projects_list.json")
	var projects []string
	if err := client.DownloadFile(config.RemoteProjectsPath, tmpFile); err == nil {
		data, _ := os.ReadFile(tmpFile)
		var projectMap map[string]string
		if json.Unmarshal(data, &projectMap) == nil {
			for name := range projectMap {
				projects = append(projects, name)
			}
		}
		os.Remove(tmpFile)
	}
	
	if len(projects) > 0 {
		fmt.Printf("      Found %d project(s): %v\n", len(projects), projects)
	} else {
		fmt.Println("      No projects found")
	}
	
	// Step 2: Tear down all projects
	if len(projects) > 0 {
		fmt.Println("\n[2/7] 🗑️  Destroying all projects...")
		for _, project := range projects {
			fmt.Printf("      Destroying project: %s\n", project)
			projectPath := fmt.Sprintf("/opt/graft/projects/%s", project)
			
			// Stop and remove all containers, volumes, and networks for this project
			destroyCmd := fmt.Sprintf("cd %s && sudo docker compose down -v --remove-orphans 2>/dev/null || true", projectPath)
			client.RunCommand(destroyCmd, os.Stdout, os.Stderr)
		}
	} else {
		fmt.Println("\n[2/7] ⏭️  Skipping projects (none found)")
	}
	
	// Step 3: Tear down infrastructure (Postgres, Redis)
	fmt.Println("\n[3/7] 🗄️  Destroying infrastructure (Postgres, Redis)...")
	infraCmd := "cd /opt/graft/infra && sudo docker compose down -v --remove-orphans 2>/dev/null || true"
	client.RunCommand(infraCmd, os.Stdout, os.Stderr)
	
	// Step 4: Tear down gateway (Traefik)
	fmt.Println("\n[4/7] 🌐 Destroying gateway (Traefik)...")
	gatewayCmd := "cd /opt/graft/gateway && sudo docker compose down -v --remove-orphans 2>/dev/null || true"
	client.RunCommand(gatewayCmd, os.Stdout, os.Stderr)
	
	// Step 5: Remove all Graft-related images
	fmt.Println("\n[5/7] 🖼️  Removing all Docker images...")
	pruneImagesCmd := "sudo docker image prune -af"
	client.RunCommand(pruneImagesCmd, os.Stdout, os.Stderr)
	
	// Step 6: Remove Graft networks
	fmt.Println("\n[6/7] 🔌 Removing Graft networks...")
	removeNetworkCmd := "sudo docker network rm graft-public 2>/dev/null || true"
	client.RunCommand(removeNetworkCmd, os.Stdout, os.Stderr)
	
	// Step 7: Remove all Graft files
	fmt.Println("\n[7/7] 📁 Removing all Graft files...")
	removeFilesCmd := "sudo rm -rf /opt/graft"
	if err := client.RunCommand(removeFilesCmd, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("      ⚠️  Warning: %v\n", err)
	}
	
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("💥 Self-destruct complete!")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("\nThe server has been cleaned of all Graft infrastructure.")
	fmt.Println("Docker and Docker Compose remain installed.")
	fmt.Println("\n💡 You can run 'graft host init' to set up a fresh environment.")
}
