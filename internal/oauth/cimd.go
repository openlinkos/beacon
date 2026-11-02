package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

// Client ID metadata documents: a client whose client_id is an https URL publishes there what it is and
// where it may be redirected, so that it needs no registration (Codex and Claude Code use them). Beacon
// fetches the document with a short timeout and a size limit, never from a private address (whatever
// the name resolves to when it connects, so that DNS cannot point it inside), follows at most three
// redirects, all https, and keeps what it found for ten minutes.

const (
	docTTL     = 10 * time.Minute
	docFailTTL = time.Minute
	maxDoc     = 64 << 10
	maxDocs    = 200
	maxDocURL  = 512
)

type cached struct {
	client store.OAuthClient
	err    error
	at     time.Time
}

// checkDocURL says what is wrong with a client_id that is a URL, or nil.
func checkDocURL(id string) error {
	if len(id) > maxDocURL {
		return errors.New("a client ID URL is at most 512 characters")
	}
	u, err := url.Parse(id)
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "":
		return errors.New("a client ID URL is an absolute https URL")
	case u.Path == "" || u.Path == "/":
		return errors.New("a client ID URL has a path")
	case u.Fragment != "" || u.User != nil:
		return errors.New("a client ID URL has no fragment and no credentials")
	}
	return nil
}

// document returns the client a client ID metadata document describes.
func (s *Server) document(ctx context.Context, id string) (store.OAuthClient, error) {
	if err := checkDocURL(id); err != nil {
		return store.OAuthClient{}, err
	}
	now := s.now()
	s.mu.Lock()
	c, ok := s.docs[id]
	s.mu.Unlock()
	if ok && (c.err == nil && now.Sub(c.at) < docTTL || c.err != nil && now.Sub(c.at) < docFailTTL) {
		return c.client, c.err
	}
	client, err := s.fetchDocument(ctx, id)
	s.mu.Lock()
	if s.docs == nil || len(s.docs) >= maxDocs {
		s.docs = map[string]cached{}
	}
	s.docs[id] = cached{client: client, err: err, at: now}
	s.mu.Unlock()
	return client, err
}

func (s *Server) fetchDocument(ctx context.Context, id string) (store.OAuthClient, error) {
	hc := s.Fetch
	if hc == nil {
		hc = fetcher
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return store.OAuthClient{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return store.OAuthClient{}, fmt.Errorf("its metadata document could not be fetched: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return store.OAuthClient{}, fmt.Errorf("its metadata document answered %s", resp.Status)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "application/json" {
		return store.OAuthClient{}, errors.New("its metadata document is not application/json")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDoc+1))
	if err != nil {
		return store.OAuthClient{}, err
	}
	if len(body) > maxDoc {
		return store.OAuthClient{}, errors.New("its metadata document is larger than 64 KiB")
	}
	var doc struct {
		ClientID                string   `json:"client_id"`
		ClientName              string   `json:"client_name"`
		ClientURI               string   `json:"client_uri"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return store.OAuthClient{}, errors.New("its metadata document is not valid JSON")
	}
	switch {
	case doc.ClientID != id:
		return store.OAuthClient{}, errors.New("its metadata document names another client_id")
	case len(doc.RedirectURIs) == 0 || len(doc.RedirectURIs) > maxRedirectURIs:
		return store.OAuthClient{}, errors.New("its metadata document lists no redirect_uris, or too many")
	case doc.TokenEndpointAuthMethod != "" && doc.TokenEndpointAuthMethod != "none":
		return store.OAuthClient{}, errors.New("it is not a public client (token_endpoint_auth_method none)")
	}
	for _, u := range doc.RedirectURIs {
		if err := CheckRedirect(u); err != nil {
			return store.OAuthClient{}, err
		}
	}
	name := cleanName(doc.ClientName)
	if name == "" {
		name = hostOf(id)
	}
	clientURI := ""
	if u, err := url.Parse(doc.ClientURI); err == nil && u.Scheme == "https" && len(doc.ClientURI) <= maxURL {
		clientURI = doc.ClientURI
	}
	return store.OAuthClient{ID: id, Name: name, RedirectURIs: doc.RedirectURIs, URI: clientURI, Created: s.now()}, nil
}

// blocked are the address ranges that are not on the public internet beyond what netip already knows
// (private, loopback, link-local, multicast, unspecified).
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// Public reports whether a is an address on the public internet.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// fetcher fetches documents only from public addresses, checked when each connection is made, without
// a proxy from the environment.
var fetcher = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(host)
			if err != nil || !Public(a) {
				return fmt.Errorf("%s is not a public address", host)
			}
			return nil
		}}).DialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		MaxIdleConns:           10,
		IdleConnTimeout:        time.Minute,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("a redirect away from https")
		}
		return nil
	},
}
