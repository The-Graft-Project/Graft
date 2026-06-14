package executors

import (
	"fmt"
	"os"

	"github.com/skssmd/graft/internal/server/deploy"
)

func (e *Executor) RunScale(service string, n int) {
	if n < 1 {
		fmt.Println("Error: replica count must be at least 1 (use 1 to remove replicas)")
		return
	}

	meta, err := e.getProjectMeta()
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	if meta.Scale == nil {
		meta.Scale = make(map[string]int)
	}

	if n == 1 {
		delete(meta.Scale, service)
	} else {
		meta.Scale[service] = n
	}

	if err := e.saveProjectMeta(meta); err != nil {
		fmt.Printf("Error saving scale config: %v\n", err)
		return
	}

	if _, err := os.Stat("graft-compose.yml"); err != nil {
		fmt.Println("Error: graft-compose.yml not found.")
		return
	}

	p, err := deploy.LoadProject(e.Env, "graft-compose.yml")
	if err != nil {
		fmt.Printf("Error loading project: %v\n", err)
		return
	}
	p.DeploymentMode = meta.DeploymentMode

	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	if n == 1 {
		fmt.Printf("⚖️  Scaling '%s' back to 1 replica (removing extras)...\n", service)
	} else {
		fmt.Printf("⚖️  Scaling '%s' to %d replicas...\n", service, n)
	}

	if err := deploy.SyncComposeOnly(e.Env, client, p, false, os.Stdout, os.Stderr, true, false); err != nil {
		fmt.Printf("Error applying scale: %v\n", err)
		return
	}

	if n == 1 {
		fmt.Printf("✅ '%s' scaled back to 1 replica.\n", service)
	} else {
		fmt.Printf("✅ '%s' now running %d replicas — Traefik is load balancing across them.\n", service, n)
	}
}
