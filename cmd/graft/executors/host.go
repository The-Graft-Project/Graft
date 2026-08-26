package executors

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/hostinit"
)

func (e *Executor) RunHostInit() {
	cfg:=e

	client, err := e.getClient()
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

	}

	// Register in global registry
	gCfg := e.GlobalConfig
	if gCfg != nil {
		if gCfg.Servers == nil {
			gCfg.Servers = make(map[string]config.ServerConfig)
		}
		srv := gCfg.Servers[cfg.Server.RegistryName]
		srv.RegistryName = cfg.Server.RegistryName
		srv.Host = cfg.Server.Host
		srv.Port = cfg.Server.Port
		srv.User = cfg.Server.User
		srv.KeyPath = cfg.Server.KeyPath
		if cfg.Server.GraftHookURL != "" {
			srv.GraftHookURL = cfg.Server.GraftHookURL
		}
		gCfg.Servers[cfg.Server.RegistryName] = srv
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
	var infraCfg config.InfraConfig
	// Secure credentials for infrastructure
	if setupPostgres {
		// Try to pull existing from remote server first
		fmt.Fprintln(os.Stdout, "🔍 Checking for existing infrastructure credentials on remote server...")
		tmpFile := filepath.Join(os.TempDir(), "host_infra.config")

		if err := client.DownloadFile(config.RemoteInfraPath, tmpFile); err == nil {
			data, _ := os.ReadFile(tmpFile)

			if err := json.Unmarshal(data, &infraCfg); err == nil {
			}
			os.Remove(tmpFile)
		} else {
			fmt.Println("🔐 Generating new secure credentials for Postgres...")
			infraCfg.PostgresUser = strings.ToLower("graft_admin_" + config.GenerateRandomString(4))
			infraCfg.PostgresPassword = config.GenerateRandomString(24)
			infraCfg.PostgresDB = strings.ToLower("graft_master_" + config.GenerateRandomString(4))
		}
	}

	err = hostinit.InitHost(client, setupPostgres, setupRedis, exposePostgres, exposeRedis,
		infraCfg.PostgresUser, infraCfg.PostgresPassword, infraCfg.PostgresDB,
		os.Stdout, os.Stderr)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Println("\n✅ Host initialized successfully!")
}

func (e *Executor) RunHostClean() {

	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("🧹 Cleaning Docker caches and unused resources...")

	cleanupCmds := []struct {
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
func (e *Executor) RunHostSelfDestruct() {

	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\n⚠️  WARNING: DESTRUCTIVE OPERATION ⚠️")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("This will PERMANENTLY DELETE all Graft infrastructure on:\n")
	fmt.Printf("  Host: %s\n", e.Server.Host)
	fmt.Printf("  Registry: %s\n\n", e.Server.RegistryName)
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

	client, err := e.getClient()
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

func (e *Executor) RunHostShell(commandArgs []string) {
	

	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if len(commandArgs) == 0 {
		// Interactive SSH
		fmt.Printf("💻 Starting interactive SSH session on '%s' (%s)...\n", e.Server.RegistryName, e.Server.Host)
		if err := client.InteractiveSession(); err != nil {
			fmt.Printf("SSH session error: %v\n", err)
		}
	} else {
		// Non-interactive command
		cmdStr := strings.Join(commandArgs, " ")
		fmt.Printf("🚀 Executing on '%s': %s\n", e.Server.RegistryName, cmdStr)
		if err := client.RunCommand(cmdStr, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}

// RunTunnel tunnels a Docker container's port to localhost.
// remotePort: the container port to tunnel; 0 auto-detects from exposed ports
// (prompting if there are several). localPort: the local port to bind; 0 uses remotePort.
func (e *Executor) RunTunnel(container string, remotePort int, localPort int) {
	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// Check container exists and get its IP
	containerIP, err := client.GetCommandOutput(
		fmt.Sprintf("sudo docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' %s", container))
	if err != nil || strings.TrimSpace(containerIP) == "" {
		fmt.Printf("Error: Container '%s' not found or not running.\n", container)
		return
	}
	containerIP = strings.TrimSpace(containerIP)

	if remotePort == 0 {
		// Auto-detect exposed ports
		portsRaw, err := client.GetCommandOutput(
			fmt.Sprintf("sudo docker inspect -f '{{range $p, $conf := .Config.ExposedPorts}}{{$p}} {{end}}' %s", container))
		if err != nil || strings.TrimSpace(portsRaw) == "" {
			fmt.Printf("Error: Could not detect exposed ports for '%s'.\n", container)
			return
		}

		// Parse ports — format is "5432/tcp 8080/tcp ..."
		var ports []int
		for _, p := range strings.Fields(strings.TrimSpace(portsRaw)) {
			parts := strings.Split(p, "/")
			if len(parts) > 0 {
				if port, err := strconv.Atoi(parts[0]); err == nil {
					ports = append(ports, port)
				}
			}
		}

		if len(ports) == 0 {
			fmt.Printf("Error: No exposed ports found for '%s'.\n", container)
			return
		}

		remotePort = ports[0]
		if len(ports) > 1 {
			fmt.Printf("📋 Container '%s' exposes multiple ports:\n", container)
			for i, p := range ports {
				fmt.Printf("  [%d] %d\n", i+1, p)
			}
			fmt.Print("Select port (or rerun with -p <port>:<localport>): ")
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(input)
			idx, err := strconv.Atoi(input)
			if err != nil || idx < 1 || idx > len(ports) {
				fmt.Println("Invalid selection.")
				return
			}
			remotePort = ports[idx-1]
		}
	}

	if localPort == 0 {
		localPort = remotePort
	}

	fmt.Printf("🔗 Tunneling %s:%d → localhost:%d\n", container, remotePort, localPort)
	fmt.Printf("   Connect to localhost:%d to reach the service.\n", localPort)
	fmt.Println("\nPress Ctrl+C to stop the tunnel.")

	if err := client.Tunnel(localPort, containerIP, remotePort); err != nil {
		fmt.Printf("\nTunnel error: %v\n", err)
	}
}

func (e *Executor) RunHostDocker(commandArgs []string) {
	

	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if len(commandArgs) == 0 {
		// Interactive SSH
		fmt.Println("Usage: graft host [init|clean|sh|self-destruct|any docker command]")
	} else {
		// Non-interactive command
		cmdStr := "sudo docker " + strings.Join(commandArgs, " ")
		fmt.Printf("🚀 Executing on '%s': %s\n", e.Server.RegistryName, cmdStr)
		if err := client.RunCommand(cmdStr, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}
