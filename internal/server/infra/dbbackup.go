package infra

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must resolve on Windows machines too

	"golang.org/x/term"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/ssh"
)

//go:embed graft-backup.sh
var backupScript string

const (
	backupDir        = "/opt/graft/infra/backup"
	backupScriptPath = backupDir + "/graft-backup.sh"
	backupLog        = backupDir + "/backup.log"
)

// Values land in a KEY=value env file that docker reads verbatim, so anything
// that could be reinterpreted (quotes, spaces, $, newlines) is refused.
var safeValue = regexp.MustCompile(`^[A-Za-z0-9_./:@+=,\-]*$`)

// Backup is one dump stored in the bucket.
type Backup struct {
	File     string
	Size     int64
	Taken    time.Time
	Verified string // RFC3339 of the last successful test restore, or ""
	ID       string // short stable id, accepted by prune/download/test/restore
}

// ScheduleHours lists the hours of the day a backup runs, given the first run
// hour and the interval in hours (which must divide 24).
func ScheduleHours(startHour, interval int) ([]int, error) {
	if interval < 1 || interval > 24 || 24%interval != 0 {
		return nil, fmt.Errorf("interval must divide 24 evenly (1, 2, 3, 4, 6, 8, 12 or 24)")
	}
	if startHour < 0 || startHour > 23 {
		return nil, fmt.Errorf("hour must be 0-23")
	}
	var hours []int
	for k := 0; k < 24/interval; k++ {
		hours = append(hours, (startHour+k*interval)%24)
	}
	sort.Ints(hours)
	return hours, nil
}

// ParseClock parses "HH:MM" (24-hour).
func ParseClock(s string) (hour, minute int, err error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("use HH:MM, for example 02:30")
	}
	hour, err1 := strconv.Atoi(parts[0])
	minute, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("use HH:MM, for example 02:30")
	}
	return hour, minute, nil
}

// ParseExpiry accepts Go durations ("90m", "24h") plus "Nd" days, and returns
// whole seconds. Presigned URLs live between 1 minute and 7 days.
func ParseExpiry(s string) (int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid expiry %q", s)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid expiry %q (try 30m, 24h or 7d)", s)
		}
	}
	if d < time.Minute || d > 7*24*time.Hour {
		return 0, fmt.Errorf("expiry must be between 1m and 7d")
	}
	return int(d.Seconds()), nil
}

// ParseBackups reads the script's "plain" output: file, size, ISO time and
// verification stamp per line, oldest first. Unparseable lines are skipped.
func ParseBackups(out string) []Backup {
	var backups []Backup
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 3 || f[0] == "" {
			continue
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		taken, err := time.Parse(time.RFC3339, f[2])
		if err != nil {
			continue
		}
		b := Backup{File: f[0], Size: size, Taken: taken}
		if len(f) >= 4 && f[3] != "-" {
			b.Verified = f[3]
		}
		if len(f) >= 5 {
			b.ID = f[4]
		}
		backups = append(backups, b)
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].File < backups[j].File })
	return backups
}

// FormatBackupMenu renders a numbered table, newest first, with the latest
// marked. Times are shown in loc so they match the user's own clock.
func FormatBackupMenu(backups []Backup, loc *time.Location, now time.Time) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %-3s  %-8s  %-17s  %-12s  %9s  %s\n", "#", "ID", "TAKEN", "AGE", "SIZE", "STATUS")
	for i := len(backups) - 1; i >= 0; i-- {
		b := backups[i]
		status := "uploaded"
		if b.Verified != "" {
			status = "✔ restore-tested"
			if t, err := time.Parse(time.RFC3339, b.Verified); err == nil {
				status += " " + t.In(loc).Format("2006-01-02")
			}
		}
		if i == len(backups)-1 {
			status += "  ← latest"
		}
		fmt.Fprintf(&sb, "  %-3d  %-8s  %-17s  %-12s  %9s  %s\n",
			len(backups)-i, b.ID, b.Taken.In(loc).Format("2006-01-02 15:04"), humanAge(now.Sub(b.Taken)), humanSize(b.Size), status)
	}
	return sb.String()
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func scriptCmd(db, action string, args ...string) string {
	parts := []string{shQuote(backupScriptPath), shQuote(db), action}
	for _, a := range args {
		parts = append(parts, shQuote(a))
	}
	return strings.Join(parts, " ")
}

// installScript uploads the current script so the server never runs a stale copy.
func installScript(client *ssh.Client, stderr io.Writer) error {
	if err := client.RunCommand("mkdir -p "+backupDir+"/tmp && chmod 700 "+backupDir, nil, stderr); err != nil {
		return fmt.Errorf("could not create %s: %v", backupDir, err)
	}
	tmp := filepath.Join(os.TempDir(), "graft-backup.sh")
	body := strings.ReplaceAll(backupScript, "\r\n", "\n")
	if err := os.WriteFile(tmp, []byte(body), 0700); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := client.UploadFile(tmp, backupScriptPath); err != nil {
		return fmt.Errorf("could not upload backup script: %v", err)
	}
	return client.RunCommand("chmod 700 "+backupScriptPath, nil, stderr)
}

func hasConfig(client *ssh.Client, db string) bool {
	return client.RunCommand("test -f "+shQuote(backupDir+"/"+db+".env"), nil, nil) == nil
}

func requireConfig(client *ssh.Client, db string) error {
	if !hasConfig(client, db) {
		return fmt.Errorf("no backup is configured for '%s' - run: graft db %s backup set", db, db)
	}
	return nil
}

// BackupNow runs a backup immediately, then prunes.
func BackupNow(client *ssh.Client, db string, stdout, stderr io.Writer) error {
	if err := requireConfig(client, db); err != nil {
		return err
	}
	if err := installScript(client, stderr); err != nil {
		return err
	}
	return client.RunCommand(scriptCmd(db, "run"), stdout, stderr)
}

// PruneBackups deletes backups older than the configured retention.
func PruneBackups(client *ssh.Client, db string, stdout, stderr io.Writer) error {
	if err := requireConfig(client, db); err != nil {
		return err
	}
	if err := installScript(client, stderr); err != nil {
		return err
	}
	return client.RunCommand(scriptCmd(db, "prune"), stdout, stderr)
}

func fetchBackups(client *ssh.Client, db string, stderr io.Writer) ([]Backup, error) {
	var out strings.Builder
	if err := client.RunCommand(scriptCmd(db, "plain"), &out, stderr); err != nil {
		return nil, fmt.Errorf("could not list backups: %v", err)
	}
	return ParseBackups(out.String()), nil
}

// ListBackups prints the config summary and the numbered backup table.
func ListBackups(client *ssh.Client, db string, stdout, stderr io.Writer) error {
	if err := requireConfig(client, db); err != nil {
		return err
	}
	if err := installScript(client, stderr); err != nil {
		return err
	}
	if err := client.RunCommand(scriptCmd(db, "info"), stdout, stderr); err != nil {
		return err
	}
	backups, err := fetchBackups(client, db, stderr)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout)
	if len(backups) == 0 {
		fmt.Fprintln(stdout, "No backups yet. Run: graft "+"db "+db+" backup now")
		return nil
	}
	fmt.Fprint(stdout, FormatBackupMenu(backups, time.Local, time.Now()))
	return nil
}

// ChooseBackup shows every backup (latest marked) and returns the file the user
// picks. Enter selects the latest.
func ChooseBackup(client *ssh.Client, db, purpose string, in *bufio.Reader, stdout, stderr io.Writer) (string, error) {
	if err := requireConfig(client, db); err != nil {
		return "", err
	}
	if err := installScript(client, stderr); err != nil {
		return "", err
	}
	backups, err := fetchBackups(client, db, stderr)
	if err != nil {
		return "", err
	}
	if len(backups) == 0 {
		return "", fmt.Errorf("no backups found for '%s' - run: graft db %s backup now", db, db)
	}
	fmt.Fprintf(stdout, "\n📚 Backups of '%s' (times in your local zone):\n\n", db)
	fmt.Fprint(stdout, FormatBackupMenu(backups, time.Local, time.Now()))
	fmt.Fprintf(stdout, "\nWhich backup to %s? [1 = latest]: ", purpose)
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	n := 1
	if line != "" {
		n, err = strconv.Atoi(line)
		if err != nil || n < 1 || n > len(backups) {
			return "", fmt.Errorf("choose a number between 1 and %d", len(backups))
		}
	}
	return backups[len(backups)-n].File, nil
}

// TestRestore restores a backup into a throwaway database and deletes it again.
// With inspect, a psql session is opened on the copy before it is removed.
func TestRestore(client *ssh.Client, db, file string, inspect bool, stdout, stderr io.Writer) error {
	args := []string{file}
	if inspect {
		args = append(args, "inspect")
		return client.RunInteractiveCommand(scriptCmd(db, "test-restore", args...))
	}
	return client.RunCommand(scriptCmd(db, "test-restore", args...), stdout, stderr)
}

// FindBackup resolves a file name, "latest" or a short id from the list.
func FindBackup(backups []Backup, ref string) (Backup, error) {
	if len(backups) == 0 {
		return Backup{}, fmt.Errorf("there are no backups")
	}
	if ref == "" || ref == "latest" {
		return backups[len(backups)-1], nil
	}
	for _, b := range backups {
		if b.ID == ref || b.File == ref {
			return b, nil
		}
	}
	return Backup{}, fmt.Errorf("no backup matches '%s' - see: graft db <name> backup list", ref)
}

// PreviewKeepOnly returns the backup that would be kept and the ones that would
// be deleted, so the CLI can show exactly what "prune <id>" will remove.
func PreviewKeepOnly(client *ssh.Client, db, ref string, stderr io.Writer) (keep Backup, remove []Backup, err error) {
	if err = requireConfig(client, db); err != nil {
		return
	}
	if err = installScript(client, stderr); err != nil {
		return
	}
	backups, err := fetchBackups(client, db, stderr)
	if err != nil {
		return
	}
	keep, err = FindBackup(backups, ref)
	if err != nil {
		return
	}
	for _, b := range backups {
		if b.File != keep.File {
			remove = append(remove, b)
		}
	}
	return
}

// KeepOnly deletes every backup except the given one, whatever its age. The caller
// must have confirmed with the user.
func KeepOnly(client *ssh.Client, db, file string, stdout, stderr io.Writer) error {
	return client.RunCommand(scriptCmd(db, "keep-only", file), stdout, stderr)
}

// Restore replaces the live database with a backup. The caller must have
// confirmed with the user; the script takes a safety backup first.
func Restore(client *ssh.Client, db, file string, stdout, stderr io.Writer) error {
	return client.RunCommand(scriptCmd(db, "restore", file), stdout, stderr)
}

// ShowLog prints the tail of the per-database backup log, which records every
// verification layer and backup step.
func ShowLog(client *ssh.Client, db string, lines int, stdout, stderr io.Writer) error {
	if err := requireConfig(client, db); err != nil {
		return err
	}
	if err := installScript(client, stderr); err != nil {
		return err
	}
	return client.RunCommand(scriptCmd(db, "log", strconv.Itoa(lines)), stdout, stderr)
}

// Download prints a presigned URL for a backup.
func Download(client *ssh.Client, db, file string, expirySeconds int, stdout, stderr io.Writer) error {
	return client.RunCommand(scriptCmd(db, "presign", file, strconv.Itoa(expirySeconds)), stdout, stderr)
}

// DatabaseExists reports whether the named database exists in graft-postgres.
func DatabaseExists(client *ssh.Client, pgUser, db string) (bool, error) {
	out, err := client.GetCommandOutput(fmt.Sprintf(
		"sudo docker exec graft-postgres psql -U %s -d postgres -tAc \"SELECT 1 FROM pg_database WHERE datname='%s'\"",
		shQuote(pgUser), db))
	if err != nil {
		return false, fmt.Errorf("could not query postgres (is graft-postgres running?): %v", err)
	}
	return strings.TrimSpace(out) == "1", nil
}

func InfraPostgresUser(client *ssh.Client) (string, error) {
	tmp := filepath.Join(os.TempDir(), "infra_config_dbbackup.json")
	defer os.Remove(tmp)
	if err := client.DownloadFile(config.RemoteInfraPath, tmp); err != nil {
		return "", fmt.Errorf("could not fetch infra config - run 'graft host init' first")
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return "", err
	}
	var cfg config.InfraConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", err
	}
	if cfg.PostgresUser == "" {
		return "", fmt.Errorf("postgres is not configured on this host - run 'graft host init'")
	}
	return cfg.PostgresUser, nil
}

func parseEnv(raw string) map[string]string {
	m := map[string]string{}
	for line := range strings.SplitSeq(raw, "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "="); ok {
			m[k] = v
		}
	}
	return m
}

type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func (p *prompter) ask(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	line, _ := p.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func (p *prompter) secret(label string, hasExisting bool) string {
	if hasExisting {
		fmt.Fprintf(p.out, "%s [keep existing]: ", label)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		b, _ := term.ReadPassword(fd)
		fmt.Fprintln(p.out)
		return strings.TrimSpace(string(b))
	}
	line, _ := p.in.ReadString('\n')
	return strings.TrimSpace(line)
}

func (p *prompter) confirm(label string, def bool) bool {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	fmt.Fprintf(p.out, "%s (%s): ", label, hint)
	line, _ := p.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	}
	return false
}

// SetupBackup interactively configures bucket, retention and schedule for one
// database, verifies the bucket, then installs the cron job.
func SetupBackup(client *ssh.Client, db string, in *bufio.Reader, stdout, stderr io.Writer) error {
	pgUser, err := InfraPostgresUser(client)
	if err != nil {
		return err
	}
	exists, err := DatabaseExists(client, pgUser, db)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("database '%s' does not exist on this server - create it with: graft db %s init", db, db)
	}
	if client.RunCommand("command -v crontab >/dev/null", nil, nil) != nil {
		return fmt.Errorf("crontab is not installed on the server (install the cron package first)")
	}

	envPath := backupDir + "/" + db + ".env"
	prevRaw, _ := client.GetCommandOutput("cat " + shQuote(envPath) + " 2>/dev/null")
	prev := parseEnv(prevRaw)
	hadPrev := prev["S3_BUCKET"] != ""

	p := &prompter{in: in, out: stdout}
	fmt.Fprintf(stdout, "\n☁️  Backup setup for database '%s'\n", db)
	fmt.Fprintln(stdout, "   Works with Cloudflare R2, AWS S3 and other S3-compatible storage.")
	fmt.Fprintln(stdout, "   R2 endpoint looks like https://<account-id>.r2.cloudflarestorage.com (region: auto)")
	fmt.Fprintln(stdout, "----------------------------------------------------------------")

	endpoint := p.ask("S3/R2 endpoint (blank for AWS)", prev["S3_ENDPOINT"])
	regionDef := prev["AWS_DEFAULT_REGION"]
	if regionDef == "" && strings.Contains(endpoint, "r2.cloudflarestorage.com") {
		regionDef = "auto"
	}
	region := p.ask("Region", regionDef)
	bucket := p.ask("Bucket", prev["S3_BUCKET"])
	access := p.ask("Access key ID", prev["AWS_ACCESS_KEY_ID"])
	secret := p.secret("Secret access key", prev["AWS_SECRET_ACCESS_KEY"] != "")
	if secret == "" {
		secret = prev["AWS_SECRET_ACCESS_KEY"]
	}
	retDef := "7"
	if prev["RETENTION_DAYS"] != "" {
		retDef = prev["RETENTION_DAYS"]
	}
	retention := p.ask("Keep backups for how many days", retDef)

	fmt.Fprintln(stdout, "\n⏰ Schedule")
	tzDef := prev["SCHEDULE_TZ"]
	if tzDef == "" {
		tzDef = "UTC"
	}
	tz := p.ask("Time zone (e.g. Asia/Dhaka, UTC)", tzDef)
	clockDef := "02:00"
	if prev["SCHEDULE_MINUTE"] != "" && prev["SCHEDULE_HOURS"] != "" {
		first, _, _ := strings.Cut(prev["SCHEDULE_HOURS"], ",")
		if h, err := strconv.Atoi(first); err == nil {
			m, _ := strconv.Atoi(prev["SCHEDULE_MINUTE"])
			clockDef = fmt.Sprintf("%02d:%02d", h, m)
		}
	}
	clock := p.ask("First backup time of day (HH:MM, 24h)", clockDef)
	interval := p.ask("Repeat every how many hours (1,2,3,4,6,8,12,24)", "12")

	// Validate everything before touching the server.
	for name, v := range map[string]string{"endpoint": endpoint, "region": region, "bucket": bucket, "access key": access, "secret key": secret} {
		if !safeValue.MatchString(v) {
			return fmt.Errorf("%s contains characters that are not allowed (spaces, quotes, $ or similar)", name)
		}
	}
	if region == "" || bucket == "" || access == "" || secret == "" {
		return fmt.Errorf("region, bucket, access key and secret key are all required")
	}
	if endpoint != "" && !strings.HasPrefix(endpoint, "https://") && !strings.HasPrefix(endpoint, "http://") {
		return fmt.Errorf("endpoint must start with https://")
	}
	days, err := strconv.Atoi(retention)
	if err != nil || days < 1 {
		return fmt.Errorf("retention must be a whole number of days, 1 or more")
	}
	if _, err := time.LoadLocation(tz); err != nil || !safeValue.MatchString(tz) {
		return fmt.Errorf("unknown time zone %q (use an IANA name like Asia/Dhaka)", tz)
	}
	hour, minute, err := ParseClock(clock)
	if err != nil {
		return err
	}
	step, err := strconv.Atoi(interval)
	if err != nil {
		return fmt.Errorf("interval must be a number of hours")
	}
	hours, err := ScheduleHours(hour, step)
	if err != nil {
		return err
	}
	hourStrs := make([]string, len(hours))
	for i, h := range hours {
		hourStrs[i] = strconv.Itoa(h)
	}

	if client.RunCommand("test -e /usr/share/zoneinfo/"+shQuote(tz), nil, nil) != nil {
		fmt.Fprintf(stdout, "⚠️  The server has no zoneinfo for %s; scheduled hours may be read as UTC. Install tzdata on the server.\n", tz)
	}

	env := fmt.Sprintf("AWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\nAWS_DEFAULT_REGION=%s\nS3_BUCKET=%s\nS3_ENDPOINT=%s\nPGUSER=%s\nRETENTION_DAYS=%d\nSCHEDULE_TZ=%s\nSCHEDULE_MINUTE=%d\nSCHEDULE_HOURS=%s\n",
		access, secret, region, bucket, endpoint, pgUser, days, tz, minute, strings.Join(hourStrs, ","))

	if err := installScript(client, stderr); err != nil {
		return err
	}
	if err := writeRemote(client, envPath, env, 0600); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "\n🔌 Checking the bucket...")
	if err := client.RunCommand(scriptCmd(db, "check"), stdout, stderr); err != nil {
		// Leave the server as it was.
		if hadPrev {
			_ = writeRemote(client, envPath, prevRaw, 0600)
		} else {
			_ = client.RunCommand("rm -f "+shQuote(envPath), nil, nil)
		}
		return fmt.Errorf("bucket check failed; nothing was changed")
	}

	line := fmt.Sprintf("%d * * * * flock -n %s %s %s scheduled >> %s 2>&1",
		minute, shQuote(backupDir+"/"+db+".lock"), backupScriptPath, db, backupLog)
	marker := backupScriptPath + " " + db + " scheduled"
	cronCmd := fmt.Sprintf("(crontab -l 2>/dev/null | grep -vF %s; printf '%%s\\n' %s) | crontab -", shQuote(marker), shQuote(line))
	if err := client.RunCommand(cronCmd, stdout, stderr); err != nil {
		return fmt.Errorf("could not install the cron job: %v", err)
	}

	times := make([]string, len(hours))
	for i, h := range hours {
		times[i] = fmt.Sprintf("%02d:%02d", h, minute)
	}
	fmt.Fprintf(stdout, "\n✅ Backups of '%s' are scheduled at %s (%s), keeping %d days.\n", db, strings.Join(times, " and "), tz, days)
	fmt.Fprintf(stdout, "   Old backups are pruned after each run. Log: %s\n", backupLog)

	if client.RunCommand("test -f "+shQuote(backupDir+"/"+db+".telegram"), nil, nil) != nil &&
		p.confirm("\nGet Telegram alerts when a backup fails?", true) {
		if err := SetupAlerts(client, db, in, stdout, stderr); err != nil {
			fmt.Fprintf(stdout, "⚠️  Alerts were not set up: %v\n   You can retry with: graft db %s backup alert\n", err, db)
		}
	}

	if p.confirm("\nRun a backup now to confirm it works?", true) {
		return client.RunCommand(scriptCmd(db, "run"), stdout, stderr)
	}
	return nil
}

func writeRemote(client *ssh.Client, remote, content string, mode os.FileMode) error {
	tmp := filepath.Join(os.TempDir(), "graft-backup-upload.tmp")
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := client.UploadFile(tmp, remote); err != nil {
		return fmt.Errorf("could not upload %s: %v", remote, err)
	}
	return client.RunCommand(fmt.Sprintf("chmod %o %s", mode, shQuote(remote)), nil, nil)
}
