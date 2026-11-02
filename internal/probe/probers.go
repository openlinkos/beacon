package probe

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func statusSample(code int) Sample {
	return Sample{Name: "probe_http_status_code", Value: float64(code)}
}

// ---- http

func (p *Prober) http(ctx context.Context, m HTTPModule, target string) Result {
	follow := m.FollowRedirects == nil || *m.FollowRedirects
	req, err := p.request(ctx, m.Method, target, m.Headers)
	if err != nil {
		return fail(ReasonConfig, err)
	}
	resp, err := p.client(m.IPConfig, follow).Do(req) //nolint:gosec,bodyclose // G704: requesting the configured target is what a probe is for; drain closes the body
	if err != nil {
		return fail(classify(ctx, err), err)
	}
	body := drain(resp.Body, 1<<20)
	r := Result{Samples: []Sample{statusSample(resp.StatusCode)}}
	if exp, ok := certExpiry(resp.TLS); ok {
		r.Samples = append(r.Samples, Sample{Name: "probe_ssl_earliest_cert_expiry", Value: exp})
	}
	switch {
	case m.FailIfNotSSL && resp.TLS == nil:
		r.Reason = ReasonNotSSL
	case len(m.ValidStatusCodes) > 0 && !slices.Contains(m.ValidStatusCodes, resp.StatusCode),
		len(m.ValidStatusCodes) == 0 && (resp.StatusCode < 200 || resp.StatusCode > 299):
		r.Reason, r.Detail = ReasonHTTPStatus, resp.Status
	default:
		r.Success = true
		for _, re := range m.bodyRe {
			if !re.Match(body) {
				r.Success, r.Reason, r.Detail = false, ReasonBody, "body does not match "+re.String()
				break
			}
		}
	}
	return r
}

// ---- tcp

func (p *Prober) tcp(ctx context.Context, m TCPModule, target string) Result {
	conn, err := p.dial(m.IPConfig)(ctx, "tcp", target)
	if err != nil {
		return fail(classify(ctx, err), err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	var r Result
	if m.TLS {
		host, _, _ := net.SplitHostPort(target)
		tc := tls.Client(conn, &tls.Config{ServerName: host, RootCAs: p.RootCAs, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			return fail(classify(ctx, err), err)
		}
		cs := tc.ConnectionState()
		if exp, ok := certExpiry(&cs); ok {
			r.Samples = append(r.Samples, Sample{Name: "probe_ssl_earliest_cert_expiry", Value: exp})
		}
		conn = tc
	}
	rd := bufio.NewReader(conn)
	for _, q := range m.QueryResponse {
		if q.Send != "" {
			if _, err := fmt.Fprintf(conn, "%s\n", q.Send); err != nil {
				return fail(classify(ctx, err), err)
			}
		}
		if q.re == nil {
			continue
		}
		matched := false
		for !matched {
			line, err := rd.ReadString('\n')
			if q.re.MatchString(strings.TrimRight(line, "\r\n")) {
				matched = true
				break
			}
			if err != nil {
				reason := ReasonBanner
				if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
					reason = ReasonTimeout
				}
				return fail(reason, fmt.Errorf("no line matched %s: %w", q.Expect, err))
			}
		}
	}
	r.Success = true
	return r
}

// ---- registry

// manifestTypes are what a client accepts for a manifest (an index first, as docker and containerd do).
var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

var challengeRe = regexp.MustCompile(`(\w+)="([^"]*)"`)

func (p *Prober) registry(ctx context.Context, m RegistryModule, target string) Result {
	base := strings.TrimSuffix(target, "/")
	password, err := p.secret("registry.password_file", m.PasswordFile)
	if err != nil {
		return fail(ReasonConfig, err)
	}
	c := p.client(m.IPConfig, true)
	req, err := p.request(ctx, http.MethodGet, base+"/v2/", nil)
	if err != nil {
		return fail(ReasonConfig, err)
	}
	resp, err := c.Do(req) //nolint:gosec,bodyclose // G704: requesting the configured target is what a probe is for; drain closes the body
	if err != nil {
		return fail(classify(ctx, err), err)
	}
	drain(resp.Body, 64<<10)
	r := Result{}
	if exp, ok := certExpiry(resp.TLS); ok {
		r.Samples = append(r.Samples, Sample{Name: "probe_ssl_earliest_cert_expiry", Value: exp})
	}
	var auth string
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		ch := resp.Header.Get("WWW-Authenticate")
		scheme, params, _ := strings.Cut(ch, " ")
		switch strings.ToLower(scheme) {
		case "basic":
			if m.Username == "" {
				return withStatus(r, ReasonAuth, resp.StatusCode, "the registry wants credentials")
			}
			req.SetBasicAuth(m.Username, password)
			auth = req.Header.Get("Authorization")
		case "bearer":
			tok, err := p.registryToken(ctx, c, params, m, password)
			if err != nil {
				return withStatus(r, ReasonAuth, resp.StatusCode, err.Error())
			}
			auth = "Bearer " + tok
		default:
			return withStatus(r, ReasonAuth, resp.StatusCode, "unknown challenge "+ch)
		}
	default:
		return withStatus(r, ReasonHTTPStatus, resp.StatusCode, resp.Status)
	}
	req, err = p.request(ctx, http.MethodHead, base+"/v2/"+m.Repository+"/manifests/"+m.Reference, map[string]string{"Accept": manifestTypes})
	if err != nil {
		return fail(ReasonConfig, err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err = c.Do(req) //nolint:gosec,bodyclose // G704: requesting the configured target is what a probe is for; drain closes the body
	if err != nil {
		return fail(classify(ctx, err), err)
	}
	drain(resp.Body, 64<<10)
	if resp.StatusCode != http.StatusOK {
		reason := ReasonManifest
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			reason = ReasonAuth
		}
		return withStatus(r, reason, resp.StatusCode, resp.Status)
	}
	r.Samples = append(r.Samples, statusSample(resp.StatusCode))
	r.Success = true
	return r
}

func withStatus(r Result, reason string, code int, detail string) Result {
	r.Reason, r.Detail = reason, detail
	r.Samples = append(r.Samples, statusSample(code))
	return r
}

// registryToken gets a pull token from the realm a Bearer challenge names.
func (p *Prober) registryToken(ctx context.Context, c *http.Client, params string, m RegistryModule, password string) (string, error) {
	ch := map[string]string{}
	for _, kv := range challengeRe.FindAllStringSubmatch(params, -1) {
		ch[kv[1]] = kv[2]
	}
	realm, err := url.Parse(ch["realm"])
	if err != nil || ch["realm"] == "" || (realm.Scheme != "https" && realm.Scheme != "http") {
		return "", errors.New("the challenge has no usable realm")
	}
	q := realm.Query()
	if ch["service"] != "" {
		q.Set("service", ch["service"])
	}
	q.Set("scope", "repository:"+m.Repository+":pull")
	realm.RawQuery = q.Encode()
	req, err := p.request(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	if m.Username != "" {
		req.SetBasicAuth(m.Username, password)
	}
	resp, err := c.Do(req) //nolint:bodyclose // drain closes the body
	if err != nil {
		return "", err
	}
	body := drain(resp.Body, 1<<20)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the token service answered %s", resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("the token service's answer is not JSON: %w", err)
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" {
		return "", errors.New("the token service gave no token")
	}
	return t.Token, nil
}

// ---- git

func (p *Prober) git(ctx context.Context, m GitModule, target string) Result {
	password, err := p.secret("git.password_file", m.PasswordFile)
	if err != nil {
		return fail(ReasonConfig, err)
	}
	req, err := p.request(ctx, http.MethodGet, strings.TrimSuffix(target, "/")+"/info/refs?service=git-upload-pack", nil)
	if err != nil {
		return fail(ReasonConfig, err)
	}
	if m.Username != "" {
		req.SetBasicAuth(m.Username, password)
	}
	resp, err := p.client(m.IPConfig, true).Do(req) //nolint:gosec,bodyclose // G704: requesting the configured target is what a probe is for; drain closes the body
	if err != nil {
		return fail(classify(ctx, err), err)
	}
	drain(resp.Body, 1<<20)
	r := Result{Samples: []Sample{statusSample(resp.StatusCode)}}
	if exp, ok := certExpiry(resp.TLS); ok {
		r.Samples = append(r.Samples, Sample{Name: "probe_ssl_earliest_cert_expiry", Value: exp})
	}
	switch {
	case resp.StatusCode != http.StatusOK:
		r.Reason, r.Detail = ReasonHTTPStatus, resp.Status
	case resp.Header.Get("Content-Type") != "application/x-git-upload-pack-advertisement":
		r.Reason, r.Detail = ReasonProtocol, "content type "+resp.Header.Get("Content-Type")
	default:
		r.Success = true
	}
	return r
}

// ---- rdap

func (p *Prober) rdap(ctx context.Context, m RDAPModule, domain string) Result {
	if domain == "" || strings.ContainsAny(domain, "/?#: ") {
		return fail(ReasonConfig, fmt.Errorf("%q is not a domain", domain))
	}
	req, err := p.request(ctx, http.MethodGet, strings.TrimSuffix(m.Server, "/")+"/domain/"+url.PathEscape(domain), map[string]string{"Accept": "application/rdap+json"})
	if err != nil {
		return fail(ReasonConfig, err)
	}
	resp, err := p.client(m.IPConfig, true).Do(req) //nolint:gosec,bodyclose // G704: requesting the configured target is what a probe is for; drain closes the body
	if err != nil {
		return fail(classify(ctx, err), err)
	}
	body := drain(resp.Body, 4<<20)
	r := Result{Samples: []Sample{statusSample(resp.StatusCode)}}
	if resp.StatusCode != http.StatusOK {
		r.Reason, r.Detail = ReasonHTTPStatus, resp.Status
		return r
	}
	var doc struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		r.Reason, r.Detail = ReasonProtocol, err.Error()
		return r
	}
	for _, e := range doc.Events {
		if e.Action != "expiration" {
			continue
		}
		t, err := time.Parse(time.RFC3339, e.Date)
		if err != nil {
			r.Reason, r.Detail = ReasonProtocol, "expiration date "+strconv.Quote(e.Date)
			return r
		}
		r.Samples = append(r.Samples, Sample{Name: "probe_domain_expiry_timestamp_seconds", Value: float64(t.Unix())})
		r.Success = true
		return r
	}
	r.Reason, r.Detail = ReasonProtocol, "no expiration event"
	return r
}
