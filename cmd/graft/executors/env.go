package executors

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/deploy"
	"github.com/skssmd/graft/internal/server/git"
	"github.com/skssmd/graft/internal/server/project"
	"github.com/skssmd/graft/internal/server/prompt"
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
	gCfg := e.GlobalConfig
	srv, err := project.SelectOrAddServer(reader,gCfg)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	e.Server = srv

	projFull := fmt.Sprintf("%s-%s", projName, name)
	var versionToKeep int
	var remoteProjects map[string]interface{}

	// 3. Remote Conflict & Host Check
	client,err:=e.getClient()
	if err != nil {
		fmt.Println("Error: Could not get client.")
		return
	}
	defer client.Close()
	remoteProjects, err = project.RemoteConflictCheck(reader, srv, projFull, false, client)
	if err != nil {
		return
	}


	// 4. Prompts
	domain := prompt.PromptDomain(reader, "")
	versionToKeep = prompt.PromptRollback(reader)

	var gitBranch string
	if strings.HasPrefix(deploymentMode, "git") {
		branch, err := prompt.PromptGitBranch(reader)
		if err == nil {
			gitBranch = branch
			fmt.Printf("✅ Selected branch: %s\n", gitBranch)
		}
	}


	// 5. Update remote registry
	project.UpdateRemoteRegistry(client, remoteProjects)

	// 6. Setup remote directory
	remoteProjPath := fmt.Sprintf("/opt/graft/projects/%s", projFull)
	project.SetupRemoteProjectDirectory(client, remoteProjPath, deploymentMode)


	// 7. Save Project Metadata
	meta := &config.ProjectMetadata{
		Name:            projName,
		RemotePath:      remoteProjPath,
		Domain:          domain,
		DeploymentMode:  deploymentMode,
		GitBranch:       gitBranch,
		RollbackBackups: versionToKeep,
		Registry:        srv.RegistryName,
		Initialized:     false,
	}

	// Fetch Hook URL if available
	if srv, exists := gCfg.Servers[srv.RegistryName]; exists {
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

