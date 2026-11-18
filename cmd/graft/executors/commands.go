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
	var force, cloud bool
	for _, arg := range args {
		if arg == "-f" || arg == "--force" {
			force = true
		}
		if arg == "--cloud" {
			cloud = true
		}
	}

	// Step 1: Project Setup (common for both modes)
	projName, err := project.InitProjectWorkflow(reader, force, e.GlobalConfig)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	var meta *config.ProjectMetadata

	if cloud {
		// Cloud mode initialization
		meta, err = project.InitCloudWorkflow(projName, e.Env)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
	} else {
		// Server mode initialization (existing logic)
		srv, err := project.SelectOrAddServer(reader, e.GlobalConfig)
		if err != nil {
			fmt.Println("Failed to select server:", err)
			return
		}
		e.Server = srv

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
		deploymentMode, gitBranch, updatedHookURL, err := project.InitDeploymentWorkflow(reader, client, e.GlobalConfig, srv, currentHookURL)
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
		meta = project.InitSaveMetadata(projName, domain, deploymentMode, gitBranch, updatedHookURL, remoteProjPath, versionToKeep, srv)
		// Set mode to "server" for backward compatibility
		if meta.Mode == "" {
			meta.Mode = "server"
		}
	}

	// Save metadata
	e.ProjectMeta = meta
	if err := e.saveProjectMeta(meta); err != nil {
		fmt.Printf("Warning: Could not save project metadata: %v\n", err)
	}

	fmt.Printf("\n✨ Project '%s' initialized!\n", projName)
	fmt.Printf("Local config: .graft/project.json\n")
	fmt.Printf("Boilerplate: graft-compose.yml\n")
}
func (e *Executor) GetSSHPub() {
	gDir := config.GetGlobalConfigDir()
	pemPath := filepath.Join(gDir, "graftpem")
	pubPath := filepath.Join(gDir, "graftpub")

	// Check if both files exist
	_, errPem := os.Stat(pemPath)
	_, errPub := os.Stat(pubPath)

	if os.IsNotExist(errPem) || os.IsNotExist(errPub) {
		fmt.Fprintln(os.Stderr, "🔑 Generating new SSH key pair (graftpub/graftpem)...")
		if err := ssh.GenerateSSHKey(pemPath, pubPath); err != nil {
			fmt.Fprintf(os.Stderr, "❌ Error generating SSH key: %v\n", err)
			return
		}
		fmt.Fprintln(os.Stderr, "✅ Keys generated successfully in", gDir)
	}

	pubKey, err := os.ReadFile(pubPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Error reading public key: %v\n", err)
		return
	}

	// Just print the key to stdout so it can be piped
	fmt.Println(strings.TrimSpace(string(pubKey)))
}

// RolloutSSHKeys replaces the Graft SSH key on every server that uses it.
//
// It is all-or-nothing: the new key is added next to the old one and proven to
// log in on every server before anything is saved locally. If any server
// fails, the new key is withdrawn everywhere and the old key stays in use, so
// a partial rollout can never lock you out of a server.
func (e *Executor) RolloutSSHKeys() {
	gDir := config.GetGlobalConfigDir()
	pemPath := filepath.Join(gDir, "graftpem")
	pubPath := filepath.Join(gDir, "graftpub")

	// 1. Load the current key. The public half is derived from the private
	// key so it is guaranteed to be the one servers authenticate us with.
	oldPem, err := os.ReadFile(pemPath)
	if err != nil {
		fmt.Printf("❌ Error: Could not read current private key: %v\n", err)
		return
	}
	oldPub, err := ssh.PublicKeyFromPrivate(oldPem)
	if err != nil {
		fmt.Printf("❌ Error: %v\n", err)
		return
	}

	gCfg := e.GlobalConfig
	if gCfg == nil {
		fmt.Println("❌ Error: Global config not loaded.")
		return
	}

	// 2. Generate the new key in memory only.
	fmt.Println("🔄 Generating new SSH key pair...")
	newPem, newPub, err := ssh.GenerateSSHKeyPairStrings()
	if err != nil {
		fmt.Printf("❌ Error generating new keys: %v\n", err)
		return
	}

	// 3. Find servers using the Graft key.
	ourPemAbs, _ := filepath.Abs(pemPath)
	var targets []config.ServerConfig
	for _, srv := range gCfg.Servers {
		srvKeyAbs, _ := filepath.Abs(srv.KeyPath)
		if srvKeyAbs == ourPemAbs {
			targets = append(targets, srv)
		}
	}
	if len(targets) == 0 {
		fmt.Println("ℹ️  No servers found using the Graft SSH key. Only local keys will be updated.")
	} else {
		fmt.Printf("🚀 Rolling out keys to %d servers...\n", len(targets))
	}

	// 4. Authorize the new key alongside the old one and prove it logs in.
	var added []config.ServerConfig
	failed := 0
	for _, srv := range targets {
		fmt.Printf("📡 Adding new key on %s (%s)... ", srv.RegistryName, srv.Host)
		if err := addAndVerifyKey(srv, oldPub, newPub, []byte(newPem)); err != nil {
			fmt.Printf("❌ %v\n", err)
			failed++
			continue
		}
		fmt.Println("✅")
		added = append(added, srv)
	}

	if failed > 0 {
		fmt.Printf("\n⏪ %d of %d servers failed, so the rotation is cancelled.\n", failed, len(targets))
		withdrawNewKey(added, oldPub, newPub)
		fmt.Println("   Your current key is unchanged and still works. Fix the servers above and run 'graft pub rollout' again.")
		return
	}

	// 5. Every server accepts the new key: save it locally.
	fmt.Println("💾 Saving new keys locally...")
	if err := ssh.ReplaceKeyPair(pemPath, pubPath, newPem, newPub); err != nil {
		fmt.Printf("❌ Error saving keys: %v\n", err)
		withdrawNewKey(added, oldPub, newPub)
		fmt.Println("   Your current key is unchanged and still works.")
		return
	}

	// 6. Revoke the old key, connecting with the new one now on disk.
	lingering := 0
	for _, srv := range added {
		fmt.Printf("🧹 Revoking old key on %s (%s)... ", srv.RegistryName, srv.Host)
		client, err := ssh.NewClient(srv.Host, srv.Port, srv.User, srv.KeyPath)
		if err == nil {
			err = client.RemoveAuthorizedKey(oldPub, newPub)
			client.Close()
		}
		if err != nil {
			fmt.Printf("⚠️  %v\n", err)
			lingering++
			continue
		}
		fmt.Println("✅")
	}

	fmt.Printf("\n✨ Successfully rolled out SSH keys! (%d servers updated)\n", len(added))
	if lingering > 0 {
		fmt.Printf("⚠️  The old key is still authorized on %d servers. Remove this line from their ~/.ssh/authorized_keys:\n   %s\n", lingering, oldPub)
	}
}

// addAndVerifyKey authorizes newPub on srv, logging in with the current key,
// then opens a separate connection with the new key to prove it works. If that
// proof fails, the new key is withdrawn again before returning.
func addAndVerifyKey(srv config.ServerConfig, oldPub, newPub string, newPem []byte) error {
	client, err := ssh.NewClient(srv.Host, srv.Port, srv.User, srv.KeyPath)
	if err != nil {
		return fmt.Errorf("connection failed: %v", err)
	}
	defer client.Close()

	if err := client.AddAuthorizedKey(oldPub, newPub); err != nil {
		return fmt.Errorf("adding key failed: %v", err)
	}
	if err := ssh.VerifyKeyLogin(srv.Host, srv.Port, srv.User, newPem); err != nil {
		if rerr := client.RemoveAuthorizedKey(newPub, oldPub); rerr != nil {
			return fmt.Errorf("new key does not log in (%v), and withdrawing it failed: %v", err, rerr)
		}
		return fmt.Errorf("new key does not log in: %v", err)
	}
	return nil
}

// withdrawNewKey removes newPub from servers where it was added, logging in
// with the old key, which is still the one on disk.
func withdrawNewKey(servers []config.ServerConfig, oldPub, newPub string) {
	for _, srv := range servers {
		fmt.Printf("   Withdrawing new key from %s... ", srv.RegistryName)
		client, err := ssh.NewClient(srv.Host, srv.Port, srv.User, srv.KeyPath)
		if err == nil {
			err = client.RemoveAuthorizedKey(newPub, oldPub)
			client.Close()
		}
		if err != nil {
			fmt.Printf("⚠️  %v (harmless: that key was never saved)\n", err)
			continue
		}
		fmt.Println("✅")
	}
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

		if err := project.SyncInitializeGitProject(e.Env, client, p, meta, e.GlobalConfig.Servers[meta.Registry].GraftHookURL); err != nil {
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
