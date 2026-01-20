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
	"github.com/skssmd/graft/internal/ssh"
)

func (e *Executor) RunRollback() {
	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	if meta.RollbackBackups <= 0 {
		fmt.Println("❌ Rollback is not configured for this project. Setup rollbacks during 'graft init' or update your project configuration with 'graft rollback config'.")
		return
	}

	cfg := e

	fmt.Printf("🔍 Connecting to %s (%s)...\n", cfg.Server.RegistryName, cfg.Server.Host)
	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	backupBase := fmt.Sprintf("/opt/graft/backup/%s", meta.Name)
	// List directories in backup path, newest first
	out, err := client.GetCommandOutput(fmt.Sprintf("sudo ls -1dt %s/* 2>/dev/null", backupBase))
	if err != nil || strings.TrimSpace(out) == "" {
		fmt.Println("❌ No backups found on server.")
		return
	}

	backups := strings.Split(strings.TrimSpace(out), "\n")
	fmt.Println("\n📦 Available Backups (Newest First):")
	var choices []string
	for i, p := range backups {
		timestamp := filepath.Base(p)
		formatted := formatTimestamp(timestamp)
		fmt.Printf("  [%d] %s\n", i+1, formatted)
		choices = append(choices, timestamp)
	}

	fmt.Printf("\nSelect a version to rollback to [1-%d] (or enter to cancel): ", len(choices))
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		fmt.Println("❌ Rollback cancelled.")
		return
	}

	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(choices) {
		fmt.Println("❌ Invalid selection.")
		return
	}

	selected := choices[choice-1]

	p := &deploy.Project{
		Name:            meta.Name,
		RollbackBackups: meta.RollbackBackups,
	}

	if err := deploy.RestoreRollback(client, p, selected, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("❌ Rollback failed: %v\n", err)
	} else {
		fmt.Println("\n✅ Rollback successful!")
	}
}

func (e *Executor) RunRollbackConfig() {
	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	cfg := e

	fmt.Printf("🔄 Rollback Configuration for project: %s\n", meta.Name)
	fmt.Printf("Current versions to keep: %d\n", meta.RollbackBackups)

	reader := bufio.NewReader(os.Stdin)
	fmt.Print("\nDo you want to change or remove rollback configuration? (y: change, n: remove, enter: skip): ")
	input, _ := reader.ReadString('\n')
	input = strings.ToLower(strings.TrimSpace(input))

	var newVersionToKeep int
	var action string

	if input == "y" || input == "yes" {
		fmt.Print("Enter the new number of versions to keep: ")
		rollInput, _ := reader.ReadString('\n')
		newVersionToKeep, err = strconv.Atoi(strings.TrimSpace(rollInput))
		if err != nil || newVersionToKeep < 0 {
			fmt.Println("❌ Invalid input. Number must be 0 or greater.")
			return
		}
		action = "updated"
	} else if input == "n" || input == "no" {
		newVersionToKeep = 0
		action = "removed"
	} else {
		fmt.Println("⏭️  Skipping configuration.")
		return
	}

	fmt.Printf("🔍 Connecting to %s (%s) to update remote configuration...\n", cfg.Server.RegistryName, cfg.Server.Host)
	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// 1. Update remote projects registry
	tmpFile := filepath.Join(os.TempDir(), "remote_projects.json")
	remoteProjects := make(map[string]interface{})
	if err := client.DownloadFile(config.RemoteProjectsPath, tmpFile); err == nil {
		data, _ := os.ReadFile(tmpFile)
		json.Unmarshal(data, &remoteProjects)
		os.Remove(tmpFile)
	}

	if entry, exists := remoteProjects[meta.Name]; exists {
		if m, ok := entry.(map[string]interface{}); ok {
			if newVersionToKeep > 0 {
				m["rollback_backups"] = newVersionToKeep
			} else {
				delete(m, "rollback_backups")
			}
			remoteProjects[meta.Name] = m
		}
	} else {
		// If it doesn't exist for some reason, create it
		remoteProjects[meta.Name] = map[string]interface{}{
			"path":             meta.RemotePath,
			"rollback_backups": newVersionToKeep,
		}
	}

	data, _ := json.MarshalIndent(remoteProjects, "", "  ")
	os.WriteFile(tmpFile, data, 0644)
	if err := client.UploadFile(tmpFile, config.RemoteProjectsPath); err != nil {
		fmt.Printf("❌ Failed to update remote registry: %v\n", err)
		return
	}
	os.Remove(tmpFile)

	// 2. Update local metadata
	meta.RollbackBackups = newVersionToKeep
	if err := config.SaveProjectMetadata(e.Env, meta); err != nil {
		fmt.Printf("⚠️  Warning: Could not save local metadata: %v\n", err)
	}

	fmt.Printf("✅ Rollback configuration %s successfully.\n", action)

	// 3. Restart Webhook
	fmt.Println("🔄 Restarting graft-hook to apply changes...")
	if err := client.RunCommand("cd /opt/graft/webhook && sudo docker compose down && sudo docker compose up -d", os.Stdout, os.Stderr); err != nil {
		fmt.Printf("⚠️  Warning: Failed to restart graft-hook: %v\n", err)
	} else {
		fmt.Println("✅ graft-hook restarted successfully.")
	}
}

func (e *Executor) RunServiceRollback(serviceName string) {
	meta, err := config.LoadProjectMetadata(e.Env)
	if err != nil {
		fmt.Println("Error: Could not load project metadata. Run 'graft init' first.")
		return
	}

	if meta.RollbackBackups <= 0 {
		fmt.Println("❌ Rollback is not configured for this project. Setup rollbacks during 'graft init' or update your project configuration with 'graft rollback config'.")
		return
	}

	cfg := e

	fmt.Printf("🔍 Connecting to %s (%s)...\n", cfg.Server.RegistryName, cfg.Server.Host)
	client, err := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	backupBase := fmt.Sprintf("/opt/graft/backup/%s", meta.Name)
	// List directories in backup path, newest first
	out, err := client.GetCommandOutput(fmt.Sprintf("sudo ls -1dt %s/* 2>/dev/null", backupBase))
	if err != nil || strings.TrimSpace(out) == "" {
		fmt.Println("❌ No backups found on server.")
		return
	}

	backups := strings.Split(strings.TrimSpace(out), "\n")
	fmt.Println("\n📦 Available Backups (Newest First):")
	var choices []string
	for i, p := range backups {
		timestamp := filepath.Base(p)
		formatted := formatTimestamp(timestamp)
		fmt.Printf("  [%d] %s\n", i+1, formatted)
		choices = append(choices, timestamp)
	}

	fmt.Printf("\nSelect a version to rollback service '%s' to [1-%d] (or enter to cancel): ", serviceName, len(choices))
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input == "" {
		fmt.Println("❌ Rollback cancelled.")
		return
	}

	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(choices) {
		fmt.Println("❌ Invalid selection.")
		return
	}

	selected := choices[choice-1]

	p := &deploy.Project{
		Name:            meta.Name,
		RollbackBackups: meta.RollbackBackups,
	}

	if err := deploy.RestoreServiceRollback(client, p, selected, serviceName, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("❌ Rollback failed: %v\n", err)
	} else {
		fmt.Printf("\n✅ Service '%s' rollback successful!\n", serviceName)
	}
}
