//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// vaultLogs collects the service's log (its own type, so this test file is portable: the unix
// config test has its own buffer).
type vaultLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *vaultLogs) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *vaultLogs) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestServePassphraseVault runs the service with the vault in the passphrase mode, the way a
// deployment would: the first start is sealed (the key functions wait; the rest of the service —
// the configuration, monitoring's world — carries on), the passphrase unseals it through the admin
// socket, sealing again works, and the passphrase never shows in the log.
func TestServePassphraseVault(t *testing.T) {
	d := t.TempDir()
	put := func(path, body string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	etc, data := filepath.Join(d, "etc"), filepath.Join(d, "data")
	put(filepath.Join(etc, "catalog", "locations.yaml"), "locations:\n  - {id: edge-1, name: United States - Los Angeles, region: overseas}\n")
	put(filepath.Join(etc, "targets", "all-web.yaml"), "- targets: [https://example.com/]\n  labels: {module: http_head, service: example.com}\n")
	put(filepath.Join(etc, "targets", "modules.yml"), "modules:\n  http_head: {prober: http, timeout: 20s}\n")
	put(filepath.Join(d, "rules-token"), "0123456789abcdef0123456789")
	passFile := filepath.Join(d, "passphrase")
	put(passFile, "correct horse battery fixture")
	cfgPath := filepath.Join(d, "config.yaml")
	put(cfgPath, "listen: 127.0.0.1:0\ndata_dir: "+data+"\ncatalog_dir: "+filepath.Join(etc, "catalog")+"\nlanguage: en\n"+
		"gateway: {listen: 127.0.0.1:0, targets_dir: "+filepath.Join(etc, "targets")+"}\n"+
		"rules: {token_file: "+filepath.Join(d, "rules-token")+"}\n"+
		"victoriametrics: {url: http://127.0.0.1:1}\n"+
		"vault: {mode: passphrase}\n")
	t.Setenv("BEACON_DATA_DIR", data)

	var logs vaultLogs
	done := make(chan int, 1)
	go func() { done <- run([]string{"serve", "-config", cfgPath}, &logs, &logs) }()
	cli := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := run(args, &out, &errb)
		return code, out.String() + errb.String()
	}
	wait := func(what string, fn func() (int, string)) (int, string) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			code, out := fn()
			if code == 0 {
				return code, out
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s never answered:\n%s", what, logs.String())
			}
		}
	}

	// the service answers while the vault is sealed: monitoring's world carries on
	wait("the admin socket", func() (int, string) { return cli("config", "history") })
	// the vault is sealed, and the key commands say so
	if code, out := wait("key status", func() (int, string) { return cli("key", "status") }); code != 0 || !strings.HasPrefix(out, "sealed\t") {
		t.Fatalf("key status: %d %q", code, out)
	}
	// the passphrase unseals it
	if code, out := cli("key", "unseal", "--passphrase-file", passFile); code != 0 || !strings.Contains(out, "unsealed: true") {
		t.Fatalf("key unseal: %d %s", code, out)
	}
	if code, out := cli("key", "status"); code != 0 || !strings.HasPrefix(out, "unsealed\t") {
		t.Fatalf("key status after the unseal: %d %q", code, out)
	}
	// the passphrase is never echoed
	if strings.Contains(logs.String(), "correct horse battery fixture") {
		t.Fatal("the passphrase shows in the log")
	}
	// sealing again, and the sealed state answers the key commands again
	if code, out := cli("key", "seal"); code != 0 {
		t.Fatalf("key seal: %d %s", code, out)
	}
	if code, out := cli("key", "status"); code != 0 || !strings.HasPrefix(out, "sealed\t") {
		t.Fatalf("key status after sealing: %d %q", code, out)
	}
	// a second unseal works too (the key check is there now)
	if code, out := cli("key", "unseal", "--passphrase-file", passFile); code != 0 || !strings.Contains(out, "unsealed: true") {
		t.Fatalf("key unseal again: %d %s", code, out)
	}
	select { // the service keeps running; the test binary's exit ends it, as the other serve tests
	case <-done:
	default:
	}
}
