package executors

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/ssh"
)

func (e *Executor) RunRegistryAdd() {
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
			Servers:  make(map[string]config.ServerConfig),
			Projects: make(map[string]string),
		}
	}

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

	if err := config.SaveGlobalConfig(gCfg); err != nil {
		fmt.Printf("Error saving registry: %v\n", err)
		return
	}

	fmt.Printf("✅ Server '%s' added to registry.\n", registryName)
}

func (e *Executor) RunRegistryDel(name string) {
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

func (e *Executor) RunRegistryShell(registryName string, commandArgs []string) {
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

func (e *Executor) RunRegistryLs() {
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

func (e *Executor) RunProjectsLs(registryName string) {
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
