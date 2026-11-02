// Package link signs the links to the pages people open from the chat, and checks them. A link carries
// ?s=<expiry>.<signature>: the expiry as Unix seconds, and the base64url HMAC-SHA256, under a key
// Beacon keeps in its database, of the method, the path and the expiry, each followed by a newline.
// Whoever holds a link may open that one page until the link expires, and nothing else; Beacon has no
// login system of its own. The bridge and the SSH channel build links through Pages, the web server
// checks them through Signer.
package link

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// KeySize is the length of a signing key.
	KeySize = 32
	// Param is the query parameter that carries a signature.
	Param = "s"
	// DefaultTTL is how long a link stays valid unless the configuration says otherwise.
	DefaultTTL = 30 * 24 * time.Hour
)

// ErrKey means the key is not KeySize bytes.
var ErrKey = errors.New("the link key must be 32 bytes")

// Signer signs page paths and checks signatures.
type Signer struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

// NewSigner makes a signer with key, whose links last ttl (DefaultTTL when zero). now may be nil.
func NewSigner(key []byte, ttl time.Duration, now func() time.Time) (*Signer, error) {
	if len(key) != KeySize {
		return nil, ErrKey
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Signer{key: append([]byte(nil), key...), ttl: ttl, now: now}, nil
}

// TTL is how long the signer's links last.
func (s *Signer) TTL() time.Duration { return s.ttl }

// Sign returns the signature of method and path, valid for the signer's lifetime.
func (s *Signer) Sign(method, path string) string {
	return s.SignUntil(method, path, s.now().Add(s.ttl))
}

// SignUntil returns the signature of method and path, valid until the second of until.
func (s *Signer) SignUntil(method, path string, until time.Time) string {
	exp := strconv.FormatInt(until.Unix(), 10)
	return exp + "." + base64.RawURLEncoding.EncodeToString(s.mac(method, path, exp))
}

func (s *Signer) mac(method, path, exp string) []byte {
	m := hmac.New(sha256.New, s.key)
	for _, part := range []string{method, path, exp} {
		_, _ = io.WriteString(m, part)
		_, _ = m.Write([]byte{'\n'})
	}
	return m.Sum(nil)
}

// Verify checks sig for method and path and returns when it expires. A signature for another path,
// another method, with a changed expiry or past it fails.
func (s *Signer) Verify(method, path, sig string) (time.Time, bool) {
	exp, enc, ok := strings.Cut(sig, ".")
	if !ok {
		return time.Time{}, false
	}
	n, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || n <= 0 || exp != strconv.FormatInt(n, 10) {
		return time.Time{}, false
	}
	until := time.Unix(n, 0)
	if !s.now().Before(until) {
		return time.Time{}, false
	}
	got, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || !hmac.Equal(got, s.mac(method, path, exp)) {
		return time.Time{}, false
	}
	return until, true
}

// Query returns the query string that carries a signature for a GET of path, without the leading "?".
func (s *Signer) Query(path string) string { return Param + "=" + s.Sign(http.MethodGet, path) }

// Pages builds the links to Beacon's pages: absolute under PublicURL and signed. A nil Pages, one
// without a signer or without a public URL builds none, so that callers can leave links out.
type Pages struct {
	Signer    *Signer
	PublicURL string
}

// idRe is what an id in a page path may look like; anything else gets no link.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// URL is the signed link to the page at path (which begins with "/"), or "".
func (p *Pages) URL(path string) string {
	if p == nil || p.Signer == nil || p.PublicURL == "" || !strings.HasPrefix(path, "/") {
		return ""
	}
	return strings.TrimSuffix(p.PublicURL, "/") + path + "?" + p.Signer.Query(path)
}

// Alert is the link to an alert group's page.
func (p *Pages) Alert(id string) string {
	if !idRe.MatchString(id) {
		return ""
	}
	return p.URL(AlertPath(id))
}

// Session is the link to a session's page, with its replay.
func (p *Pages) Session(id string) string {
	if !idRe.MatchString(id) {
		return ""
	}
	return p.URL(SessionPath(id))
}

// AlertPath is the path of an alert group's page.
func AlertPath(id string) string { return "/alerts/" + id }

// SessionPath is the path of a session's page.
func SessionPath(id string) string { return "/sessions/" + id }

// RecordingPath is the path of a session's recording, which its page plays.
func RecordingPath(id string) string { return "/sessions/" + id + "/recording" }
