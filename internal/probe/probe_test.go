package probe

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The module file the ops repository uses with the Blackbox Exporter, shortened.
const blackboxFile = `
modules:
  http_head:
    prober: http
    timeout: 20s
    http:
      method: HEAD
      valid_status_codes: [200]
      follow_redirects: false
      fail_if_not_ssl: true
      preferred_ip_protocol: ip4
      ip_protocol_fallback: false
      headers:
        Connection: close
        User-Agent: Beacon-Probe/1.0
  http_get_gitea:
    prober: http
    timeout: 20s
    http:
      method: GET
      valid_status_codes: [200]
      follow_redirects: false
      fail_if_not_ssl: true
      fail_if_body_not_matches_regexp: ['"status":\s*"pass"']
      preferred_ip_protocol: ip4
      ip_protocol_fallback: false
  tcp_ssh:
    prober: tcp
    timeout: 15s
    tcp:
      preferred_ip_protocol: ip4
      ip_protocol_fallback: false
      query_response:
        - expect: '^SSH-2\.0-'
  mirror:
    prober: registry
    timeout: 20s
    registry: {repository: library/busybox, preferred_ip_protocol: ip4}
  repo:
    prober: git
  domain:
    prober: rdap
`

func modules(t *testing.T) *Modules {
	t.Helper()
	ms, err := ParseModules([]byte(blackboxFile))
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestParseModules(t *testing.T) {
	ms := modules(t)
	if len(ms.Modules) != 6 || ms.Modules["mirror"].Registry.Reference != "latest" || ms.Modules["domain"].RDAP.Server != "https://rdap.org" {
		t.Fatalf("modules %+v", ms.Modules)
	}
	for name, bad := range map[string]string{
		"unknown prober": "modules: {x: {prober: icmp}}",
		"unknown field":  "modules: {x: {prober: http, http: {method: GET, fail_if_ssl: true}}}",
		"bad regexp":     "modules: {x: {prober: http, http: {fail_if_body_not_matches_regexp: ['(']}}}",
		"registry repo":  "modules: {x: {prober: registry}}",
		"ip protocol":    "modules: {x: {prober: tcp, tcp: {preferred_ip_protocol: ip5}}}",
	} {
		if _, err := ParseModules([]byte(bad)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// tlsEnv is an HTTPS server and a prober that trusts it.
func tlsEnv(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Prober) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, &Prober{RootCAs: pool}
}

func run(p *Prober, ms *Modules, module, target string) Result {
	return p.Run(context.Background(), ms.Modules[module], target)
}

func TestHTTP(t *testing.T) {
	ms := modules(t)
	srv, p := tlsEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"status": "pass"}`))
		case "/fail":
			_, _ = w.Write([]byte(`{"status": "fail"}`))
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/slow":
			time.Sleep(2 * time.Second)
		}
	})
	r := run(p, ms, "http_get_gitea", srv.URL+"/ok")
	if !r.Success || !has(r, "probe_http_status_code", 200) || !hasName(r, "probe_ssl_earliest_cert_expiry") {
		t.Fatalf("ok: %+v", r)
	}
	if r := run(p, ms, "http_get_gitea", srv.URL+"/fail"); r.Success || r.Reason != ReasonBody {
		t.Errorf("body: %+v", r)
	}
	// redirects are not followed: 302 is not 200
	if r := run(p, ms, "http_head", srv.URL+"/moved"); r.Success || r.Reason != ReasonHTTPStatus || !has(r, "probe_http_status_code", 302) {
		t.Errorf("redirect: %+v", r)
	}
	// an untrusted certificate
	if r := (&Prober{}).Run(context.Background(), ms.Modules["http_head"], srv.URL+"/ok"); r.Success || r.Reason != ReasonTLS {
		t.Errorf("untrusted: %+v", r)
	}
	// plain HTTP where HTTPS is required
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer plain.Close()
	if r := run(p, ms, "http_head", plain.URL); r.Success || r.Reason != ReasonNotSSL {
		t.Errorf("not ssl: %+v", r)
	}
	// timeout
	m := *ms.Modules["http_head"]
	m.Timeout = Duration(300 * time.Millisecond)
	if r := p.Run(context.Background(), &m, srv.URL+"/slow"); r.Success || r.Reason != ReasonTimeout {
		t.Errorf("timeout: %+v", r)
	}
	// connection refused
	ln, _ := net.Listen("tcp4", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	if r := run(p, ms, "http_head", "https://"+addr+"/"); r.Success || r.Reason != ReasonConnect {
		t.Errorf("refused: %+v", r)
	}
	// a name that does not resolve
	noDNS := &Prober{RootCAs: p.RootCAs, Resolver: &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("no DNS here")
	}}}
	if r := run(noDNS, ms, "http_head", "https://nothing.invalid/"); r.Success || r.Reason != ReasonDNS {
		t.Errorf("dns: %+v", r)
	}
}

func has(r Result, name string, v float64) bool {
	for _, s := range r.Samples {
		if s.Name == name && s.Value == v {
			return true
		}
	}
	return false
}

func hasName(r Result, name string) bool {
	for _, s := range r.Samples {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestTCP(t *testing.T) {
	ms := modules(t)
	serve := func(banner string) string {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_, _ = fmt.Fprint(c, banner)
				_ = c.Close()
			}
		}()
		return ln.Addr().String()
	}
	p := &Prober{}
	if r := run(p, ms, "tcp_ssh", serve("SSH-2.0-OpenSSH_9.2\r\n")); !r.Success {
		t.Errorf("ssh: %+v", r)
	}
	if r := run(p, ms, "tcp_ssh", serve("HTTP/1.1 400 Bad Request\r\n")); r.Success || r.Reason != ReasonBanner {
		t.Errorf("not ssh: %+v", r)
	}
}

func TestRegistry(t *testing.T) {
	ms := modules(t)
	var srvURL string
	srv, p := tlsEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/":
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srvURL+`/token",service="registry.test"`)
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") != "repository:library/busybox:pull" || r.URL.Query().Get("service") != "registry.test" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"token":"t0k"}`))
		case r.URL.Path == "/v2/library/busybox/manifests/latest" && r.Method == http.MethodHead:
			if r.Header.Get("Authorization") != "Bearer t0k" || !strings.Contains(r.Header.Get("Accept"), "oci.image.index") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srvURL = srv.URL
	if r := run(p, ms, "mirror", srv.URL); !r.Success || !has(r, "probe_http_status_code", 200) {
		t.Fatalf("registry: %+v", r)
	}
	m := *ms.Modules["mirror"]
	m.Registry.Repository = "library/nothing"
	if r := p.Run(context.Background(), &m, srv.URL); r.Success || r.Reason != ReasonAuth && r.Reason != ReasonManifest {
		t.Errorf("unknown repository: %+v", r)
	}
}

func TestGitAndRDAP(t *testing.T) {
	ms := modules(t)
	srv, p := tlsEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/org/repo.git/info/refs" && r.URL.Query().Get("service") == "git-upload-pack":
			w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
			_, _ = w.Write([]byte("001e# service=git-upload-pack\n"))
		case r.URL.Path == "/login.git/info/refs":
			_, _ = w.Write([]byte("<html>"))
		case r.URL.Path == "/domain/linkos.io":
			_, _ = w.Write([]byte(`{"events":[{"eventAction":"registration","eventDate":"2024-01-01T00:00:00Z"},{"eventAction":"expiration","eventDate":"2027-03-04T05:06:07Z"}]}`))
		case r.URL.Path == "/domain/nodate.io":
			_, _ = w.Write([]byte(`{"events":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if r := run(p, ms, "repo", srv.URL+"/org/repo.git"); !r.Success {
		t.Errorf("git: %+v", r)
	}
	if r := run(p, ms, "repo", srv.URL+"/login.git"); r.Success || r.Reason != ReasonProtocol {
		t.Errorf("git page: %+v", r)
	}
	m := *ms.Modules["domain"]
	m.RDAP.Server = srv.URL
	exp, _ := time.Parse(time.RFC3339, "2027-03-04T05:06:07Z")
	if r := p.Run(context.Background(), &m, "linkos.io"); !r.Success || !has(r, "probe_domain_expiry_timestamp_seconds", float64(exp.Unix())) {
		t.Errorf("rdap: %+v", r)
	}
	if r := p.Run(context.Background(), &m, "nodate.io"); r.Success || r.Reason != ReasonProtocol {
		t.Errorf("rdap without expiry: %+v", r)
	}
	if r := p.Run(context.Background(), &m, "a/b"); r.Success || r.Reason != ReasonConfig {
		t.Errorf("not a domain: %+v", r)
	}
}

func TestHandlerAndWrite(t *testing.T) {
	ms := modules(t)
	srv, p := tlsEnv(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	var logged string
	h := Handler(p, func() *Modules { return ms }, func(module, target string, r Result) { logged = module + " " + r.Reason })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/probe?module=http_head&target="+srv.URL, nil)
	req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", "10")
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{"# TYPE probe_success gauge\nprobe_success 0\n", "probe_http_status_code 503\n", `probe_failure_info{reason="http_status"} 1`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	if logged != "http_head http_status" {
		t.Errorf("logged %q", logged)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/probe?module=nope&target=x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown module: %d", rec.Code)
	}
}

// TestSecretsDir: a module's password_file is a file name in the probe's secrets directory; a path, an
// absolute one most of all, is refused before anything is read, with the reason config.
func TestSecretsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git-password"), []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("leak"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Prober{SecretsDir: dir}
	if pw, err := p.secret("git.password_file", "git-password"); err != nil || pw != "s3cret" {
		t.Fatalf("a name in the directory: %q %v", pw, err)
	}
	if pw, err := p.secret("git.password_file", ""); err != nil || pw != "" {
		t.Fatalf("no file: %q %v", pw, err)
	}
	if _, err := p.secret("git.password_file", "missing"); err == nil || !strings.Contains(err.Error(), "git.password_file") {
		t.Errorf("a missing file: %v", err)
	}
	for _, bad := range []string{outside, "/etc/passwd", `C:\secrets\x`, "../" + filepath.Base(outside), "..", "a/b", `a\b`, ".", "x\ny"} {
		_, err := p.secret("git.password_file", bad)
		if err == nil || !strings.Contains(err.Error(), "secrets directory") || !strings.Contains(err.Error(), "-secrets-dir") {
			t.Errorf("%q: %v", bad, err)
		}
		if err := SecretName("registry.password_file", bad); err == nil {
			t.Errorf("SecretName(%q) accepted", bad)
		}
	}
	if (&Prober{}).SecretsDir != "" || DefaultSecretsDir != "/etc/beacon-probe/secrets" {
		t.Error("the default directory")
	}

	// the module file's checks for the centre: every module's file fields
	ms, err := ParseModules([]byte("modules:\n  a: {prober: git, git: {username: u, password_file: /etc/passwd}}\n  b: {prober: registry, registry: {repository: r, password_file: ../x}}\n  c: {prober: git, git: {password_file: fine}}\n"))
	if err != nil {
		t.Fatalf("an older gateway's file still parses: %v", err)
	}
	err = CheckSecretFiles(ms)
	if err == nil || !strings.Contains(err.Error(), "module a: git.password_file") || !strings.Contains(err.Error(), "module b: registry.password_file") || strings.Contains(err.Error(), "module c") {
		t.Fatalf("CheckSecretFiles: %v", err)
	}

	// at probe time: the git prober sends the password from the directory, and refuses a path
	srv, tp := tlsEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if u, pw, ok := r.BasicAuth(); !ok || u != "u" || pw != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
	})
	tp.SecretsDir = dir
	m := &Module{Prober: "git", Timeout: Duration(5 * time.Second), Git: GitModule{Username: "u", PasswordFile: "git-password"}}
	if r := tp.Run(context.Background(), m, srv.URL+"/repo.git"); !r.Success {
		t.Errorf("with the password from the secrets directory: %+v", r)
	}
	m.Git.PasswordFile = outside
	if r := tp.Run(context.Background(), m, srv.URL+"/repo.git"); r.Success || r.Reason != ReasonConfig || !strings.Contains(r.Detail, "secrets directory") {
		t.Errorf("a path is refused before anything is read: %+v", r)
	}
	reg := &Module{Prober: "registry", Timeout: Duration(5 * time.Second), Registry: RegistryModule{Repository: "r", PasswordFile: "/etc/passwd"}}
	if r := tp.Run(context.Background(), reg, srv.URL); r.Success || r.Reason != ReasonConfig || !strings.Contains(r.Detail, "registry.password_file") {
		t.Errorf("the registry prober too: %+v", r)
	}
}
