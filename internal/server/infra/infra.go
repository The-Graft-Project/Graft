package infra

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/ssh"
)

func InitPostgres(client *ssh.Client, name string, stdout, stderr io.Writer) (string, error) {
	fmt.Fprintf(stdout, "🐘 Creating isolated Postgres database: %s\n", name)

	// Fetch admin credentials from remote server
	fmt.Fprintln(stdout, "🔍 Fetching admin credentials from remote server...")
	tmpFile := filepath.Join(os.TempDir(), "remote_infra.config")

	var adminUser, adminDB string
	if err := client.DownloadFile(config.RemoteInfraPath, tmpFile); err == nil {
		data, _ := os.ReadFile(tmpFile)
		var infraCfg config.InfraConfig
		if err := json.Unmarshal(data, &infraCfg); err == nil {
			adminUser = infraCfg.PostgresUser
			adminDB = infraCfg.PostgresDB
			fmt.Fprintln(stdout, "✅ Admin credentials fetched from remote server")
		}
		os.Remove(tmpFile)
	}

	if adminUser == "" || adminDB == "" {
		return "", fmt.Errorf("could not fetch admin credentials from remote server")
	}

	// Generate dedicated credentials for this database
	dbUser := fmt.Sprintf("%s_user", name)
	dbPass := config.GenerateRandomString(24)

	// Create dedicated user
	fmt.Fprintf(stdout, "🔐 Creating dedicated user '%s'...\n", dbUser)
	createUserCmd := fmt.Sprintf(
		`sudo docker exec graft-postgres psql -U %s -d %s -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '%s') THEN CREATE USER %s WITH PASSWORD '%s'; END IF; END \$\$;"`,
		adminUser, adminDB, dbUser, dbUser, dbPass,
	)
	if err := client.RunCommand(createUserCmd, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  User might already exist: %v\n", err)
	}

	// Create database owned by the dedicated user
	fmt.Fprintf(stdout, "🗄️  Creating database '%s'...\n", name)
	createDBCmd := fmt.Sprintf(
		`sudo docker exec graft-postgres psql -U %s -d %s -c "CREATE DATABASE %s OWNER %s;"`,
		adminUser, adminDB, name, dbUser,
	)
	if err := client.RunCommand(createDBCmd, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Database might already exist: %v\n", err)
	}

	// Grant privileges
	grantCmd := fmt.Sprintf(
		`sudo docker exec graft-postgres psql -U %s -d %s -c "GRANT ALL PRIVILEGES ON DATABASE %s TO %s;"`,
		adminUser, adminDB, name, dbUser,
	)
	if err := client.RunCommand(grantCmd, stdout, stderr); err != nil {
		fmt.Fprintf(stdout, "⚠️  Grant warning: %v\n", err)
	}

	url := fmt.Sprintf("postgres://%s:%s@graft-postgres:5432/%s", dbUser, dbPass, name)
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
