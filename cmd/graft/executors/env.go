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
	"github.com/skssmd/graft/internal/deploy"
	"github.com/skssmd/graft/internal/git"
	"github.com/skssmd/graft/internal/hostinit"
	"github.com/skssmd/graft/internal/ssh"
)

func (e *Executor) RunNewEnv(name string) {
	reader := bufio.NewReader(os.Stdin)

	// 1. Load existing project environment to inherit basics
	pEnv, err := config.LoadProjectEnv()
	if err != nil {
		fmt.Printf("❌ Error: Could not load project configuration. Make sure you are in a Graft project directory: %v\n", err)
		return
	}

	projName := pEnv.Name
	deploymentMode := pEnv.DeploymentMode

	if _, exists := pEnv.Env[name]; exists {
		fmt.Printf("❌ Error: Environment '%s' already exists for this project.\n", name)
		return
	}

	fmt.Printf("🚀 Adding new environment '%s' to project '%s' (Mode: %s)\n", name, projName, deploymentMode)

	// 2. Server Selection (Copy from RunInit)
	gCfg, _ := config.LoadGlobalConfig()
	var host, user, keyPath string
	var port int
	var registryName string

	if gCfg != nil && len(gCfg.Servers) > 0 {
		fmt.Println("\n📋 Available servers in registry:")
		var keys []string
		i := 1
		for n, srv := range gCfg.Servers {
			fmt.Printf("  [%d] %s (%s)\n", i, n, srv.Host)
			keys = append(keys, n)
			i++
		}
		fmt.Printf("\nSelect a server [1-%d] or type '/new' for a new connection: ", len(keys))

		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		if input == "/new" {
			host, port, user, keyPath = promptNewServer(reader)
			fmt.Print("Registry Name (e.g. staging-us): ")
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
				fmt.Print("Registry Name (e.g. staging-us): ")
				registryName, _ = reader.ReadString('\n')
				registryName = strings.TrimSpace(registryName)
			}
		}
	} else {
		fmt.Println("No servers found in registry. Enter new server details:")
		host, port, user, keyPath = promptNewServer(reader)
		fmt.Print("Registry Name (e.g. staging-us): ")
		registryName, _ = reader.ReadString('\n')
		registryName = strings.TrimSpace(registryName)
	}

	// Update global registry
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

	projFull := fmt.Sprintf("%s-%s", projName, name)
	var versionToKeep int
	var remoteProjects map[string]interface{}

	// 3. Remote Conflict & Host Check
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

		if _, exists := remoteProjects[projFull]; exists {
			fmt.Printf("❌ Conflict: Environment '%s' already exists on this server.\n", projFull)
			return
		}
		
		remoteProjects[projFull] = map[string]interface{}{
			"path": fmt.Sprintf("/opt/graft/projects/%s", projFull),
		}
	}

	// 4. Prompts
	fmt.Print("Domain (e.g. staging.example.com): ")
	domain, _ := reader.ReadString('\n')
	domain = strings.TrimSpace(domain)

	fmt.Println("\n🔄 Rollback configurations")
	fmt.Print("Do you want to setup rollback configurations? (y/N): ")
	input, _ := reader.ReadString('\n')
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "y" || input == "yes" {
		fmt.Print("Enter the number of versions to keep: ")
		rollInput, _ := reader.ReadString('\n')
		versionToKeep, _ = strconv.Atoi(strings.TrimSpace(rollInput))
		if versionToKeep > 0 {
			fmt.Printf("✅ Rollback configured to keep %d versions\n", versionToKeep)
		}
	}

	var gitBranch string
	if strings.HasPrefix(deploymentMode, "git") {
		branch, err := e.promptGitBranch(reader)
		if err == nil {
			gitBranch = branch
			fmt.Printf("✅ Selected branch: %s\n", gitBranch)
		}
	}

	// 5. Update remote registry
	if client != nil && remoteProjects != nil {
		data, _ := json.MarshalIndent(remoteProjects, "", "  ")
		tmpPath := filepath.Join(os.TempDir(), "upload_projects.json")
		os.WriteFile(tmpPath, data, 0644)
		client.UploadFile(tmpPath, config.RemoteProjectsPath)
		os.Remove(tmpPath)
		fmt.Println("✅ Remote project registry updated")
	}

	// 6. Setup remote directory
	remoteProjPath := fmt.Sprintf("/opt/graft/projects/%s", projFull)
	if client != nil {
		fmt.Printf("📂 Setting up remote project directory: %s\n", remoteProjPath)
		client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown $USER:$USER %s", remoteProjPath, remoteProjPath), nil, nil)
	}

	// 7. Save Project Metadata
	meta := &config.ProjectMetadata{
		Name:            projName,
		RemotePath:      remoteProjPath,
		Domain:          domain,
		DeploymentMode:  deploymentMode,
		GitBranch:       gitBranch,
		RollbackBackups: versionToKeep,
		Registry:        registryName,
		Initialized:     false,
	}

	// Fetch Hook URL if available
	if srv, exists := gCfg.Servers[registryName]; exists {
		meta.GraftHookURL = srv.GraftHookURL
	}

	if err := config.SaveProjectMetadata(name, meta); err != nil {
		fmt.Printf("❌ Failed to save environment metadata: %v\n", err)
		return
	}

	fmt.Printf("\n✨ Environment '%s' successfully added to project '%s'!\n", name, projName)
	
	// 8. Generate CI/CD workflows if in git mode
	if strings.HasPrefix(deploymentMode, "git") {
		fmt.Printf("📦 Git-based environment detected. Setting up workflows for %s...\n", name)
		
		remoteURL, err := git.GetRemoteURL(".", "origin")
		if err != nil {
			fmt.Printf("⚠️  Warning: Could not get git remote URL for workflow generation: %v\n", err)
		} else {
			// Load the project for workflow generation
			// We can reconstruct a basic Project object since we only need Name/DeploymentMode
			p := &deploy.Project{
				Name:           projName,
				DeploymentMode: deploymentMode,
			}
			
			if err := deploy.GenerateWorkflows(p, name, remoteURL, deploymentMode, meta.GraftHookURL); err != nil {
				fmt.Printf("⚠️  Warning: Failed to generate workflows: %v\n", err)
			} else {
				fmt.Printf("✅ GitHub Workflows created in .github/workflows/ (deploy-%s.yml, ci-%s.yml)\n", name, name)
			}
		}
	}

	fmt.Printf("📍 Switch Context: graft env %s\n", name)
	fmt.Printf("🚀 Deploy: graft env %s sync\n", name)
}

