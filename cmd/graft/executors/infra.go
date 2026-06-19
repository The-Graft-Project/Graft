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
	"github.com/skssmd/graft/internal/server/hostinit"
	"github.com/skssmd/graft/internal/server/infra"
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

	// Check if infra is initialized on the remote server
	tmpCheck := filepath.Join(os.TempDir(), "infra_check.config")
	if err := client.DownloadFile(config.RemoteInfraPath, tmpCheck); err != nil {
		os.Remove(tmpCheck)
		fmt.Printf("⚠️  Infrastructure (%s) is not initialized on this server.\n", typ)
		fmt.Print("Run 'graft host init' to set up infrastructure first? (y/n): ")
		reader := bufio.NewReader(os.Stdin)
		confirm, _ := reader.ReadString('\n')
		confirm = strings.ToLower(strings.TrimSpace(confirm))
		if confirm == "y" || confirm == "yes" {
			e.RunHostInit()
			// Re-connect after host init since client was closed
			client, err = e.getClient()
			if err != nil {
				fmt.Printf("Error reconnecting: %v\n", err)
				return
			}
			defer client.Close()
		} else {
			fmt.Println("Aborted. Run 'graft host init' before creating databases.")
			return
		}
	} else {
		os.Remove(tmpCheck)
	}

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
	fmt.Printf("Secret saved at ./graft/secrets.env\n")

	// Offer to link database to a project environment
	if typ == "postgres" {
		pEnv, err := config.LoadProjectEnv()
		if err != nil || pEnv == nil || len(pEnv.Env) == 0 {
			return
		}

		reader := bufio.NewReader(os.Stdin)
		fmt.Print("\n📂 Link this database to a project environment? (y/n): ")
		confirm, _ := reader.ReadString('\n')
		confirm = strings.ToLower(strings.TrimSpace(confirm))
		if confirm != "y" && confirm != "yes" {
			return
		}

		targetEnv := e.Env
		if len(pEnv.Env) > 1 {
			fmt.Println("📋 Which environment?")
			var envNames []string
			for eName := range pEnv.Env {
				envNames = append(envNames, eName)
			}
			for i, eName := range envNames {
				fmt.Printf("  [%d] %s\n", i+1, eName)
			}
			fmt.Print("Select environment: ")
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(input)

			idx, err := strconv.Atoi(input)
			if err != nil || idx < 1 || idx > len(envNames) {
				fmt.Println("Invalid selection. Database not linked.")
				return
			}
			targetEnv = envNames[idx-1]
		}

		meta, err := config.LoadProjectMetadata(targetEnv)
		if err != nil || meta == nil {
			return
		}

		if meta.Database != "" && meta.Database != name {
			fmt.Printf("⚠️  Environment '%s' already has database '%s' linked.\n", targetEnv, meta.Database)
			fmt.Print("Overwrite? (y/n): ")
			overwrite, _ := reader.ReadString('\n')
			overwrite = strings.ToLower(strings.TrimSpace(overwrite))
			if overwrite != "y" && overwrite != "yes" {
				fmt.Println("Database not linked.")
				return
			}
		}

		meta.Database = name
		if err := config.SaveProjectMetadata(targetEnv, meta); err != nil {
			fmt.Printf("Warning: Could not save database to project metadata: %v\n", err)
		} else {
			fmt.Printf("📂 Database '%s' linked to environment '%s'.\n", name, targetEnv)
		}
	}
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

// RunDbServe opens an SSH tunnel from localhost:localPort to the remote graft-postgres:5432.
// dbOverride: when non-empty, uses that db name. When empty, uses master db.
func (e *Executor) RunDbServe(dbOverride string, localPort int) {
	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// Fetch infra credentials to verify postgres is configured
	tmpFile := filepath.Join(os.TempDir(), "serve_infra.config")
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

	if infraCfg.PostgresUser == "" {
		fmt.Println("Error: Postgres is not configured on this host. Run 'graft host init' to set it up.")
		return
	}

	dbname := dbOverride
	if dbname == "" {
		dbname = infraCfg.PostgresDB
	}

	// Try to find dedicated credentials from local secrets
	secretsHint := ""
	secrets, _ := config.LoadSecrets()
	secretKey := fmt.Sprintf("GRAFT_POSTGRES_%s_URL", strings.ToUpper(dbname))
	if url, ok := secrets[secretKey]; ok {
		secretsHint = fmt.Sprintf("   %s (from .graft/secrets.env)\n", strings.Replace(url, "graft-postgres:5432", fmt.Sprintf("localhost:%d", localPort), 1))
	}

	// Get the container IP — port may not be mapped to host
	containerIP, err := client.GetCommandOutput("sudo docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' graft-postgres")
	if err != nil || strings.TrimSpace(containerIP) == "" {
		fmt.Println("Error: Could not find graft-postgres container. Is it running?")
		fmt.Println("Try: graft host init")
		return
	}
	containerIP = strings.TrimSpace(containerIP)

	fmt.Printf("🔗 Tunneling remote postgres to localhost:%d...\n", localPort)
	fmt.Printf("\n📋 Connection details:\n")
	fmt.Printf("   Host:     localhost\n")
	fmt.Printf("   Port:     %d\n", localPort)
	fmt.Printf("   Database: %s\n", dbname)
	if secretsHint != "" {
		fmt.Printf("\n📋 Connection string:\n")
		fmt.Print(secretsHint)
	} else {
		fmt.Printf("\n💡 Credentials are in .graft/secrets.env (key: %s)\n", secretKey)
	}
	fmt.Println("\nPress Ctrl+C to stop the tunnel.")

	if err := client.Tunnel(localPort, containerIP, 5432); err != nil {
		fmt.Printf("\nTunnel error: %v\n", err)
	}
}

// RunPsql opens a psql session on the infra postgres.
// dbOverride: when non-empty, used as the database name and all args are treated as psql flags.
// When empty (registry scope), the first non-flag arg is parsed as dbname, falling back to master.
func (e *Executor) RunPsql(dbOverride string, args []string) {
	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	// Fetch infra credentials from remote server
	tmpFile := filepath.Join(os.TempDir(), "psql_infra.config")
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

	if infraCfg.PostgresUser == "" {
		fmt.Println("Error: Postgres is not configured on this host. Run 'graft host init' to set it up.")
		return
	}

	var dbname string
	var psqlFlags []string
	interactive := true

	if dbOverride != "" {
		// Project scope — db comes from env metadata, all args are psql flags
		dbname = dbOverride
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" || args[i] == "-f" {
				interactive = false
				psqlFlags = append(psqlFlags, args[i])
				if i+1 < len(args) {
					i++
					psqlFlags = append(psqlFlags, "'"+strings.ReplaceAll(args[i], "'", "'\\''")+"'")
				}
			} else {
				psqlFlags = append(psqlFlags, args[i])
			}
		}
	} else {
		// Registry scope — first non-flag arg is dbname
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" || args[i] == "-f" {
				interactive = false
				psqlFlags = append(psqlFlags, args[i])
				if i+1 < len(args) {
					i++
					psqlFlags = append(psqlFlags, "'"+strings.ReplaceAll(args[i], "'", "'\\''")+"'")
				}
			} else if strings.HasPrefix(args[i], "-") {
				psqlFlags = append(psqlFlags, args[i])
			} else if dbname == "" {
				dbname = args[i]
			} else {
				psqlFlags = append(psqlFlags, args[i])
			}
		}
		if dbname == "" {
			dbname = infraCfg.PostgresDB
		}
	}

	psqlCmd := fmt.Sprintf("psql -U %s -d %s", infraCfg.PostgresUser, dbname)
	if len(psqlFlags) > 0 {
		psqlCmd += " " + strings.Join(psqlFlags, " ")
	}

	if interactive {
		fmt.Printf("🐘 Connecting to postgres database '%s' on %s...\n", dbname, e.Server.Host)
		cmd := fmt.Sprintf("sudo docker exec -it graft-postgres %s", psqlCmd)
		if err := client.RunInteractiveCommand(cmd); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	} else {
		fmt.Printf("🐘 Running on '%s' (%s):\n", dbname, e.Server.Host)
		cmd := fmt.Sprintf("sudo docker exec graft-postgres %s", psqlCmd)
		if err := client.RunCommand(cmd, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("Error: %v\n", err)
		}
	}
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
