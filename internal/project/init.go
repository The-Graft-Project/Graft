package project

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/deploy"
	"github.com/skssmd/graft/internal/hostinit"
	"github.com/skssmd/graft/internal/prompt"
	"github.com/skssmd/graft/internal/ssh"
	"github.com/skssmd/graft/internal/webhook"
)

// CheckLocalDirectory checks if the current directory is already initialized with Graft
func CheckLocalDirectory(reader *bufio.Reader) error {
	configPath := filepath.Join(".graft", "config.json")
	projectPath := filepath.Join(".graft", "project.json")
	if _, err := os.Stat(configPath); err == nil {
		if _, err := os.Stat(projectPath); err == nil {
			fmt.Print("\n⚠️  This directory is already initialized with Graft. Do you want to proceed? (y/n): ")
			input, _ := reader.ReadString('\n')
			input = strings.ToLower(strings.TrimSpace(input))
			if input != "y" && input != "yes" {
				fmt.Println("❌ Init aborted.")
				return errors.New("Init aborted.")
			}
			fmt.Println("✅ Proceeding with re-initialization...")
		}
	}
	return nil
}

// SelectOrAddServer prompts the user to select an existing server or add a new one
func SelectOrAddServer(reader *bufio.Reader, gCfg *config.GlobalConfig, promptNewServerFunc func(*bufio.Reader) (string, int, string, string)) (*config.ServerConfig, error) {
	var host string
	var port int
	var user string
	var keyPath string
	var registryName string
	var srv config.ServerConfig
	
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
			host, port, user, keyPath = promptNewServerFunc(reader)
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
				host, port, user, keyPath = promptNewServerFunc(reader)
				fmt.Print("Registry Name (e.g. prod-us): ")
				registryName, _ = reader.ReadString('\n')
				registryName = strings.TrimSpace(registryName)
			}
		}
	} else {
		fmt.Println("No servers found in registry. Enter new server details:")
		host, port, user, keyPath = promptNewServerFunc(reader)
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
	return &srv, nil
}

// DefineProjectName prompts the user for a project name and validates it
func DefineProjectName(reader *bufio.Reader) string {
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
	return projName
}

// LocalConfigCheck checks for local project conflicts
func LocalConfigCheck(reader *bufio.Reader, gCfg *config.GlobalConfig, projName string, force bool) bool {
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
				return false
			}
			fmt.Println("✅ Local overwrite confirmed.")
		}
	}
	return true
}

// RemoteConflictCheck checks for remote project conflicts and initializes the host if needed
func RemoteConflictCheck(reader *bufio.Reader, srv *config.ServerConfig, projFull string, force bool, client *ssh.Client) (map[string]interface{}, error) {
	remoteProjects := make(map[string]interface{})
	fmt.Printf("🔍 Checking for conflicts on remote server '%s'...\n", srv.Host)

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
				client.Close()
				return nil, err
			}
			fmt.Println("✅ Host initialized.")
		} else {
			fmt.Println("⏭️  Skipping host initialization. Some features may not work.")
		}
	}

	// Ensure config dir exists
	client.RunCommand("sudo mkdir -p /opt/graft/config && sudo chown $USER:$USER /opt/graft/config", os.Stdout, os.Stderr)

	tmpFile := filepath.Join(os.TempDir(), "remote_projects.json")

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
		client.Close()
		return nil, errors.New("Project already exists on this server.")
	}

	// Update remote registry (local record for now, will upload after boilerplate generation)
	remoteProjects[projFull] = map[string]interface{}{
		"path": fmt.Sprintf("/opt/graft/projects/%s", projFull),
	}

	return remoteProjects, nil
}

// UpdateRemoteRegistry uploads the project registry to the remote server
func UpdateRemoteRegistry(client *ssh.Client, remoteProjects map[string]interface{}) {
	if client == nil || remoteProjects == nil {
		return
	}
	data, _ := json.MarshalIndent(remoteProjects, "", "  ")
	tmpPath := filepath.Join(os.TempDir(), "upload_projects.json")
	os.WriteFile(tmpPath, data, 0644)
	client.UploadFile(tmpPath, config.RemoteProjectsPath)
	os.Remove(tmpPath)
	fmt.Println("✅ Remote project registry updated")
}

// SetupRemoteProjectDirectory creates the remote project directory and optionally initializes git
func SetupRemoteProjectDirectory(client *ssh.Client, remoteProjPath string, deploymentMode string) {
	if client == nil {
		return
	}
	fmt.Printf("📂 Setting up remote project directory: %s\n", remoteProjPath)
	client.RunCommand(fmt.Sprintf("sudo mkdir -p %s && sudo chown $USER:$USER %s", remoteProjPath, remoteProjPath), nil, nil)

	if strings.HasPrefix(deploymentMode, "git") {
		// Ensure git is installed
		if err := client.RunCommand("git --version", nil, nil); err != nil {
			fmt.Println("📦 Installing git on remote server...")
			client.RunCommand("sudo yum install -y git || sudo apt-get install -y git", os.Stdout, os.Stderr)
		}

		// Remote git init
		fmt.Println("🔧 Initializing git repository on server...")
		// The actual remote add origin should be done by the caller who has access to local git
	}
}
// InitProjectWorkflow handles the initial sequence of project setup steps
func InitProjectWorkflow(reader *bufio.Reader, force bool, gCfg *config.GlobalConfig) (projName string, srv *config.ServerConfig, err error) {
	// Directory Check
	if err = CheckLocalDirectory(reader); err != nil {
		return "", nil, fmt.Errorf("directory check failed: %w", err)
	}

	// Server Selection
	srv, err = SelectOrAddServer(reader, gCfg, prompt.PromptNewServer)
	if err != nil {
		return "", nil, fmt.Errorf("server selection failed: %w", err)
	}

	projName = DefineProjectName(reader)

	// Local Conflict Check
	if !LocalConfigCheck(reader, gCfg, projName, force) {
		return "", nil, fmt.Errorf("local conflict check failed")
	}

	return projName, srv, nil
}

// InitRemoteWorkflow handles checking remote conflicts and gathering configuration
func InitRemoteWorkflow(reader *bufio.Reader, client *ssh.Client, srv *config.ServerConfig, projFull string, force bool) (remoteProjects map[string]interface{}, domain string, versionToKeep int, err error) {
	remoteProjects, err = RemoteConflictCheck(reader, srv, projFull, force, client)
	if err != nil {
		return nil, "", 0, err
	}

	domain = prompt.PromptDomain(reader, "")
	versionToKeep = prompt.PromptRollback(reader)

	// Update remote entry if available
	if remoteProjects != nil && versionToKeep > 0 {
		if entry, ok := remoteProjects[projFull].(map[string]interface{}); ok {
			entry["rollback_backups"] = versionToKeep
			remoteProjects[projFull] = entry
		}
	}

	return remoteProjects, domain, versionToKeep, nil
}

// InitDeploymentWorkflow handles deployment mode selection and optional hook installation
func InitDeploymentWorkflow(reader *bufio.Reader, client *ssh.Client, gCfg *config.GlobalConfig, srv *config.ServerConfig, currentHookURL string) (deploymentMode, gitBranch string, err error) {
	deploymentMode, gitBranch = prompt.SetupDeploymentMode(reader)

	// Graft-Hook detection and deployment for automated modes
	if deploymentMode == "git-images" || deploymentMode == "git-repo-serverbuild" || deploymentMode == "git-manual" {
		err = webhook.InstallHook(client, gCfg, deploymentMode, reader, currentHookURL, srv)
		if err != nil {
			return "", "", fmt.Errorf("could not install hook: %w", err)
		}
	}

	return deploymentMode, gitBranch, nil
}

// InitGitRemoteWorkflow ensures the remote project directory has git initialized if needed
func InitGitRemoteWorkflow(client *ssh.Client, remoteProjPath string, deploymentMode string) error {
	if !strings.HasPrefix(deploymentMode, "git") {
		return nil
	}

	// Get remote origin
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, _ := cmd.Output()
	gitRemote := strings.TrimSpace(string(out))

	// Init git repo on server
	return client.RunCommand(fmt.Sprintf("cd %s && git init && git remote add origin %s", remoteProjPath, gitRemote), os.Stdout, os.Stderr)
}

// InitSaveMetadata generates boilerplate and returns the project metadata to be saved
func InitSaveMetadata(projName, domain, deploymentMode, gitBranch, currentHookURL, remoteProjPath string, versionToKeep int, srv *config.ServerConfig) *config.ProjectMetadata {
	// Generate boilerplate
	p := deploy.GenerateBoilerplate(projName, domain, deploymentMode)
	p.Save(".")

	// Return new metadata
	return &config.ProjectMetadata{
		Name:            projName,
		RemotePath:      remoteProjPath,
		Domain:          domain,
		DeploymentMode:  deploymentMode,
		GitBranch:       gitBranch,
		GraftHookURL:    currentHookURL,
		RollbackBackups: versionToKeep,
		Registry:        srv.RegistryName,
	}
}
