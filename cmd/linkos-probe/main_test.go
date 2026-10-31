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
