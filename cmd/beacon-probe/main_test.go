package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAndRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	f := filepath.Join(t.TempDir(), "modules.yml")
	body := "modules:\n  http_204:\n    prober: http\n    timeout: 5s\n    http: {valid_status_codes: [204], preferred_ip_protocol: ip4}\n"
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-modules", f}, &out, &errb); code != 0 || !strings.Contains(out.String(), "modules OK: 1") {
		t.Fatalf("check: %d %s %s", code, out.String(), errb.String())
	}
	out.Reset()
	if code := run([]string{"run", "-modules", f, "http_204", srv.URL}, &out, &errb); code != 0 || !strings.Contains(out.String(), "probe_success 1") {
		t.Fatalf("run: %d %s %s", code, out.String(), errb.String())
	}
	if code := run([]string{"run", "-modules", f, "nope", srv.URL}, &out, &errb); code != 2 {
		t.Fatalf("unknown module: %d", code)
	}
	if code := run([]string{"nonsense"}, &out, &errb); code != 2 {
		t.Fatalf("unknown command: %d", code)
	}
}

// TestSecretsDir: check refuses a module whose password_file is a path, as the centre does; run reads
// the file from -secrets-dir by name.
func TestSecretsDir(t *testing.T) {
	d := t.TempDir()
	bad := filepath.Join(d, "bad.yml")
	_ = os.WriteFile(bad, []byte("modules:\n  repo:\n    prober: git\n    git: {username: u, password_file: /etc/passwd}\n"), 0o600)
	var out, errb bytes.Buffer
	if code := run([]string{"check", "-modules", bad}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "secrets directory") {
		t.Fatalf("check with a path: %d %s %s", code, out.String(), errb.String())
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pw, ok := r.BasicAuth(); !ok || pw != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	}))
	defer srv.Close()
	secrets := filepath.Join(d, "secrets")
	_ = os.Mkdir(secrets, 0o700)
	_ = os.WriteFile(filepath.Join(secrets, "git-password"), []byte("s3cret\n"), 0o600)
	good := filepath.Join(d, "good.yml")
	_ = os.WriteFile(good, []byte("modules:\n  repo:\n    prober: git\n    timeout: 5s\n    git: {username: u, password_file: git-password, preferred_ip_protocol: ip4}\n"), 0o600)
	out.Reset()
	errb.Reset()
	if code := run([]string{"check", "-modules", good}, &out, &errb); code != 0 {
		t.Fatalf("check: %d %s", code, errb.String())
	}
	out.Reset()
	if code := run([]string{"run", "-modules", good, "-secrets-dir", secrets, "repo", srv.URL + "/repo.git"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "probe_success 1") {
		t.Fatalf("run with the secret from -secrets-dir: %d %s %s", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"run", "-modules", good, "-secrets-dir", filepath.Join(d, "elsewhere"), "repo", srv.URL + "/repo.git"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "config") {
		t.Fatalf("run without the file: %d %s %s", code, out.String(), errb.String())
	}
}
