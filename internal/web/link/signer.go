package link

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
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
	key        []byte
	ttl        time.Duration
	now        func() time.Time
	generation func(kind, id string) (uint64, error)
}

// NewSigner makes a signer for exact paths without a revocation store. now may be nil.
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

// NewRevocableSigner makes a signer for alert and session paths backed by a durable generation.
func NewRevocableSigner(key []byte, ttl time.Duration, now func() time.Time, generation func(kind, id string) (uint64, error)) (*Signer, error) {
	s, err := NewSigner(key, ttl, now)
	if err != nil {
		return nil, err
	}
	if generation == nil {
		return nil, errors.New("a revocable signer needs a page link generation lookup")
	}
	s.generation = generation
	return s, nil
}

// TTL is how long the signer's links last.
func (s *Signer) TTL() time.Duration { return s.ttl }

// Sign returns the signature of method and path, valid for the signer's lifetime.
func (s *Signer) Sign(method, path string) string {
	return s.SignUntil(method, path, s.now().Add(s.ttl))
}

// SignUntil returns a new signature valid until the second of until. If a generation lookup is
// required and fails, or if the path is outside the supported resources, it returns an empty string.
func (s *Signer) SignUntil(method, path string, until time.Time) string {
	expiry := strconv.FormatInt(until.Unix(), 10)
	if s.generation == nil {
		return s.sign(method, path, expiry, "")
	}
	resource := pageResource(path)
	generation, ok := s.current(resource)
	if !ok {
		return ""
	}
	return s.sign(method, path, expiry, strconv.FormatUint(generation, 10))
}

func (s *Signer) sign(method, path, expiry, generation string) string {
	mac := base64.RawURLEncoding.EncodeToString(s.mac(method, path, expiry, generation))
	if generation == "" {
		return expiry + "." + mac
	}
	return expiry + "." + generation + "." + mac
}

func (s *Signer) mac(method, path, expiry, generation string) []byte {
	m := hmac.New(sha256.New, s.key)
	if generation != "" {
		_, _ = io.WriteString(m, "beacon signed page v2\n")
	}
	for _, part := range []string{method, path, expiry} {
		_, _ = io.WriteString(m, part)
		_, _ = m.Write([]byte{'\n'})
	}
	if generation != "" {
		_, _ = io.WriteString(m, generation+"\n")
	}
	return m.Sum(nil)
}

type resource struct{ kind, id string }

// pageResource recognises only supported page and recording paths. The root aliases and canonical
// paths of a resource share a generation, but their exact paths stay independently authenticated.
func pageResource(path string) resource {
	relative, canonical := strings.CutPrefix(path, Root+"/")
	if !canonical {
		var rooted bool
		relative, rooted = strings.CutPrefix(path, "/")
		if !rooted {
			return resource{}
		}
	}
	parts := strings.Split(relative, "/")
	if len(parts) < 2 || !idRe.MatchString(parts[1]) {
		return resource{}
	}
	switch {
	case parts[0] == "alerts" && (len(parts) == 2 ||
		canonical && len(parts) == 4 && parts[2] == "episodes" && ValidEpisode(parts[3])):
		return resource{"alert", parts[1]}
	case parts[0] == "sessions" && (len(parts) == 2 || len(parts) == 3 && parts[2] == "recording"):
		return resource{"session", parts[1]}
	default:
		return resource{}
	}
}

func (s *Signer) current(r resource) (uint64, bool) {
	if s.generation == nil {
		return 0, true
	}
	if r.kind == "" {
		return 0, false
	}
	generation, err := s.generation(r.kind, r.id)
	return generation, err == nil
}

// Grant represents one verified signed request. Its authenticated limits are kept privately for
// deriving a canonical redirect or a recording link; Until is available to the page renderer.
type Grant struct {
	Until      time.Time
	signer     *Signer
	resource   resource
	method     string
	path       string
	expiry     int64
	generation uint64
}

// VerifyGrant checks a signature and the resource's current generation. Legacy two-part signatures
// authenticate generation zero and therefore stop working when the resource is first revoked.
func (s *Signer) VerifyGrant(method, path, sig string) (Grant, bool) {
	expiry, rest, ok := strings.Cut(sig, ".")
	if !ok {
		return Grant{}, false
	}
	unix, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || unix <= 0 || expiry != strconv.FormatInt(unix, 10) {
		return Grant{}, false
	}
	until := time.Unix(unix, 0).UTC()
	if !s.now().Before(until) {
		return Grant{}, false
	}
	var genText string
	var gen uint64
	encoded := rest
	if field, mac, versioned := strings.Cut(rest, "."); versioned {
		if s.generation == nil {
			return Grant{}, false
		}
		gen, err = strconv.ParseUint(field, 10, 64)
		if err != nil || field != strconv.FormatUint(gen, 10) {
			return Grant{}, false
		}
		genText, encoded = field, mac
	}
	if len(encoded) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return Grant{}, false
	}
	received, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || !hmac.Equal(received, s.mac(method, path, expiry, genText)) {
		return Grant{}, false
	}
	r := pageResource(path)
	if current, ok := s.current(r); !ok || current != gen {
		return Grant{}, false
	}
	return Grant{Until: until, signer: s, resource: r, method: method, path: path, expiry: unix, generation: gen}, true
}

// Verify checks sig for method and path and returns when it expires.
func (s *Signer) Verify(method, path, sig string) (time.Time, bool) {
	grant, ok := s.VerifyGrant(method, path, sig)
	return grant.Until, ok
}

// Derive authenticates the same page, its supported canonical move, or the recording needed by
// its session replay. Derived links keep the verified expiry and generation, even if the resource
// is revoked between this check and use of the derived URL.
func (s *Signer) Derive(method, target string, grant Grant) string {
	r := pageResource(target)
	if grant.signer != s || grant.method != method || r.kind == "" || r != grant.resource ||
		!s.now().Before(time.Unix(grant.expiry, 0)) {
		return ""
	}
	moved, _ := Moved(grant.path)
	replay := r.kind == "session" && (grant.path == SessionPath(r.id) || moved == SessionPath(r.id))
	recording := replay && target == RecordingPath(r.id)
	if target != grant.path && target != moved && !recording {
		return ""
	}
	current, ok := s.current(r)
	if !ok || current != grant.generation {
		return ""
	}
	generation := ""
	if s.generation != nil {
		generation = strconv.FormatUint(grant.generation, 10)
	}
	return s.sign(method, target, strconv.FormatInt(grant.expiry, 10), generation)
}

// Query returns the query string that signs a GET of path, or an empty string on lookup failure.
func (s *Signer) Query(path string) string {
	sig := s.Sign(http.MethodGet, path)
	if sig == "" {
		return ""
	}
	return Param + "=" + sig
}
