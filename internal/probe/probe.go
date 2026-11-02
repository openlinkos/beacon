package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Failure reasons, the reason label of probe_failure_info.
const (
	ReasonDNS        = "dns"         // the name did not resolve
	ReasonConnect    = "connect"     // the connection was refused or reset
	ReasonTimeout    = "timeout"     // no answer in time
	ReasonTLS        = "tls"         // the TLS handshake or the certificate failed
	ReasonNotSSL     = "not_ssl"     // plain HTTP where HTTPS was required
	ReasonHTTPStatus = "http_status" // an unexpected status code
	ReasonBody       = "body"        // the body did not match
	ReasonBanner     = "banner"      // a TCP server did not say what was expected
	ReasonProtocol   = "protocol"    // an answer of the wrong kind (content type, JSON)
	ReasonAuth       = "auth"        // a registry token could not be obtained
	ReasonManifest   = "manifest"    // the registry has no such manifest
	ReasonConfig     = "config"      // the module or target is wrong
)

// Sample is one line of the result: name{labels} value. Type is gauge unless set (counter).
type Sample struct {
	Name, Help, Labels, Type string
	Value                    float64
}

// Result is what a probe found.
type Result struct {
	Success  bool
	Reason   string // why it failed
	Detail   string // the error, for the log; never in metrics (it may hold anything a server said)
	Duration time.Duration
	Samples  []Sample
}

// Prober runs probes. RootCAs is nil for the system's roots and Resolver nil for the system's resolver
// (tests set their own). SecretsDir is the one directory the files modules name (password_file) are read
// from (secrets.go); empty for DefaultSecretsDir.
type Prober struct {
	RootCAs    *x509.CertPool
	Resolver   *net.Resolver
	UserAgent  string
	SecretsDir string
}

// Run probes target with module m, within the module's timeout and ctx.
func (p *Prober) Run(ctx context.Context, m *Module, target string) Result {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.Timeout))
	defer cancel()
	start := time.Now()
	var r Result
	switch m.Prober {
	case "http":
		r = p.http(ctx, m.HTTP, target)
	case "tcp":
		r = p.tcp(ctx, m.TCP, target)
	case "registry":
		r = p.registry(ctx, m.Registry, target)
	case "git":
		r = p.git(ctx, m.Git, target)
	case "rdap":
		r = p.rdap(ctx, m.RDAP, target)
	default:
		r = Result{Reason: ReasonConfig, Detail: "unknown prober " + m.Prober}
	}
	r.Duration = time.Since(start)
	if !r.Success && r.Reason == "" {
		r.Reason = ReasonProtocol
	}
	return r
}

// stepError carries the reason of a failure through the standard library's error wrapping.
type stepError struct {
	reason string
	err    error
}

func (e *stepError) Error() string { return e.reason + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

// classify turns an error from dialling or a request into a reason.
func classify(ctx context.Context, err error) string {
	var se *stepError
	var certErr *tls.CertificateVerificationError
	var recErr tls.RecordHeaderError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var invalid x509.CertificateInvalidError
	var alert tls.AlertError
	switch {
	case errors.As(err, &se):
		return se.reason
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded):
		return ReasonTimeout
	case errors.As(err, &certErr), errors.As(err, &recErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr),
		errors.As(err, &invalid), errors.As(err, &alert):
		return ReasonTLS
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ReasonTimeout
	}
	if strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509:") {
		return ReasonTLS
	}
	return ReasonConnect
}

// dial resolves host itself (to tell a DNS failure from a connection failure, and to keep to one
// address family) and connects.
func (p *Prober) dial(ip IPConfig) func(ctx context.Context, network, addr string) (net.Conn, error) {
	res := p.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, &stepError{ReasonConfig, err}
		}
		first, second := "ip6", "ip4"
		if ip.PreferredIPProtocol == "ip4" {
			first, second = "ip4", "ip6"
		}
		fallback := ip.IPProtocolFallback == nil || *ip.IPProtocolFallback
		var ips []net.IP
		if a := net.ParseIP(host); a != nil {
			ips = []net.IP{a}
		} else {
			ips, err = res.LookupIP(ctx, first, host)
			if (err != nil || len(ips) == 0) && fallback {
				ips, err = res.LookupIP(ctx, second, host)
			}
			if err != nil || len(ips) == 0 {
				if ctx.Err() != nil {
					return nil, &stepError{ReasonTimeout, ctx.Err()}
				}
				if err == nil {
					err = errors.New("no address")
				}
				return nil, &stepError{ReasonDNS, err}
			}
		}
		var d net.Dialer
		var last error
		for _, a := range ips {
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(a.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		if ctx.Err() != nil {
			return nil, &stepError{ReasonTimeout, last}
		}
		return nil, &stepError{ReasonConnect, last}
	}
}

// client is an HTTP client that connects through dial, verifies TLS against the prober's roots, and
// keeps no connection open between probes.
func (p *Prober) client(ip IPConfig, follow bool) *http.Client {
	tr := &http.Transport{
		DialContext:       p.dial(ip),
		TLSClientConfig:   &tls.Config{RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12},
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
	}
	c := &http.Client{Transport: tr}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return c
}

func (p *Prober) request(ctx context.Context, method, url string, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil) //nolint:gosec // G704: requesting the configured target is what a probe is for
	if err != nil {
		return nil, err
	}
	if p.UserAgent != "" {
		req.Header.Set("User-Agent", p.UserAgent)
	}
	for k, v := range headers {
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	return req, nil
}

func certExpiry(cs *tls.ConnectionState) (float64, bool) {
	if cs == nil || len(cs.PeerCertificates) == 0 {
		return 0, false
	}
	earliest := cs.PeerCertificates[0].NotAfter
	for _, c := range cs.PeerCertificates[1:] {
		if c.NotAfter.Before(earliest) {
			earliest = c.NotAfter
		}
	}
	return float64(earliest.Unix()), true
}

func fail(reason string, err error) Result {
	r := Result{Reason: reason}
	if err != nil {
		r.Detail = err.Error()
	}
	return r
}

// drain reads at most n bytes of a body and closes it.
func drain(rc io.ReadCloser, n int64) []byte {
	defer func() { _ = rc.Close() }()
	b, _ := io.ReadAll(io.LimitReader(rc, n))
	return b
}

// ---- Exposition

// help texts for the standard names.
var helps = map[string]string{
	"probe_success":                         "Whether the probe succeeded",
	"probe_duration_seconds":                "How long the probe took",
	"probe_http_status_code":                "The HTTP status code of the (last) response",
	"probe_ssl_earliest_cert_expiry":        "The earliest expiry of the certificates the server presented",
	"probe_failure_info":                    "Why the probe failed (1 for the reason)",
	"probe_domain_expiry_timestamp_seconds": "When the domain expires, from RDAP",
}

// Write puts a result in the Prometheus text format.
func Write(w io.Writer, r Result) error {
	ok := 0.0
	if r.Success {
		ok = 1
	}
	all := append([]Sample{{Name: "probe_success", Value: ok}, {Name: "probe_duration_seconds", Value: r.Duration.Seconds()}}, r.Samples...)
	if !r.Success {
		all = append(all, Sample{Name: "probe_failure_info", Labels: `reason="` + r.Reason + `"`, Value: 1})
	}
	return WriteSamples(w, all)
}

// WriteSamples puts samples in the Prometheus text format, a HELP and TYPE line per name.
func WriteSamples(w io.Writer, samples []Sample) error {
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].Name < samples[j].Name })
	var b strings.Builder
	last := ""
	for _, s := range samples {
		if s.Name != last {
			help := s.Help
			if help == "" {
				help = helps[s.Name]
			}
			typ := s.Type
			if typ == "" {
				typ = "gauge"
			}
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", s.Name, help, s.Name, typ)
			last = s.Name
		}
		b.WriteString(s.Name)
		if s.Labels != "" {
			b.WriteString("{" + s.Labels + "}")
		}
		b.WriteString(" " + strconv.FormatFloat(s.Value, 'g', -1, 64) + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Handler serves /probe?module=…&target=…, like the Blackbox Exporter. modules is read on every request,
// so a reload takes effect. A scrape's own timeout (X-Prometheus-Scrape-Timeout-Seconds) shortens the
// module's, leaving half a second to answer.
func Handler(p *Prober, modules func() *Modules, log func(module, target string, r Result)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, target := r.URL.Query().Get("module"), r.URL.Query().Get("target")
		m := modules().Modules[name]
		if m == nil || target == "" {
			http.Error(w, "need a known module and a target", http.StatusBadRequest)
			return
		}
		mod := *m
		if s, err := strconv.ParseFloat(r.Header.Get("X-Prometheus-Scrape-Timeout-Seconds"), 64); err == nil && s > 1 {
			if t := time.Duration((s - 0.5) * float64(time.Second)); t < time.Duration(mod.Timeout) {
				mod.Timeout = Duration(t)
			}
		}
		res := p.Run(r.Context(), &mod, target)
		if log != nil && !res.Success {
			log(name, target, res)
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_ = Write(w, res)
	})
}

// Reloadable holds the module file and swaps it on reload.
type Reloadable struct{ p atomic.Pointer[Modules] }

// Load returns the current modules.
func (r *Reloadable) Load() *Modules { return r.p.Load() }

// Store sets the modules.
func (r *Reloadable) Store(m *Modules) { r.p.Store(m) }
