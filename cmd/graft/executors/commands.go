package executors

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/deploy"
	"github.com/skssmd/graft/internal/server/project"
	"github.com/skssmd/graft/internal/server/prompt"
	"github.com/skssmd/graft/internal/server/ssh"
)

func (e *Executor) RunMode() {
	reader := bufio.NewReader(os.Stdin)

	// Load project metadata
	meta, err := e.getProjectMeta()
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
		branch, err := prompt.PromptGitBranch(reader)
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
	if err := e.saveProjectMeta(meta); err != nil {
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
	for _, arg := range args {
		if arg == "-f" || arg == "--force" {
			force = true
		}
	}

	// Step 1: Project Setup
	projName, srv, err := project.InitProjectWorkflow(reader, force, e.GlobalConfig)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Get current hook URL from registry
	var currentHookURL string
	if e.GlobalConfig != nil {
		if srvCfg, exists := e.GlobalConfig.Servers[srv.RegistryName]; exists {
			currentHookURL = srvCfg.GraftHookURL
		}
	}

	// Prepare full project name
	projFull := projName
	if !strings.HasSuffix(projFull, "-"+e.Env) {
		projFull = fmt.Sprintf("%s-%s", projFull, e.Env)
	}

	// Step 2: Remote Setup
	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	remoteProjects, domain, versionToKeep, err := project.InitRemoteWorkflow(reader, client, srv, projFull, force)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Step 3: Update Remote Registry
	project.UpdateRemoteRegistry(client, remoteProjects)

	// Step 4: Deployment Setup
	deploymentMode, gitBranch, err := project.InitDeploymentWorkflow(reader, client, e.GlobalConfig, srv, currentHookURL)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Step 5: Remote Project Directory Setup
	remoteProjPath := fmt.Sprintf("/opt/graft/projects/%s", projFull)
	project.SetupRemoteProjectDirectory(client, remoteProjPath, deploymentMode)

	// Step 6: Git Remote Setup
	if err := project.InitGitRemoteWorkflow(client, remoteProjPath, deploymentMode); err != nil {
		fmt.Printf("Warning: Git remote setup failed: %v\n", err)
	}

	// Step 7: Save Metadata
	meta := project.InitSaveMetadata(projName, domain, deploymentMode, gitBranch, currentHookURL, remoteProjPath, versionToKeep, srv)
	e.ProjectMeta = meta
	if err := e.saveProjectMeta(meta); err != nil {
		fmt.Printf("Warning: Could not save project metadata: %v\n", err)
	}

	fmt.Printf("\n✨ Project '%s' initialized!\n", projName)
	fmt.Printf("Local config: .graft/config.json\n")
	fmt.Printf("Boilerplate: graft-compose.yml\n")
}


func (e *Executor) RunSync(args []string) {
	// Parse command line arguments
	sa := project.ParseSyncArgs(args)

	// Find and load project file
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

	meta, err := e.getProjectMeta()
	if err != nil {
		fmt.Println("Warning: Could not load project metadata. Run 'graft init' first.")
	} else {
		p.DeploymentMode = meta.DeploymentMode
		p.RollbackBackups = meta.RollbackBackups
	}

	reader := bufio.NewReader(os.Stdin)

	// Handle first-time git project initialization
	if !meta.Initialized && strings.HasPrefix(meta.DeploymentMode, "git") {
		client, err := e.getClient()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		defer client.Close()

		if err := project.SyncInitializeGitProject(e.Env, client, p, meta); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
		return
	}

	// Handle already initialized projects
	if meta.Initialized {
		client, err := e.getClient()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		defer client.Close()
		
		if err := project.SyncHandleInitializedProject(e.Env, client, p, reader); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
		return
	}

	// For automated git modes, don't allow manual sync
	if meta.Initialized && (meta.DeploymentMode == "git-images" || meta.DeploymentMode == "git-repo-serverbuild") {
		fmt.Printf("\nℹ️  Project '%s' is in Git-automated mode (%s).\n", p.Name, meta.DeploymentMode)
		fmt.Println("🚀 Please do 'git push' to trigger deployment via GitHub Actions and webhooks.")
		fmt.Println("💡 To force a manual sync (upload source), use a different deployment mode with 'graft mode'.")
		return
	}

	// Perform the actual sync/deploy
	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if err := project.SyncPerformDeploy(e.Env, client, p, sa, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("Error: %v\n", err)
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

	client, err := e.getClient()
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
	// Load project metadata to get remote path
	meta, err := e.getProjectMeta()
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	client, err := e.getClient()
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

	meta, err := e.getProjectMeta()
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	client, err := e.getClient()
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
	

	client, err := e.getClient()
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
	gCfg := e.GlobalConfig
	if gCfg == nil {
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



