package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/deploy"
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
		runInit()
	case "host":
		if len(args) < 2 {
			fmt.Println("Usage: graft host [init|clean]")
			return
		}
		switch args[1] {
		case "init":
			runHostInit()
		case "clean":
			runHostClean()
		default:
			fmt.Println("Usage: graft host [init|clean]")
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
	default:
		// Pass through to docker compose for any other command
		runDockerCompose(args)
	}
}

func printUsage() {
	fmt.Println("Graft CLI - Interactive Deployment Tool")
	fmt.Println("\nUsage:")
	fmt.Println("  graft [flags] <command> [args]")
	fmt.Println("\nFlags:")
	fmt.Println("  -p, --project <name>     Run command in specific project context")
	fmt.Println("\nCommands:")
	fmt.Println("  init                     Initialize local project")
	fmt.Println("  host init                Setup remote server")
	fmt.Println("  host clean               Clean Docker caches")
	fmt.Println("  db <name> init           Deploy Postgres instance")
	fmt.Println("  redis <name> init        Deploy Redis instance")
	fmt.Println("  sync [service]           Deploy project to server")
	fmt.Println("  logs <service>           Stream service logs")
}

// Helper to load only global config (not local)
func loadGlobalConfig() (*config.GraftConfig, error) {
	globalPath := config.GetGlobalConfigPath()
	data, err := os.ReadFile(globalPath)
	if err != nil {
		return nil, err
	}
	var cfg config.GraftConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func runInit() {
	reader := bufio.NewReader(os.Stdin)

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

	fmt.Print("Domain (e.g. app.example.com): ")
	domain, _ := reader.ReadString('\n')
	domain = strings.TrimSpace(domain)

	// Save local config
	cfg := &config.GraftConfig{
		Server: config.ServerConfig{
			RegistryName: registryName,
			Host:         host, Port: port, User: user, KeyPath: keyPath,
		},
	}
	config.SaveConfig(cfg, true) // local

	// Update global registry if new
	if gCfg == nil {
		gCfg, _ = config.LoadGlobalConfig()
	}
	if gCfg != nil {
		if gCfg.Servers == nil { gCfg.Servers = make(map[string]config.ServerConfig) }
		gCfg.Servers[registryName] = cfg.Server
		config.SaveGlobalConfig(gCfg)
	}

	// Generate boilerplate
	p := deploy.GenerateBoilerplate(projName, domain)
	p.Save(".")

	// Save project metadata
	meta := &config.ProjectMetadata{
		Name:       projName,
		RemotePath: fmt.Sprintf("/opt/graft/projects/%s", projName),
	}
	if err := config.SaveProjectMetadata(meta); err != nil {
		fmt.Printf("Warning: Could not save project metadata: %v\n", err)
	}

	fmt.Printf("\n✨ Project '%s' initialized!\n", projName)
	fmt.Printf("Local config: .graft/config.json\n")
	fmt.Printf("Boilerplate: graft-compose.yml\n")
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

	fmt.Print("Setup shared Redis instance? (y/n): ")
	confirmRedis, _ := reader.ReadString('\n')
	confirmRedis = strings.ToLower(strings.TrimSpace(confirmRedis))
	setupRedis := confirmRedis == "y" || confirmRedis == "yes"

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
				fmt.Fprintln(os.Stdout, "✅ Existing credentials found and loaded from remote server")
			}
			os.Remove(tmpFile)
		} else {
			fmt.Println("🔐 Generating new secure credentials for Postgres...")
			cfg.Infra.PostgresUser = strings.ToLower("graft_admin_" + config.GenerateRandomString(4))
			cfg.Infra.PostgresPassword = config.GenerateRandomString(24)
			cfg.Infra.PostgresDB = strings.ToLower("graft_master_" + config.GenerateRandomString(4))
		}
	}

	err = hostinit.InitHost(client, setupPostgres, setupRedis, 
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

	err = deploy.SyncComposeOnly(client, p, heave, os.Stdout, os.Stderr)
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
