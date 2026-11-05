package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The backup section: defaults that make a working backup with nothing but a destination, and
// validation that stops the mistakes a quiet backup would hide (a mistyped key, a destination
// that is neither directory nor store, a schedule too tight to breathe).

func TestBackupDefaults(t *testing.T) {
	c, err := Parse([]byte(base(t) + "backup: {destinations: [{dir: /var/lib/beacon/backups}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	b := c.Backup
	if !b.Enabled() || len(b.Destinations) != 1 {
		t.Fatalf("enabled: %+v", b)
	}
	if b.Heartbeat.MaxAge.D() != 2*time.Hour || b.Heartbeat.Severity != "warning" {
		t.Errorf("heartbeat defaults: %+v", b.Heartbeat)
	}
	if b.DrillExpiry.D() != 90*24*time.Hour {
		t.Errorf("drill expiry default: %s", b.DrillExpiry)
	}
	if b.Schedule.Database.D() != time.Hour || b.Schedule.Files.D() != 24*time.Hour || b.Schedule.FilesFull.D() != 7*24*time.Hour {
		t.Errorf("schedule defaults: %+v", b.Schedule)
	}
	if b.Retention.Hourly != 24 || b.Retention.Daily != 14 || b.Retention.Weekly != 8 || b.Retention.Monthly != 12 {
		t.Errorf("retention defaults: %+v", b.Retention)
	}
	if b.Retries != 3 || b.RetryWait.D() != time.Minute || !b.VerifyReadbackOn() {
		t.Errorf("retry defaults: %d %s readback %v", b.Retries, b.RetryWait, b.VerifyReadbackOn())
	}
	if b.OnFailure != "alert" {
		t.Errorf("on-failure default: %s", b.OnFailure)
	}
}

func TestBackupOff(t *testing.T) {
	c, err := Parse([]byte(base(t)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Backup.Enabled() {
		t.Fatal("backups are on with no destination")
	}
}

func TestBackupValidation(t *testing.T) {
	tok := base(t)
	dir := t.TempDir()
	access := writeTmp(t, dir, "access", "a-fixture-access-key")
	secretKey := writeTmp(t, dir, "secret", "a-secret-value")
	s3 := "s3: {endpoint: https://s3.example.com, bucket: b, prefix: beacon/, access_key_file: " + access + ", secret_key_file: " + secretKey + "}"
	for name, y := range map[string]string{
		"both kinds":        "backup: {destinations: [{dir: /tmp, " + s3 + "}]}\n",
		"neither kind":      "backup: {destinations: [{}]}\n",
		"s3 without bucket": "backup: {destinations: [{s3: {endpoint: https://s3.example.com}}]}\n",
		"s3 bad endpoint":   "backup: {destinations: [{s3: {endpoint: s3.example.com, bucket: b}}]}\n",
		"s3 without keys":   "backup: {destinations: [{s3: {endpoint: https://s3.example.com, bucket: b}}]}\n",
		"s3 missing file": "backup: {destinations: [{s3: {endpoint: https://s3.example.com, bucket: b, " +
			"access_key_file: " + filepath.ToSlash(filepath.Join(dir, "nope")) + ", secret_key_file: " + secretKey + "}}]}\n",
		"bad recipient":      "backup: {destinations: [{dir: /tmp}], recipients: [not-a-key]}\n",
		"bad heartbeat sev":  "backup: {destinations: [{dir: /tmp}], heartbeat: {severity: loud}}\n",
		"tight database":     "backup: {destinations: [{dir: /tmp}], schedule: {database: 1m}}\n",
		"tight files":        "backup: {destinations: [{dir: /tmp}], schedule: {files: 5m}}\n",
		"full under files":   "backup: {destinations: [{dir: /tmp}], schedule: {files: 24h, files_full: 2h}}\n",
		"bad on failure":     "backup: {destinations: [{dir: /tmp}], on_failure: ignore}\n",
		"alert and pause":    "backup: {destinations: [{dir: /tmp}], on_failure: alert_and_pause}\n",
		"recipients missing": "backup: {destinations: [{dir: /tmp}], recipients_file: " + filepath.ToSlash(filepath.Join(dir, "nope")) + "}\n",
	} {
		if _, err := Parse([]byte(tok + y)); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if strings.Contains(err.Error(), "a-secret-value") {
			t.Errorf("%s: the error leaks a credential: %v", name, err)
		}
	}
	// a whole, valid section parses: a local directory and an S3 store at once, two recipients
	c, err := Parse([]byte(tok + "backup:\n  destinations:\n    - {dir: /var/lib/beacon/backups}\n    - {" + s3 + "}\n" +
		"  recipients: [age1qyvlchnhvn7h9vhzxcqdr4pj4gwz4gp8q4lzjr0vzq99w8pn5atsp4a2km]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Backup.Destinations) != 2 || len(c.Backup.Recipients) != 1 {
		t.Fatalf("parsed: %+v", c.Backup)
	}
}

// base is a minimal whole configuration (the rules token aside, the gateway's metrics store).
func base(t *testing.T) string {
	t.Helper()
	return "rules: {token_file: " + writeTmp(t, t.TempDir(), "tok", "0123456789abcdef0123456789") + "}\n" +
		"victoriametrics: {url: http://vm:8428}\n"
}

func writeTmp(t *testing.T, dir, name, value string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(p)
}
