package executors

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/prompt"
	"github.com/skssmd/graft/internal/server/ssh"
)

func (e *Executor) RunRegistryAdd() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\n➕ Add New Server to Global Registry")
	host, port, user, keyPath := prompt.PromptNewServer(reader)

	fmt.Print("Registry Name (e.g. prod-us): ")
	registryName, _ := reader.ReadString('\n')
	registryName = strings.TrimSpace(registryName)

	if registryName == "" {
		fmt.Println("Error: Registry name cannot be empty.")
		return
	}

	gCfg := e.GlobalConfig
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
	gCfg := e.GlobalConfig
	if gCfg == nil {
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

	// Do not leave the default pointing at a registry that no longer exists.
	clearedDefault := false
	if gCfg.Default == name {
		gCfg.Default = ""
		clearedDefault = true
	}

	if err := config.SaveGlobalConfig(gCfg); err != nil {
		fmt.Printf("Error saving registry: %v\n", err)
		return
	}

	fmt.Printf("✅ Registry '%s' deleted.\n", name)
	if clearedDefault {
		fmt.Println("   It was the default registry, so the default is now unset.")
	}
}

// RunSetDefaultRegistry marks a registry as the fallback for commands run
// outside any project directory.
func (e *Executor) RunSetDefaultRegistry(name string) {
	gCfg, err := config.LoadGlobalConfig()
	if err != nil || gCfg == nil {
		fmt.Println("Error: Could not load global registry.")
		return
	}

	srv, exists := gCfg.Servers[name]
	if !exists {
		fmt.Printf("Error: Registry '%s' not found.\n", name)
		if len(gCfg.Servers) > 0 {
			fmt.Println("\nAvailable registries:")
			for n := range gCfg.Servers {
				fmt.Printf("  - %s\n", n)
			}
		}
		return
	}

	gCfg.Default = name
	if err := config.SaveGlobalConfig(gCfg); err != nil {
		fmt.Printf("Error saving registry: %v\n", err)
		return
	}

	fmt.Printf("✅ Default registry set to '%s' (%s@%s)\n", name, srv.User, srv.Host)
	fmt.Println("   Commands run outside a project directory will target this server.")
}

// RunShowDefaultRegistry reports the current default registry.
func (e *Executor) RunShowDefaultRegistry() {
	gCfg, err := config.LoadGlobalConfig()
	if err != nil || gCfg == nil {
		fmt.Println("Error: Could not load global registry.")
		return
	}

	name, ok := gCfg.DefaultRegistry()
	switch {
	case name == "":
		fmt.Println("No default registry set. Set one with: graft -default <name>")
	case !ok:
		fmt.Printf("⚠️  Default registry '%s' no longer exists. Set another with: graft -default <name>\n", name)
	default:
		srv := gCfg.Servers[name]
		fmt.Printf("Default registry: %s (%s@%s)\n", name, srv.User, srv.Host)
	}
}

func (e *Executor) RunRegistryShell(registryName string, commandArgs []string) {
	gCfg := e.GlobalConfig
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

func (e *Executor) RunRegistryDocker(registry string, args []string){
	gCfg := e.GlobalConfig
	if gCfg == nil {
		fmt.Println("Error: Could not load global registry.")
		return
	}
	srv, exists := gCfg.Servers[registry]
	if !exists {
		fmt.Printf("Error: Registry '%s' not found.\n", registry)
		return
	}

	client, err := ssh.NewClient(srv.Host, srv.Port, srv.User, srv.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if len(args) == 0 {
		fmt.Printf("Usage: graft -r <registry name> <any docker command>\n")
		fmt.Printf("Example: graft -r prod-us ps\n")
		return
	} else {
		cmdStr := "sudo docker " + strings.Join(args, " ")
		fmt.Printf("🚀 Executing on '%s': %s\n", registry, cmdStr)
		if err := client.RunCommand(cmdStr, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
}

func (e *Executor) RunRegistryLs() {
	gCfg := e.GlobalConfig
	if gCfg == nil || len(gCfg.Servers) == 0 {
		fmt.Println("No servers found in global registry.")
		return
	}

	fmt.Println("\n📋 Registered Servers:")
	fmt.Printf("%-15s %-20s %-10s %-10s %s\n", "Name", "Host", "User", "Port", "Default")
	fmt.Println(strings.Repeat("-", 68))
	for name, srv := range gCfg.Servers {
		marker := ""
		if name == gCfg.Default {
			marker = "*"
		}
		fmt.Printf("%-15s %-20s %-10s %-10d %s\n", name, srv.Host, srv.User, srv.Port, marker)
	}
	fmt.Println()
	if name, ok := gCfg.DefaultRegistry(); ok {
		fmt.Printf("* default registry: commands run outside a project target '%s'\n\n", name)
	}
}

func (e *Executor) RunProjectsLs(registryName string) {
	gCfg := e.GlobalConfig
	if gCfg == nil {
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
			localMetaPath := filepath.Join(path, ".graft", "project.json")
			if data, err := os.ReadFile(localMetaPath); err == nil {
				var projectEnv config.ProjectEnv
				if err := json.Unmarshal(data, &projectEnv); err == nil {
					if prodMeta, exists := projectEnv.Env["prod"]; exists && prodMeta.Registry != "" {
						serverName = prodMeta.Registry
					}
				}
			}
			fmt.Printf("%-20s %-15s %-40s\n", name, serverName, path)
		}
		fmt.Println()
	}
}
