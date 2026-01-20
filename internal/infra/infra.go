package infra

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/ssh"
)

func InitPostgres(client *ssh.Client, name string, stdout, stderr io.Writer) (string, error) {
	fmt.Fprintf(stdout, "🐘 Creating isolated Postgres database: %s\n", name)

	// If credentials missing, try to load from remote server
	fmt.Fprintln(stdout, "🔍 Credentials missing locally, fetching from remote server...")
	tmpFile := filepath.Join(os.TempDir(), "remote_infra.config")

	var pgDB, pgPass, pgUser string
	if err := client.DownloadFile(config.RemoteInfraPath, tmpFile); err == nil {
		data, _ := os.ReadFile(tmpFile)
		var infraCfg config.InfraConfig
		if err := json.Unmarshal(data, &infraCfg); err == nil {

			pgUser = infraCfg.PostgresUser
			pgPass = infraCfg.PostgresPassword
			pgDB = infraCfg.PostgresDB
			fmt.Fprintln(stdout, "✅ Credentials fetched from remote server")
		}
		os.Remove(tmpFile)
	}

	// Connect to the shared 'graft-postgres' container and create the database
	cmd := fmt.Sprintf(`sudo docker exec graft-postgres psql -U %s -d %s -c "CREATE DATABASE %s;"`, pgUser, pgDB, name)

	if err := client.RunCommand(cmd, stdout, stderr); err != nil {
		// If it fails, maybe the DB already exists, which is fine for idempotency
		fmt.Fprintf(stdout, "⚠️  Database might already exist or creation failed: %v\n", err)
	}

	// Use the container name as host since they will be in the same graft-public network
	url := fmt.Sprintf("postgres://%s:%s@graft-postgres:5432/%s", pgUser, pgPass, name)
	return url, nil
}

func InitRedis(client *ssh.Client, name string, stdout, stderr io.Writer) (string, error) {
	fmt.Fprintf(stdout, "🍦 Mapping Redis database for: %s\n", name)

	// Redis doesn't have "CREATE DATABASE" in the same way.
	// We'll map the name to a database index (1-15) using a hash.
	// Index 0 is reserved for general use.
	h := fnv.New32a()
	h.Write([]byte(name))
	dbIndex := (h.Sum32() % 15) + 1 // Ensure it's in range 1-15

	url := fmt.Sprintf("redis://graft-redis:6379/%d", dbIndex)
	return url, nil
}
