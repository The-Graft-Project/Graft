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

	command := os.Args[1]

	switch command {
	case "init":
		runInit()
	case "host":
		if len(os.Args) < 3 {
			fmt.Println("Usage: graft host [init|clean]")
			return
		}
		switch os.Args[2] {
		case "init":
			runHostInit()
		case "clean":
			runHostClean()
		default:
			fmt.Println("Usage: graft host [init|clean]")
		}
	case "db":
		if len(os.Args) < 4 || os.Args[3] != "init" {
			fmt.Println("Usage: graft db <name> init")
			return
		}
		runInfraInit("postgres", os.Args[2])
	case "redis":
		if len(os.Args) < 4 || os.Args[3] != "init" {
			fmt.Println("Usage: graft redis <name> init")
			return
		}
		runInfraInit("redis", os.Args[2])
	case "logs":
		if len(os.Args) < 3 {
			fmt.Println("Usage: graft logs <service>")
			return
		}
		runLogs(os.Args[2])
	case "sync":
		// Check if "compose" subcommand is specified
		if len(os.Args) > 2 && os.Args[2] == "compose" {
			runSyncCompose()
		} else {
			runSync()
		}
	default:
		// Pass through to docker compose for any other command
		runDockerCompose(os.Args[1:])
	}
}

func printUsage() {
	fmt.Println("Graft CLI - Interactive Deployment Tool")
	fmt.Println("\nUsage:")
	fmt.Println("  graft init               Initialize local project")
	fmt.Println("  graft host init          Setup remote server")
	fmt.Println("  graft host clean         Clean Docker caches")
	fmt.Println("  graft db <name> init     Deploy Postgres instance")
	fmt.Println("  graft redis <name> init  Deploy Redis instance")
	fmt.Println("  graft sync               Deploy project to server")
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

	// Check ONLY for global config (not local)
	globalCfg, _ := loadGlobalConfig()
	
	var host, user, keyPath string
	var port int

	if globalCfg != nil {
		// Show existing config and ask for confirmation
		fmt.Println("\n📋 Found existing global configuration:")
		fmt.Printf("  Host: %s\n", globalCfg.Server.Host)
		fmt.Printf("  Port: %d\n", globalCfg.Server.Port)
		fmt.Printf("  User: %s\n", globalCfg.Server.User)
		fmt.Printf("  Key:  %s\n", globalCfg.Server.KeyPath)
		fmt.Print("\nUse this configuration? (y/n): ")
		
		confirm, _ := reader.ReadString('\n')
		confirm = strings.ToLower(strings.TrimSpace(confirm))
		
		if confirm == "y" || confirm == "yes" {
			host = globalCfg.Server.Host
			user = globalCfg.Server.User
			port = globalCfg.Server.Port
			keyPath = globalCfg.Server.KeyPath
			fmt.Println("✅ Using global configuration")
		} else {
			fmt.Println("\n🔧 Enter new server details:")
			fmt.Print("Host IP: ")
			host, _ = reader.ReadString('\n')
			host = strings.TrimSpace(host)

			fmt.Print("Port (22): ")
			portStr, _ := reader.ReadString('\n')
			port, _ = strconv.Atoi(strings.TrimSpace(portStr))
			if port == 0 { port = 22 }

			fmt.Print("User: ")
			user, _ = reader.ReadString('\n')
			user = strings.TrimSpace(user)

			fmt.Print("Key Path: ")
			keyPath, _ = reader.ReadString('\n')
			keyPath = strings.TrimSpace(keyPath)
		}
	} else {
		fmt.Println("No global config found. Enter server details:")
		fmt.Print("Host IP: ")
		host, _ = reader.ReadString('\n')
		host = strings.TrimSpace(host)

		fmt.Print("Port (22): ")
		portStr, _ := reader.ReadString('\n')
		port, _ = strconv.Atoi(strings.TrimSpace(portStr))
		if port == 0 { port = 22 }

		fmt.Print("User: ")
		user, _ = reader.ReadString('\n')
		user = strings.TrimSpace(user)

		fmt.Print("Key Path: ")
		keyPath, _ = reader.ReadString('\n')
		keyPath = strings.TrimSpace(keyPath)
	}

	fmt.Print("Project Name: ")
	projName, _ := reader.ReadString('\n')
	projName = strings.TrimSpace(projName)

	fmt.Print("Domain (e.g. app.example.com): ")
	domain, _ := reader.ReadString('\n')
	domain = strings.TrimSpace(domain)

	// Save local config
	cfg := &config.GraftConfig{
		Server: config.ServerConfig{
			Host: host, Port: port, User: user, KeyPath: keyPath,
		},
	}
	config.SaveConfig(cfg, true) // local

	// Save global if not exists
	if globalCfg == nil {
		config.SaveConfig(cfg, false) // global
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

	// Ask about shared infrastructure
	reader := bufio.NewReader(os.Stdin)
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
			cfg.Infra.PostgresUser = "graft_admin_" + config.GenerateRandomString(4)
			cfg.Infra.PostgresPassword = config.GenerateRandomString(24)
			cfg.Infra.PostgresDB = "graft_master_" + config.GenerateRandomString(4)
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

func runSync() {
	// Check if a specific service is specified
	var serviceName string
	var noCache bool
	var partial bool
	
	// Parse arguments: graft sync [service] [--no-cache] [-p|--partial]
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--no-cache" {
			noCache = true
		} else if arg == "-p" || arg == "--partial" {
			partial = true
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
		if noCache {
			fmt.Println("🔥 No-cache mode enabled")
		}
		if partial {
			fmt.Println("📦 Partial sync enabled (upload only)")
		}
		err = deploy.SyncService(client, p, serviceName, noCache, partial, os.Stdout, os.Stderr)
	} else {
		if noCache {
			fmt.Println("🔥 No-cache mode enabled")
		}
		if partial {
			fmt.Println("🚀 Partial sync enabled (upload only)")
		}
		err = deploy.Sync(client, p, noCache, partial, os.Stdout, os.Stderr)
	}

	if err != nil {
		fmt.Printf("Error during sync: %v\n", err)
		return
	}

	if !partial {
		fmt.Println("\n✅ Sync complete!")
	}
}

func runSyncCompose() {
	var partial bool
	// Parse arguments: graft sync compose [-p|--partial]
	for i := 3; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "-p" || arg == "--partial" {
			partial = true
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

	if partial {
		fmt.Println("📄 Partial sync enabled (config upload only)")
	}

	err = deploy.SyncComposeOnly(client, p, partial, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Printf("Error during sync: %v\n", err)
		return
	}

	if !partial {
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
