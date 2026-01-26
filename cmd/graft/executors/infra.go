package executors

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/hostinit"
	"github.com/skssmd/graft/internal/infra"
)

func (e *Executor) RunInfraInit(typ, name string) {
	name = config.NormalizeProjectName(name)
	if name == "" {
		fmt.Printf("Error: Invalid %s name. Use only letters, numbers, and underscores.\n", typ)
		return
	}

	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	var url string
	if typ == "postgres" {
		url, err = infra.InitPostgres(client, name, os.Stdout, os.Stderr)
	} else {
		url, err = infra.InitRedis(client, name, os.Stdout, os.Stderr)
	}

	if err != nil {
		fmt.Printf("Error initializing %s: %v\n", typ, err)
		return
	}

	secretKey := fmt.Sprintf("GRAFT_%s_%s_URL", strings.ToUpper(typ), strings.ToUpper(name))
	if err := config.SaveSecret(secretKey, url); err != nil {
		fmt.Printf("Warning: Could not save secret locally: %v\n", err)
	}

	fmt.Printf("\n✅ %s '%s' initialized!\n", typ, name)
	fmt.Printf("Secret saved at ./graft/secrets.env")

}

func (e *Executor) RunInfra(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: graft infra [db|redis] ports:<value>")
		fmt.Println("       graft infra db backup")
		fmt.Println("       graft infra reload")
		return
	}

	typ := args[0]
	if typ != "db" && typ != "redis" {
		fmt.Println("Error: First argument must be 'db' or 'redis'")
		return
	}

	// Handle backup subcommand
	if typ == "db" && len(args) > 1 && args[1] == "backup" {
		if e.Server.Host == "" {
			fmt.Println("Error: No server configuration found.")
			return
		}

		client, err := e.getClient()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			return
		}
		defer client.Close()

		if err := infra.SetupDBBackup(client, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("Error setting up database backup: %v\n", err)
		}
		return
	}

	var portVal string
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "ports:") {
			portVal = strings.TrimPrefix(arg, "ports:")
			break
		}
	}

	if portVal == "" {
		fmt.Println("Usage: graft infra [db|redis] ports:<value> (use 'ports:null' to hide)")
		fmt.Println("       graft infra db backup")
		return
	}

	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// Fetch infra config from remote server
	tmpFile := filepath.Join(os.TempDir(), "infra_config.json")
	defer os.Remove(tmpFile)
	
	if err := client.DownloadFile(config.RemoteInfraPath, tmpFile); err != nil {
		fmt.Println("Error: Could not fetch infra config from remote server.")
		fmt.Println("Make sure infrastructure has been initialized with 'graft host init'")
		return
	}

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		fmt.Printf("Error reading infra config: %v\n", err)
		return
	}

	var infraCfg config.InfraConfig
	if err := json.Unmarshal(data, &infraCfg); err != nil {
		fmt.Printf("Error parsing infra config: %v\n", err)
		return
	}

	// Update port in config
	if typ == "db" {
		infraCfg.PostgresPort = portVal
	} else {
		infraCfg.RedisPort = portVal
	}

	// Re-run infra setup
	fmt.Printf("🔄 Updating %s port to: %s\n", typ, portVal)

	setupPG := infraCfg.PostgresUser != ""
	setupRedis := true // Assume redis exists if we are here, or based on previous host init

	// We need to know if redis was setup. Usually both are.
	// For now, assume both if they have been initialized.

	err = hostinit.SetupInfra(client, setupPG, setupRedis, infraCfg, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Printf("Error updating infrastructure: %v\n", err)
		return
	}

	fmt.Println("\n✅ Infrastructure updated successfully!")
}

func (e *Executor) RunInfraReload() {
	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("🔄 Reloading infrastructure (pulling latest images)...")

	// Use docker compose up -d --pull always to pull and reload
	reloadCmd := "cd /opt/graft/infra && sudo docker compose up -d --pull always"
	if err := client.RunCommand(reloadCmd, os.Stdout, os.Stderr); err != nil {
		fmt.Printf("Error reloading infrastructure: %v\n", err)
		return
	}

	fmt.Println("\n✅ Infrastructure reloaded successfully!")
}
