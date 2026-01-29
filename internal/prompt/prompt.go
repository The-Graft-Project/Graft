package prompt

import (
	"bufio"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// PromptDomain prompts the user for a domain name with an optional default value
func PromptDomain(reader *bufio.Reader, defaultDomain string) string {
	promptText := "Domain (e.g. app.example.com): "
	if defaultDomain != "" {
		promptText = fmt.Sprintf("Domain [%s]: ", defaultDomain)
	}
	fmt.Print(promptText)
	domain, _ := reader.ReadString('\n')
	domain = strings.TrimSpace(domain)
	if domain == "" && defaultDomain != "" {
		return defaultDomain
	}
	return domain
}

// PromptRollback prompts the user for rollback configuration
func PromptRollback(reader *bufio.Reader) int {
	fmt.Println("\n🔄 Rollback configurations")
	fmt.Print("Do you want to setup rollback configurations? (y/N): ")
	input, _ := reader.ReadString('\n')
	input = strings.ToLower(strings.TrimSpace(input))

	if input == "y" || input == "yes" {
		fmt.Print("Enter the number of versions to keep: ")
		rollInput, _ := reader.ReadString('\n')
		versionToKeep, err := strconv.Atoi(strings.TrimSpace(rollInput))
		if err == nil && versionToKeep > 0 {
			fmt.Printf("✅ Rollback configured to keep %d versions\n", versionToKeep)
			return versionToKeep
		}
	}
	fmt.Println("⏭️  Skipping rollback configurations")
	return 0
}

// PromptGitBranch prompts the user to select a git branch
func PromptGitBranch(reader *bufio.Reader) (string, error) {
	// Get remote origin
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no git remote 'origin' found. Git modes require a remote 'origin' for deployment")
	}
	gitRemote := strings.TrimSpace(string(out))
	fmt.Printf("✅ Found git remote: %s\n", gitRemote)

	// Get all branches
	branchCmd := exec.Command("git", "branch", "-a", "--format=%(refname:short)")
	branchOut, err := branchCmd.Output()
	if err != nil {
		fmt.Printf("⚠️  Warning: Could not fetch git branches: %v. Defaulting to 'main'\n", err)
		return "main", nil
	}

	lines := strings.Split(string(branchOut), "\n")
	var branches []string
	seen := make(map[string]bool)
	for _, line := range lines {
		branch := strings.TrimSpace(line)
		if branch == "" || strings.Contains(branch, "HEAD") {
			continue
		}
		// Clean up remote prefixes
		cleanBranch := branch
		if strings.HasPrefix(cleanBranch, "remotes/origin/") {
			cleanBranch = strings.TrimPrefix(cleanBranch, "remotes/origin/")
		} else if strings.HasPrefix(cleanBranch, "origin/") {
			cleanBranch = strings.TrimPrefix(cleanBranch, "origin/")
		}

		if !seen[cleanBranch] {
			branches = append(branches, cleanBranch)
			seen[cleanBranch] = true
		}
	}

	if len(branches) == 0 {
		return "main", nil
	}

	fmt.Println("\n🌿 Available branches:")
	for i, b := range branches {
		fmt.Printf("  [%d] %s\n", i+1, b)
	}
	fmt.Printf("\nSelect branch [1-%d] (default: 1): ", len(branches))
	bInput, _ := reader.ReadString('\n')
	bInput = strings.TrimSpace(bInput)
	if bInput == "" {
		return branches[0], nil
	}

	idx, err := strconv.Atoi(bInput)
	if err != nil || idx < 1 || idx > len(branches) {
		fmt.Printf("⚠️  Invalid selection, using default: %s\n", branches[0])
		return branches[0], nil
	}

	return branches[idx-1], nil
}

// PromptNewServer prompts the user for new server connection details
func PromptNewServer(reader *bufio.Reader) (string, int, string, string) {
	fmt.Print("Host IP: ")
	host, _ := reader.ReadString('\n')
	host = strings.TrimSpace(host)

	fmt.Print("Port (22): ")
	portStr, _ := reader.ReadString('\n')
	port, _ := strconv.Atoi(strings.TrimSpace(portStr))
	if port == 0 {
		port = 22
	}

	fmt.Print("User: ")
	user, _ := reader.ReadString('\n')
	user = strings.TrimSpace(user)

	fmt.Print("Key Path: ")
	keyPath, _ := reader.ReadString('\n')
	keyPath = strings.TrimSpace(keyPath)
	//if key path starts with ./ then dow pwd and marge to find absolute keypath
	keyPath, err := filepath.Abs(keyPath)
	if err != nil {
		fmt.Println("Error getting absolute path:", err)
		return host, port, user, keyPath
	}

	return host, port, user, keyPath
}

// SetupDeploymentMode prompts the user to select a deployment mode and git branch
func SetupDeploymentMode(reader *bufio.Reader) (string, string) {
	var gitBranch string
	var deploymentMode string
	for {
		fmt.Println("\n📦 Project Type / Deployment Mode:")
		fmt.Println("  Git-based modes:")
		fmt.Println("    [1] git-images (GitHub Actions → GHCR → automated deployment via graft-hook)")
		fmt.Println("    [2] git-repo-serverbuild (GitHub Actions → server build → automated deployment)")
		fmt.Println("    [3] git-manual (Git repo only, no CI/CD workflow provided)")
		fmt.Println("\n  Direct deployment modes:")
		fmt.Println("    [4] direct-serverbuild (upload source → build on server)")
		fmt.Print("\nSelect deployment mode [1-4]: ")

		modeInput, _ := reader.ReadString('\n')
		modeInput = strings.TrimSpace(modeInput)

		switch modeInput {
		case "1":
			deploymentMode = "git-images"
		case "2":
			deploymentMode = "git-repo-serverbuild"
		case "3":
			deploymentMode = "git-manual"
		case "4":
			deploymentMode = "direct-serverbuild"
		default:
			fmt.Println("Invalid selection, defaulting to direct-serverbuild")
			deploymentMode = "direct-serverbuild"
		}

		// Git validation for git modes
		if strings.HasPrefix(deploymentMode, "git") {
			branch, err := PromptGitBranch(reader)
			if err != nil {
				fmt.Printf("\n❌ %v\n", err)
				continue
			}
			gitBranch = branch
			fmt.Printf("✅ Selected branch: %s\n", gitBranch)
		}
		break
	}

	switch deploymentMode {
	case "git-images":
		fmt.Println("\n✅ Git-based image deployment selected (GHCR)")
		fmt.Println("\n📦 This mode uses GitHub Actions to build images and push to GHCR.")
		fmt.Println("\n⚠️  IMPORTANT: Requires graft-hook webhook service for automated deployment")
	case "git-repo-serverbuild":
		fmt.Println("\n✅ Git-based server build deployment selected")
		fmt.Println("\n📦 This mode uses GitHub Actions to trigger server-side builds.")
	case "git-manual":
		fmt.Println("\n✅ Git manual deployment selected")
		fmt.Println("\n📦 This mode sets up the server for Git-based deployment without CI/CD.")
	case "direct-serverbuild":
		fmt.Println("\n✅ Direct server build mode selected")
	case "direct-localbuild":
		fmt.Println("\n✅ Direct local build mode selected")
	}
	return deploymentMode, gitBranch
}
