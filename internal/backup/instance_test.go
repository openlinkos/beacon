package backup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestResolveInstanceNamePersistsAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	first, err := ResolveInstanceName(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`).MatchString(first) {
		t.Fatalf("not a hostname: %q", first)
	}
	path := filepath.Join(dir, "backup", "instance-name")
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != first {
		t.Fatalf("file: %q %v", b, err)
	}
	second, err := ResolveInstanceName(dir, "")
	if err != nil || second != first {
		t.Fatalf("second: %q %v want %q", second, err, first)
	}
	if err := os.WriteFile(path, []byte("kept-name\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := ResolveInstanceName(dir, "")
	if err != nil || third != "kept-name" {
		t.Fatalf("kept: %q %v", third, err)
	}
}

func TestResolveInstanceNameUsesConfigured(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolveInstanceName(dir, "edge-1")
	if err != nil || got != "edge-1" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backup", "instance-name")); !os.IsNotExist(err) {
		t.Fatalf("configured host wrote instance-name: %v", err)
	}
}

func TestResolveInstanceNameRejectsBadConfigured(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"../x", "bad name", "-nope"} {
		if _, err := ResolveInstanceName(dir, bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
