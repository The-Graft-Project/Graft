package infra

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/ssh"
)

func SetupDBBackup(client *ssh.Client, stdout, stderr io.Writer) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Fprintln(stdout, "\n☁️  S3 Backup Configuration")
	fmt.Fprintln(stdout, "----------------------------")

	s3 := &config.S3Config{}

	fmt.Fprint(stdout, "S3 Endpoint (leave empty for AWS): ")
	endpoint, _ := reader.ReadString('\n')
	s3.Endpoint = strings.TrimSpace(endpoint)

	fmt.Fprint(stdout, "S3 Region (e.g., us-east-1): ")
	region, _ := reader.ReadString('\n')
	s3.Region = strings.TrimSpace(region)
	if s3.Region == "" {
		return fmt.Errorf("S3 region is required")
	}

	fmt.Fprint(stdout, "S3 Bucket Name: ")
	bucket, _ := reader.ReadString('\n')
	s3.Bucket = strings.TrimSpace(bucket)
	if s3.Bucket == "" {
		return fmt.Errorf("S3 bucket name is required")
	}

	fmt.Fprint(stdout, "S3 Access Key: ")
	accessKey, _ := reader.ReadString('\n')
	s3.AccessKey = strings.TrimSpace(accessKey)
	if s3.AccessKey == "" {
		return fmt.Errorf("S3 access key is required")
	}

	fmt.Fprint(stdout, "S3 Secret Key: ")
	secretKey, _ := reader.ReadString('\n')
	s3.SecretKey = strings.TrimSpace(secretKey)
	if s3.SecretKey == "" {
		return fmt.Errorf("S3 secret key is required")
	}

	fmt.Fprint(stdout, "❓ Setup a daily backup schedule (2 AM)? (y/n): ")
	ansSchedule, _ := reader.ReadString('\n')
	doSchedule := strings.ToLower(strings.TrimSpace(ansSchedule)) == "y"

	fmt.Fprint(stdout, "❓ Save credentials on the server in /opt/graft/infra/.backup.env? (y/n): ")
	ansRemote, _ := reader.ReadString('\n')
	doRemote := strings.ToLower(strings.TrimSpace(ansRemote)) == "y"

	if !doRemote {
		fmt.Fprintln(stdout, "⚠️  Notice: Storing credentials on the server is required for scheduled backups.")
		if doSchedule {
			fmt.Fprint(stdout, "Do you want to proceed with saving them? (y/n): ")
			confirm, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(confirm)) == "y" {
				doRemote = true
			} else {
				fmt.Fprintln(stdout, "❌ Aborted: Credentials must be saved on server for scheduling.")
				return nil
			}
		}
	}

	// 1. Create .backup.env
	envContent := fmt.Sprintf(`AWS_ACCESS_KEY_ID=%s
AWS_SECRET_ACCESS_KEY=%s
AWS_DEFAULT_REGION=%s
S3_BUCKET=%s
S3_ENDPOINT=%s
`, s3.AccessKey, s3.SecretKey, s3.Region, s3.Bucket, s3.Endpoint)

	tmpEnv := filepath.Join(os.TempDir(), ".backup.env")
	os.WriteFile(tmpEnv, []byte(envContent), 0600)
	defer os.Remove(tmpEnv)

	// 2. Create backup.sh
	backupScript := `#!/bin/bash
# pipefail: a failed pg_dumpall must fail the backup, not upload an empty gzip.
set -euo pipefail
umask 077

# Load environment variables
source /opt/graft/infra/.backup.env

TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
FILENAME="db_backup_${TIMESTAMP}.sql.gz"
WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

echo "🐘 Dumping Postgres database..."
sudo docker exec graft-postgres pg_dumpall -U ${POSTGRES_USER:-graft} | gzip > "$WORKDIR/${FILENAME}"
gzip -t "$WORKDIR/${FILENAME}"
if [ "$(stat -c%s "$WORKDIR/${FILENAME}")" -lt 1024 ]; then
    echo "❌ Dump is suspiciously small - not uploading" >&2
    exit 1
fi

echo "📤 Uploading to S3..."
ENDPOINT_FLAG=""
if [ ! -z "$S3_ENDPOINT" ]; then
    ENDPOINT_FLAG="--endpoint-url $S3_ENDPOINT"
fi

sudo docker run --rm -v "$WORKDIR:/work:ro" --env-file /opt/graft/infra/.backup.env amazon/aws-cli $ENDPOINT_FLAG s3 cp /work/${FILENAME} s3://${S3_BUCKET}/backups/${FILENAME}

echo "✅ Backup complete: ${FILENAME}"
`
	// Fetch infra config from remote server to get PostgresUser
	tmpConfigFile := filepath.Join(os.TempDir(), "infra_config_backup.json")
	defer os.Remove(tmpConfigFile)
	
	pgUser := "graft" // default
	if err := client.DownloadFile(config.RemoteInfraPath, tmpConfigFile); err == nil {
		data, err := os.ReadFile(tmpConfigFile)
		if err == nil {
			var infraCfg config.InfraConfig
			if err := json.Unmarshal(data, &infraCfg); err == nil && infraCfg.PostgresUser != "" {
				pgUser = infraCfg.PostgresUser
			}
		}
	}
	
	backupScript = strings.Replace(backupScript, "${POSTGRES_USER:-graft}", pgUser, 1)

	tmpScript := filepath.Join(os.TempDir(), "backup.sh")
	os.WriteFile(tmpScript, []byte(backupScript), 0755)
	defer os.Remove(tmpScript)

	// 3. Upload to server
	fmt.Fprintln(stdout, "📤 Uploading backup script and configuration...")
	if err := client.UploadFile(tmpEnv, "/opt/graft/infra/.backup.env"); err != nil {
		return fmt.Errorf("failed to upload .backup.env: %v", err)
	}
	if err := client.UploadFile(tmpScript, "/opt/graft/infra/backup.sh"); err != nil {
		return fmt.Errorf("failed to upload backup.sh: %v", err)
	}

	// 4. Set permissions
	if err := client.RunCommand("chmod 600 /opt/graft/infra/.backup.env && chmod +x /opt/graft/infra/backup.sh", stdout, stderr); err != nil {
		return fmt.Errorf("failed to set permissions: %v", err)
	}

	// 5. Setup Cron
	if doSchedule {
		fmt.Fprintln(stdout, "📅 Setting up cron job...")
		cronJob := "0 2 * * * /opt/graft/infra/backup.sh >> /var/log/graft-backup.log 2>&1"

		// Check if cron job already exists
		checkCmd := "crontab -l | grep -q '/opt/graft/infra/backup.sh'"
		if err := client.RunCommand(checkCmd, nil, nil); err != nil {
			// Doesn't exist, add it
			addCronCmd := fmt.Sprintf("(crontab -l 2>/dev/null; echo \"%s\") | crontab -", cronJob)
			if err := client.RunCommand(addCronCmd, stdout, stderr); err != nil {
				return fmt.Errorf("failed to setup cron job: %v", err)
			}
			fmt.Fprintln(stdout, "✅ Cron job added (daily at 2 AM)")
		} else {
			fmt.Fprintln(stdout, "✅ Cron job already exists")
		}
	}

	fmt.Fprint(stdout, "\n❓ Run a backup now? (y/n): ")
	runNow, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(runNow)) == "y" {
		fmt.Fprintln(stdout, "🚀 Running backup...")
		if err := client.RunCommand("/opt/graft/infra/backup.sh", stdout, stderr); err != nil {
			return fmt.Errorf("failed to run backup: %v", err)
		}
		fmt.Fprintln(stdout, "✅ Initial backup successful!")
	}

	fmt.Fprintln(stdout, "\n✨ Database backup setup complete!")
	return nil
}
