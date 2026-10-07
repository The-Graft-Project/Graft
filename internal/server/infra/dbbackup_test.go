package infra

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestScheduleHours(t *testing.T) {
	tests := []struct {
		name     string
		start    int
		interval int
		want     []int
		wantErr  bool
	}{
		{"every 12h from 02", 2, 12, []int{2, 14}, false},
		{"every 12h wraps midnight", 20, 12, []int{8, 20}, false},
		{"daily", 2, 24, []int{2}, false},
		{"every 6h", 1, 6, []int{1, 7, 13, 19}, false},
		{"interval that does not divide 24", 2, 5, nil, true},
		{"zero interval", 2, 0, nil, true},
		{"hour out of range", 24, 12, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScheduleHours(tt.start, tt.interval)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseClock(t *testing.T) {
	h, m, err := ParseClock("02:30")
	if err != nil || h != 2 || m != 30 {
		t.Fatalf("ParseClock(02:30) = %d, %d, %v", h, m, err)
	}
	for _, bad := range []string{"", "2", "24:00", "12:60", "ab:cd", "1:2:3"} {
		if _, _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) should fail", bad)
		}
	}
}

func TestParseExpiry(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"1h", 3600, false},
		{"90m", 5400, false},
		{"7d", 604800, false},
		{"24H", 86400, false},
		{"30s", 0, true},
		{"8d", 0, true},
		{"soon", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseExpiry(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseExpiry(%q) = %d, %v; want %d (err %v)", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestSafeValueRejectsShellMetacharacters(t *testing.T) {
	for _, ok := range []string{"https://abc.r2.cloudflarestorage.com", "AKIA0123", "a/b+c=d", "auto"} {
		if !safeValue.MatchString(ok) {
			t.Errorf("%q should be allowed", ok)
		}
	}
	for _, bad := range []string{"a b", "a'b", `a"b`, "a$b", "a\nb", "a;b", "a`b"} {
		if safeValue.MatchString(bad) {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestParseBackupsAndMenu(t *testing.T) {
	out := "main_20261007T020000Z.dump\t1048576\t2026-10-07T02:00:05+00:00\t-\n" +
		"main_20261006T140000Z.dump\t2097152\t2026-10-06T14:00:04+00:00\t2026-10-06T15:00:00Z\n" +
		"garbage line\n"
	backups := ParseBackups(out)
	if len(backups) != 2 {
		t.Fatalf("got %d backups, want 2", len(backups))
	}
	if backups[0].File != "main_20261006T140000Z.dump" || backups[0].Verified == "" {
		t.Fatalf("backups must be sorted oldest first with verification kept: %+v", backups)
	}

	now := time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)
	menu := FormatBackupMenu(backups, time.UTC, now)
	lines := strings.Split(strings.TrimSpace(menu), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got:\n%s", menu)
	}
	if !strings.Contains(lines[1], "2026-10-07 02:00") || !strings.Contains(lines[1], "latest") || !strings.Contains(lines[1], "2h ago") {
		t.Errorf("row 1 must be the newest backup, marked latest: %q", lines[1])
	}
	if !strings.Contains(lines[2], "restore-tested") || strings.Contains(lines[2], "latest") {
		t.Errorf("row 2 must be the older, verified backup: %q", lines[2])
	}
}

func TestEmbeddedScriptIsLinuxShell(t *testing.T) {
	if !strings.HasPrefix(backupScript, "#!/bin/bash") {
		t.Fatal("embedded script is missing its shebang")
	}
	for _, must := range []string{"pipefail", "--single-transaction", "layer 7", "PGDMP"} {
		if !strings.Contains(backupScript, must) {
			t.Errorf("embedded script lost its %q safeguard", must)
		}
	}
}

func TestParseBackupsReadsID(t *testing.T) {
	out := "main_20261007T020000Z.dump\t10\t2026-10-07T02:00:05+00:00\t-\tabcd1234\t0\n" +
		"main_20261006T140000Z.dump\t10\t2026-10-06T14:00:04+00:00\t2026-10-06T15:00:00Z\t0f0f0f0f\t1\n"
	backups := ParseBackups(out)

	older, err := FindBackup(backups, "0f0f0f0f")
	if err != nil || older.ID != "0f0f0f0f" || older.File != "main_20261006T140000Z.dump" {
		t.Fatalf("FindBackup by id = %+v, %v", older, err)
	}
	if latest, _ := FindBackup(backups, "latest"); latest.ID != "abcd1234" {
		t.Fatalf("latest = %+v", latest)
	}
	if byName, _ := FindBackup(backups, "main_20261007T020000Z.dump"); byName.ID != "abcd1234" {
		t.Fatalf("FindBackup by file name = %+v", byName)
	}
	if _, err := FindBackup(backups, "deadbeef"); err == nil {
		t.Fatal("unknown id must be an error")
	}

	menu := FormatBackupMenu(backups, time.UTC, time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC))
	if !strings.Contains(menu, "abcd1234") {
		t.Errorf("menu must show ids:\n%s", menu)
	}
}

func TestScriptKeepOnlyRequiresTestedBackup(t *testing.T) {
	for _, must := range []string{"has never passed a test restore", "do_keep_only"} {
		if !strings.Contains(backupScript, must) {
			t.Errorf("embedded script lost %q", must)
		}
	}
}

func TestParseChatID(t *testing.T) {
	got, err := parseChatID([]byte(`{"ok":true,"result":[
		{"message":{"chat":{"id":111}}},
		{"message":{"chat":{"id":-1001234567890}}}]}`))
	if err != nil || got != "-1001234567890" {
		t.Fatalf("parseChatID = %q, %v; want the most recent chat", got, err)
	}
	if got, err := parseChatID([]byte(`{"ok":true,"result":[]}`)); err != nil || got != "" {
		t.Fatalf("empty result = %q, %v", got, err)
	}
	if _, err := parseChatID([]byte(`{"ok":false}`)); err == nil {
		t.Fatal("a rejected token must be an error")
	}
}

func TestTelegramValidation(t *testing.T) {
	if !telegramToken.MatchString("123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw") {
		t.Error("valid token rejected")
	}
	for _, bad := range []string{"", "abc", "123:short", "123456789:AAHdq TcvCH1vGWJxfSeofSAs0K5PALDsaw", `1234567:"; rm -rf /; "aaaaaaaaaaaaaaaaaaaa`} {
		if telegramToken.MatchString(bad) {
			t.Errorf("token %q must be rejected", bad)
		}
	}
	for _, ok := range []string{"123456789", "-1001234567890"} {
		if !telegramChatID.MatchString(ok) {
			t.Errorf("chat id %q rejected", ok)
		}
	}
	if telegramChatID.MatchString("12ab") {
		t.Error("non-numeric chat id accepted")
	}
}

func TestScriptAlertsOnFailureAndStaleness(t *testing.T) {
	for _, must := range []string{"notify()", "stale_check", "alert-test", "curl -fsS -m 20 -K -"} {
		if !strings.Contains(backupScript, must) {
			t.Errorf("embedded script lost %q", must)
		}
	}
}
