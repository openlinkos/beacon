package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportFlagsArePaired(t *testing.T) {
	base := []string{"-secret-file", "fixture", "-run", "true"}
	for _, one := range []string{"-report-url", "-report-token-file"} {
		var stdout, stderr bytes.Buffer
		args := append(base, one, "private-flag-value")
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "must be set together") ||
			strings.Contains(stderr.String(), "private-flag-value") {
			t.Errorf("unexpected result: code=%d, error=%q", code, stderr.String())
		}
	}
}

func TestUnreadableReportTokenFileRedactsItsPath(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "hook-secret")
	if err := os.WriteFile(secret, []byte("fixture-hook-secret-longer-than-24"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"-secret-file", secret, "-run", "true", "-report-url", "https://beacon.example.com",
		"-report-token-file", filepath.Join(dir, "private-path-marker")}
	if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "cannot read the report token file") ||
		strings.Contains(stderr.String(), "private-path-marker") {
		t.Errorf("unexpected result: code=%d, error=%q", code, stderr.String())
	}
}
