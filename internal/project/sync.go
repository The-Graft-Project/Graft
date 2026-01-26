package project

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/deploy"
	"github.com/skssmd/graft/internal/git"
	"github.com/skssmd/graft/internal/ssh"
)

// SyncArgs holds parsed sync command arguments
type SyncArgs struct {
	ServiceName string
	NoCache     bool
	Heave       bool
	UseGit      bool
	GitBranch   string
	GitCommit   string
}

// ParseSyncArgs parses command line arguments for sync command
func ParseSyncArgs(args []string) SyncArgs {
	var sa SyncArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--no-cache" {
			sa.NoCache = true
		} else if arg == "-h" || arg == "--heave" {
			sa.Heave = true
		} else if arg == "--git" {
			sa.UseGit = true
		} else if arg == "--branch" && i+1 < len(args) {
			sa.GitBranch = args[i+1]
			i++ // Skip next arg
		} else if arg == "--commit" && i+1 < len(args) {
			sa.GitCommit = args[i+1]
			i++ // Skip next arg
		} else if sa.ServiceName == "" {
			sa.ServiceName = arg
		}
	}
	return sa
}

// SyncInitializeGitProject handles first-time git project initialization
func SyncInitializeGitProject(env string, client *ssh.Client, p *deploy.Project, meta *config.ProjectMetadata, saveMetaFn func(*config.ProjectMetadata) error) error {
	fmt.Println("\n📦 Git-based project detected. Setting up CI/CD workflows...")

	remoteURL, err := git.GetRemoteURL(".", "origin")
	if err != nil {
		return fmt.Errorf("could not get git remote URL: %w", err)
	}

	// Generate Workflows
	if err := deploy.GenerateWorkflows(p, env, remoteURL, meta.DeploymentMode, meta.GraftHookURL); err != nil {
		return fmt.Errorf("error generating workflows: %w", err)
	}

	fmt.Println("✅ GitHub Workflows created in .github/workflows/")

	fmt.Println("📤 Transferring files to server...")
	if err := deploy.SyncComposeOnly(env, client, p, true, os.Stdout, os.Stderr, true, true); err != nil {
		fmt.Printf("Error syncing compose: %v\n", err)
	}

	// Update initialized status
	meta.Initialized = true
	if err := saveMetaFn(meta); err != nil {
		return fmt.Errorf("failed to save project metadata: %w", err)
	}

	fmt.Println("\n✅ Project initialized! Next steps:")
	fmt.Println("1. Review .github/workflows/ci.yml and deploy.yml")
	if meta.DeploymentMode == "git-images" {
		fmt.Println("2. Your server is set up to receive images from GHCR.")
	}
	fmt.Println("3. Run: git add . && git commit -m \"Initial Graft setup\" && git push")
	fmt.Println("\n🚀 Your project is ready to be updated with git push!")

	return nil
}

// SyncHandleInitializedProject handles sync for already initialized projects
func SyncHandleInitializedProject(env string, client *ssh.Client, p *deploy.Project, reader *bufio.Reader) error {
	fmt.Printf("\n⚠️  Project '%s' is already initialized.\n", p.Name)

	fmt.Print("❓ Do you want to re-generate and transfer the compose file? (y/n): ")
	ansCompose, _ := reader.ReadString('\n')

	fmt.Print("❓ Do you want to transfer the environment files (env/)? (y/n): ")
	ansEnv, _ := reader.ReadString('\n')

	doCompose := strings.TrimSpace(strings.ToLower(ansCompose)) == "y"
	doEnv := strings.TrimSpace(strings.ToLower(ansEnv)) == "y"

	if doCompose || doEnv {
		if err := deploy.SyncComposeOnly(env, client, p, true, os.Stdout, os.Stderr, doCompose, doEnv); err != nil {
			return fmt.Errorf("error during sync: %w", err)
		}

		fmt.Println("\n✅ Completed! Please do git push to update/restart the server through CI/CD or run native docker commands with graft.")
	}

	return nil
}

// SyncPerformDeploy performs the actual sync/deploy operation
func SyncPerformDeploy(env string, client *ssh.Client, p *deploy.Project, sa SyncArgs, stdout, stderr io.Writer) error {
	if sa.ServiceName != "" {
		fmt.Fprintf(stdout, "🎯 Syncing service: %s\n", sa.ServiceName)
		if sa.UseGit {
			fmt.Fprintln(stdout, "📦 Git mode enabled")
		}
		if sa.NoCache {
			fmt.Fprintln(stdout, "🔥 No-cache mode enabled")
		}
		if sa.Heave {
			fmt.Fprintln(stdout, "📦 Heave sync enabled (upload only)")
		}
		return deploy.SyncService(env, client, p, sa.ServiceName, sa.NoCache, sa.Heave, sa.UseGit, sa.GitBranch, sa.GitCommit, stdout, stderr)
	}

	if sa.UseGit {
		fmt.Fprintln(stdout, "📦 Git mode enabled")
	}
	if sa.NoCache {
		fmt.Fprintln(stdout, "🔥 No-cache mode enabled")
	}
	if sa.Heave {
		fmt.Fprintln(stdout, "🚀 Heave sync enabled (upload only)")
	}
	err := deploy.Sync(env, client, p, sa.NoCache, sa.Heave, sa.UseGit, sa.GitBranch, sa.GitCommit, stdout, stderr)
	if err != nil {
		return fmt.Errorf("error during sync: %w", err)
	}

	if !sa.Heave {
		fmt.Fprintln(stdout, "\n✅ Sync complete!")
	}

	return nil
}
