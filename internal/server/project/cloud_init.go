package project

import (
	"fmt"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/deploy"
)

// InitCloudWorkflow handles cloud mode initialization
func InitCloudWorkflow(projName, env string) (*config.ProjectMetadata, error) {
	fmt.Println("\n☁️  Cloud Mode Selected")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("Platform-native deployment (no server setup required)")
	fmt.Println("")
	fmt.Println("💡 Configure deployment in graft-compose.yml:")
	fmt.Println("   - Use 'deploy_on: flyio' or 'deploy_on: vercel' labels per service")
	fmt.Println("   - Domain is optional (platforms provide subdomains)")

	// Prepare full project name
	projFull := projName
	if !strings.HasSuffix(projFull, "-"+env) {
		projFull = fmt.Sprintf("%s-%s", projFull, env)
	}

	// Generate graft-compose.yml for cloud (no provider/domain prompts)
	p := deploy.GenerateCloudBoilerplate(projName, "", "cloud")
	if err := p.Save("."); err != nil {
		return nil, fmt.Errorf("failed to save compose file: %w", err)
	}

	// Create metadata - minimal for cloud mode
	meta := &config.ProjectMetadata{
		Name:           projName,
		Mode:           "cloud",
		Initialized:    true,
		DeploymentMode: "cloud",
	}

	fmt.Println("\n✅ Cloud project initialized!")
	fmt.Println("\n📝 Next steps:")
	fmt.Println("   1. Edit graft-compose.yml and add 'deploy_on' labels to services")
	fmt.Println("   2. Optionally set domain in compose file")
	fmt.Println("   3. Run 'graft sync' to deploy")

	return meta, nil
}
