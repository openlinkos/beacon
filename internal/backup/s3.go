package backup

// A smallest-possible S3 client for backup destinations: PUT, GET, DELETE and LIST over any
// S3-compatible store (MinIO, R2, S3 itself), with SigV4 signing from the standard library. The
// AWS SDK would add a dependency the whole service carries for four calls; path-style addressing
// (endpoint/bucket/key) is what the S3-compatible stores share. Credentials are read from the
// files the configuration names, at use, so a rotated key needs no restart and nothing secret is
// held longer than a request.
//
// Uploads stream: the request carries x-amz-content-sha256: UNSIGNED-PAYLOAD (accepted by S3
// over HTTPS, MinIO and R2 alike), and the archive's integrity is checked by Beacon's own
// after-upload read-back — size and SHA-256, as the plan's §11 asks — rather than by the
// transport's signature.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/version"
)

// timeNow is the clock, one place for tests to move.
var timeNow = time.Now

// S3 is one bucket on one S3-compatible store, addressed path-style. Region is what signs the
// request (us-east-1 when the store ignores regions, as MinIO does); AccessKeyFile and
// SecretKeyFile hold the credentials.
type S3 struct {
	Endpoint      string
	Region        string
	Bucket        string
	Prefix        string
	AccessKeyFile string
	SecretKeyFile string

	hc *http.Client
}

// key is the object's full key inside the bucket.
func (s *S3) key(name string) string { return s.Prefix + name }

// creds reads the credential files.
func (s *S3) creds() (access, secret string, err error) {
	if s.AccessKeyFile == "" || s.SecretKeyFile == "" {
		return "", "", fmt.Errorf("s3 destination: access_key_file and secret_key_file are both needed")
	}
	a, err := os.ReadFile(s.AccessKeyFile)
	if err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(s.SecretKeyFile)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(a)), strings.TrimSpace(string(b)), nil
}

// request is one signed call, ready to run.
type request struct {
	req    *http.Request
	client *http.Client
}

// build signs a request for objectKey (the full key inside the bucket; empty for the bucket
// root, as a listing asks). body may be nil (nothing to send) or a reader of size bytes.
// payloadHash is either the hex SHA-256 of what is sent or UNSIGNED-PAYLOAD for a stream.
func (s *S3) build(ctx context.Context, method, objectKey string, query url.Values, body io.Reader, size int64, payloadHash string, headers map[string]string) (*request, error) {
	access, secret, err := s.creds()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimSuffix(s.Endpoint, "/") + "/" + s.Bucket + "/" + objectKey)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("User-Agent", version.UserAgent())
	hc := s.hc
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Minute}
	}
	return &request{req: signS3(req, access, secret, s.region(), payloadHash, timeNow()), client: hc}, nil
}

func (s *S3) region() string {
	if s.Region == "" {
		return "us-east-1"
	}
	return s.Region
}

// do runs the request and fails unless the status is want (a second accepted status may be given).
func (r *request) do(want ...int) (*http.Response, error) {
	resp, err := r.client.Do(r.req)
	if err != nil {
		return nil, err
	}
	ok := false
	for _, w := range want {
		if resp.StatusCode == w {
			ok = true
		}
	}
	if !ok {
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("s3: %s: %s", resp.Status, firstLine(b))
	}
	return resp, nil
}

// signS3 finishes the request with the SigV4 Authorization header (service s3) and returns it.
func signS3(req *http.Request, access, secret, region, payloadHash string, now time.Time) *http.Request {
	amzDate := now.UTC().Format("20060102T150405Z")
	shortDate := amzDate[:8]
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Host = req.URL.Host

	// the canonical request: method, path with escaped segments, the sorted query, the signed
	// headers, and the payload hash
	canonURI := canonicalURI(req.URL.EscapedPath())
	var qb []string
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range q[k] {
			qb = append(qb, uriEscape(k, true)+"="+uriEscape(v, true))
		}
	}
	canonQuery := strings.Join(qb, "&")

	hdrs := []string{"host"}
	for k := range req.Header {
		l := strings.ToLower(k)
		if l == "authorization" || l == "user-agent" {
			continue
		}
		hdrs = append(hdrs, l)
	}
	sort.Strings(hdrs)
	var canonHdrs strings.Builder
	for _, h := range hdrs {
		v := req.Host
		if h != "host" {
			v = strings.TrimSpace(req.Header.Get(canonicalHeaderName(h)))
		}
		canonHdrs.WriteString(h + ":" + v + "\n")
	}
	signedList := strings.Join(hdrs, ";")
	canonReq := strings.Join([]string{
		req.Method, canonURI, canonQuery, canonHdrs.String(), signedList, payloadHash,
	}, "\n")

	// the string to sign, the derived key, the signature
	scope := shortDate + "/" + region + "/s3/aws4_request"
	csum := sha256.Sum256([]byte(canonReq))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hex.EncodeToString(csum[:])}, "\n")
	mac := func(key []byte, data string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(data))
		return m.Sum(nil)
	}
	kSigning := mac(mac(mac(mac([]byte("AWS4"+secret), shortDate), region), "s3"), "aws4_request")
	sig := hex.EncodeToString(mac(kSigning, toSign))
	req.Header.Set("Authorization",
		fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", access, scope, signedList, sig))
	return req
}

func canonicalHeaderName(lower string) string {
	parts := strings.Split(lower, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "-")
}

// canonicalURI re-escapes each path segment the way S3 expects: unescaped slashes, everything
// else escaped.
func canonicalURI(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = uriEscape(s, false)
	}
	return strings.Join(segs, "/")
}

// uriEscape is the RFC 3986 escaping S3 signs with: A-Z a-z 0-9 - _ . ~ stay (and / in a path).
func uriEscape(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// S3Entry is one object a listing returned.
type S3Entry struct {
	Key          string
	Size         int64
	LastModified time.Time
}

type listBucketResult struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	IsTrunc  bool     `xml:"IsTruncated"`
	Next     string   `xml:"NextContinuationToken"`
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

// List returns the objects under the destination's prefix, its pages followed.
func (s *S3) List(ctx context.Context) ([]S3Entry, error) {
	var out []S3Entry
	token := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", s.key(""))
		if token != "" {
			q.Set("continuation-token", token)
		}
		sum := sha256.Sum256(nil)
		r, err := s.build(ctx, http.MethodGet, "", q, nil, 0, hex.EncodeToString(sum[:]), nil)
		if err != nil {
			return nil, err
		}
		resp, err := r.do(http.StatusOK)
		if err != nil {
			return nil, err
		}
		next := resp.Header.Get("x-amz-next-continuation-token")
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		var res listBucketResult
		if err := xml.Unmarshal(b, &res); err != nil {
			return nil, fmt.Errorf("list %s: %w", s.Endpoint, err)
		}
		for _, c := range res.Contents {
			when, _ := time.Parse(time.RFC3339, c.LastModified)
			out = append(out, S3Entry{Key: strings.TrimPrefix(c.Key, s.Prefix), Size: c.Size, LastModified: when})
		}
		if !res.IsTrunc {
			return out, nil
		}
		token = next
		if token == "" {
			token = res.Next
		}
		if token == "" {
			return out, nil
		}
	}
}

// MaxS3PutBytes is the largest single PUT AWS S3 and R2 accept (5 GiB). Larger archives need
// a local destination, or a later multipart upload.
const MaxS3PutBytes = 5 << 30

// Put streams src (size bytes) under name.
func (s *S3) Put(ctx context.Context, name string, src io.Reader, size int64) (string, error) {
	if size > MaxS3PutBytes {
		return "", fmt.Errorf("an S3 PUT is at most 5 GiB (%d bytes given); use a local destination or split the backup", size)
	}
	r, err := s.build(ctx, http.MethodPut, s.key(name), nil, src, size, "UNSIGNED-PAYLOAD",
		map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return "", err
	}
	resp, err := r.do(http.StatusOK)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	return "", nil // the archive's hash is the read-back's to check, not the transport's
}

// HashOf streams the object under name through a hash and reports its size and SHA-256: the
// read-back check, without holding the object in memory.
func (s *S3) HashOf(ctx context.Context, name string) (int64, string, error) {
	sum := sha256.Sum256(nil)
	r, err := s.build(ctx, http.MethodGet, s.key(name), nil, nil, 0, hex.EncodeToString(sum[:]), nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := r.do(http.StatusOK, http.StatusPartialContent)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, resp.Body)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// Delete removes the object under name; a missing object is fine.
func (s *S3) Delete(ctx context.Context, name string) error {
	sum := sha256.Sum256(nil)
	r, err := s.build(ctx, http.MethodDelete, s.key(name), nil, nil, 0, hex.EncodeToString(sum[:]), nil)
	if err != nil {
		return err
	}
	resp, err := r.do(http.StatusNoContent, http.StatusNotFound)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i > 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
