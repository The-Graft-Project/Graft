package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/deploy"
)

func runMode() {
	reader := bufio.NewReader(os.Stdin)
	
	// Load project metadata
	meta, err := config.LoadProjectMetadata()
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

	// Update project metadata
	meta.DeploymentMode = newMode
	meta.Initialized = false // Reset to false when mode changes
	if err := config.SaveProjectMetadata(meta); err != nil {
		fmt.Printf("Error: Could not save project metadata: %v\n", err)
		return
	}

	// Regenerate compose file with new mode
	fmt.Println("\n🔄 Regenerating graft-compose.yml with new deployment mode...")
	
	// Load existing compose to get project name and domain
	p, err := deploy.LoadProject("graft-compose.yml")
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
