package executors

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/skssmd/graft/internal/config"
	"github.com/skssmd/graft/internal/server/infra"
)

const dbBackupUsage = `Usage: graft db <name> backup <command>

  set                     Configure bucket, retention and schedule (R2 / S3)
  now                     Back up right now, then prune old backups
  list                    Show every backup: date, size, latest, verified status
  test [--inspect]        Pick a backup, restore it into a throwaway database,
                          run the integrity checks, then delete the database
  restore                 Pick a backup and restore it over the live database
                          (always verified first, with a safety backup)
  download [-expires 24h] Pick a backup and print a temporary download link
  prune                   Delete backups older than the retention period (default 7 days)
  prune <id>              Keep ONLY that backup (must be restore-tested) and delete
                          every other one, however recent - use it once you trust a version
  alert                   Set up Telegram alerts (failed or missing backups, restores)
  log [lines]             Show the verification and backup log`

// RunDbBackup handles "graft db <name> backup <command>".
func (e *Executor) RunDbBackup(dbname string, args []string) {
	name := config.NormalizeProjectName(dbname)
	if name == "" {
		fmt.Println("Error: Invalid database name. Use only letters, numbers, and underscores.")
		return
	}
	if len(args) == 0 {
		fmt.Println(dbBackupUsage)
		return
	}

	sub, rest := args[0], args[1:]
	switch sub {
	case "set", "now", "list", "ls", "test", "test-restore", "restore", "download", "prune", "alert", "log":
	default:
		fmt.Printf("Unknown backup command '%s'.\n\n%s\n", sub, dbBackupUsage)
		return
	}

	client, err := e.getClient()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	defer client.Close()

	in := bufio.NewReader(os.Stdin)
	fail := func(err error) { fmt.Printf("\nError: %v\n", err) }

	switch sub {
	case "set":
		if err := infra.SetupBackup(client, name, in, os.Stdout, os.Stderr); err != nil {
			fail(err)
		}

	case "now":
		if err := infra.BackupNow(client, name, os.Stdout, os.Stderr); err != nil {
			fail(err)
		}

	case "list", "ls":
		if err := infra.ListBackups(client, name, os.Stdout, os.Stderr); err != nil {
			fail(err)
		}

	case "prune":
		if len(rest) > 0 {
			keep, remove, err := infra.PreviewKeepOnly(client, name, rest[0], os.Stderr)
			if err != nil {
				fail(err)
				return
			}
			if keep.Verified == "" {
				fail(fmt.Errorf("%s (id %s) has never passed a test restore - run: graft db %s backup test (nothing was deleted)", keep.File, keep.ID, name))
				return
			}
			if len(remove) == 0 {
				fmt.Printf("%s is already the only backup - nothing to delete.\n", keep.File)
				return
			}
			fmt.Printf("\n📌 KEEP   %s  (id %s, %s, restore-tested)\n", keep.File, keep.ID, keep.Taken.Local().Format("2006-01-02 15:04"))
			var total int64
			for _, b := range remove {
				total += b.Size
				fmt.Printf("🗑️  DELETE  %s  (id %s, %s)\n", b.File, b.ID, b.Taken.Local().Format("2006-01-02 15:04"))
			}
			fmt.Printf("\nThis permanently deletes %d backup(s) (%.1f MB) from the bucket, however recent they are.\n", len(remove), float64(total)/1048576)
			fmt.Println("The kept backup is not protected: regular pruning removes it once it is past the retention period.")
			fmt.Printf("\nType the id (%s) to confirm: ", keep.ID)
			answer, _ := in.ReadString('\n')
			if strings.TrimSpace(answer) != keep.ID {
				fmt.Println("Cancelled. Nothing was deleted.")
				return
			}
			fmt.Println()
			if err := infra.KeepOnly(client, name, keep.File, os.Stdout, os.Stderr); err != nil {
				fail(err)
			}
			return
		}
		if err := infra.PruneBackups(client, name, os.Stdout, os.Stderr); err != nil {
			fail(err)
		}

	case "alert":
		if err := infra.SetupAlerts(client, name, in, os.Stdout, os.Stderr); err != nil {
			fail(err)
		}

	case "log":
		if err := infra.ShowLog(client, name, 150, os.Stdout, os.Stderr); err != nil {
			fail(err)
		}

	case "test", "test-restore":
		inspect := false
		var file string
		for _, a := range rest {
			switch {
			case a == "restore":
			case a == "--inspect" || a == "-inspect":
				inspect = true
			default:
				file = a
			}
		}
		if file == "" {
			file, err = infra.ChooseBackup(client, name, "test", in, os.Stdout, os.Stderr)
			if err != nil {
				fail(err)
				return
			}
		}
		fmt.Println()
		if err := infra.TestRestore(client, name, file, inspect, os.Stdout, os.Stderr); err != nil {
			fmt.Println("\n❌ Test restore FAILED - do not rely on this backup. Full details: graft db " + name + " backup log")
		}

	case "restore":
		var file string
		if len(rest) > 0 {
			file = rest[0]
		} else {
			file, err = infra.ChooseBackup(client, name, "restore", in, os.Stdout, os.Stderr)
			if err != nil {
				fail(err)
				return
			}
		}

		fmt.Printf("\n🛡️  Step 1: verifying %s in a throwaway database before anything is touched...\n\n", file)
		if err := infra.TestRestore(client, name, file, false, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("\n❌ Verification failed - the live database '%s' was NOT changed.\n", name)
			fmt.Println("   Full details: graft db " + name + " backup log")
			return
		}

		fmt.Printf("\n⚠️  Step 2: this REPLACES the live database '%s' with %s.\n", name, file)
		fmt.Println("   Review any warnings above (rows written after the backup will be lost).")
		fmt.Println("   A safety backup of the current data is taken first, and the restore is one")
		fmt.Println("   all-or-nothing transaction. Stop services that write to this database first.")
		fmt.Printf("\nType the database name (%s) to continue: ", name)
		answer, _ := in.ReadString('\n')
		if strings.TrimSpace(answer) != name {
			fmt.Println("Cancelled. Nothing was changed.")
			return
		}
		fmt.Println()
		if err := infra.Restore(client, name, file, os.Stdout, os.Stderr); err != nil {
			fmt.Printf("\n❌ Restore did not complete: %v\n   Full details: graft db %s backup log\n", err, name)
		}

	case "download":
		expiry := "1h"
		var file string
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "-expires", "--expires":
				if i+1 < len(rest) {
					i++
					expiry = rest[i]
				}
			default:
				file = rest[i]
			}
		}
		secs, err := infra.ParseExpiry(expiry)
		if err != nil {
			fail(err)
			return
		}
		if file == "" {
			file, err = infra.ChooseBackup(client, name, "download", in, os.Stdout, os.Stderr)
			if err != nil {
				fail(err)
				return
			}
		}
		fmt.Println()
		if err := infra.Download(client, name, file, secs, os.Stdout, os.Stderr); err != nil {
			fail(err)
			return
		}
		fmt.Println("\n💡 Open the link in a browser, or: curl -o " + file + " \"<link>\"")
		fmt.Println("   Restore a downloaded file locally with: pg_restore -d <local-db> --no-owner " + file)
	}
}
