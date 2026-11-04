// Package s3 is a small client for the S3-compatible storage Beacon writes to (MinIO, R2, S3):
// put, get, head and delete, signed the way every one of them understands, AWS Signature Version
// 4. It is deliberately no SDK: Beacon needs four verbs over one bucket — the session recordings
// (internal/sshgate) and, from the backup package on, the backups' destination — and a hand-signed
// request keeps the dependency tree as they are. Addresses are always path-style
// (endpoint/bucket/key), which all three answer.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Options is the bucket and its credentials. The credentials are read from the files the
// configuration names (ssh.recordings_s3.access_key_id_file and secret_access_key_file); they
// never appear in errors or logs.
type Options struct {
	Endpoint        string // https://s3.example.com, or http://…:9000 for MinIO on a private network
	Region          string // us-east-1
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	Client          *http.Client // nil: 30s timeouts, no proxy following
}

// Client is one bucket.
type Client struct {
	url     *url.URL
	region  string
	bucket  string
	id, key string
	http    *http.Client
}

// New checks the options and keeps them.
func New(o Options) (*Client, error) {
	u, err := url.Parse(o.Endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("s3 endpoint %q is not an http(s) URL such as https://s3.example.com", o.Endpoint)
	}
	if o.Bucket == "" || o.AccessKeyID == "" || o.SecretAccessKey == "" {
		return nil, errors.New("s3: bucket and credentials are needed")
	}
	if o.Region == "" {
		o.Region = "us-east-1"
	}
	hc := o.Client
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{url: u, region: o.Region, bucket: o.Bucket, id: o.AccessKeyID, key: o.SecretAccessKey, http: hc}, nil
}

// Put stores body under key, over what was there.
func (c *Client) Put(ctx context.Context, key string, body []byte) error {
	req, err := c.request(ctx, http.MethodPut, key, nil, body)
	if err != nil {
		return err
	}
	return c.status(req, key, http.StatusOK)
}

// Get returns what is under key; the caller closes it.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := c.request(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		defer func() { _ = res.Body.Close() }()
		return nil, c.failure(res, key)
	}
	return res.Body, nil
}

// GetAll reads what is under key into memory (transcripts are small; recordings replay streamed).
func (c *Client) GetAll(ctx context.Context, key string) ([]byte, error) {
	r, err := c.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

// Stat returns key's size, or an error naming a missing object.
func (c *Client) Stat(ctx context.Context, key string) (int64, error) {
	req, err := c.request(ctx, http.MethodHead, key, nil, nil)
	if err != nil {
		return 0, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return 0, c.failure(res, key)
	}
	return res.ContentLength, nil
}

// Remove deletes key; deleting what is not there is not an error.
func (c *Client) Remove(ctx context.Context, key string) error {
	req, err := c.request(ctx, http.MethodDelete, key, nil, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusNoContent {
		return nil
	}
	if res.StatusCode != http.StatusOK {
		return c.failure(res, key)
	}
	return nil
}

// request builds the signed request for one object. body is the payload for PUT (nil elsewhere,
// which is the empty payload's hash).
func (c *Client) request(ctx context.Context, method, key string, query url.Values, body []byte) (*http.Request, error) {
	if key == "" || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return nil, fmt.Errorf("s3: the object key %q is not a plain relative path", key)
	}
	u := *c.url
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + c.bucket + "/" + escapeKey(key)
	if query != nil {
		u.RawQuery = query.Encode()
	}
	payload := sha256.Sum256(body)
	hash := hex.EncodeToString(payload[:])
	now := time.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-date", now.Format("20060102T150405Z"))
	req.Header.Set("x-amz-content-sha256", hash)
	c.sign(req, now, hash)
	return req, nil
}

// status sends a request whose answer carries no body to read, and checks the code.
func (c *Client) status(req *http.Request, key string, want int) error {
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	if res.StatusCode != want {
		return c.failure(res, key)
	}
	return nil
}

// failure reads what the bucket said and says it without the credentials.
func (c *Client) failure(res *http.Response, key string) error {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	code := strings.TrimSpace(string(b))
	if code == "" {
		code = res.Status
	}
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("s3: %s: not found", key)
	}
	return fmt.Errorf("s3: %s: %s", res.Status, code)
}

// ---- Signature Version 4

// sign puts the Authorization header on req: the canonical request, hashed and signed with a key
// derived from the secret, over host (and content-type when the request carries one),
// x-amz-content-sha256 and x-amz-date. This is the signing every S3-compatible store checks; the
// test pins it to the worked example of the specification.
func (c *Client) sign(req *http.Request, now time.Time, payloadHash string) {
	c.signService(req, now, payloadHash, "s3")
}

// signService signs for the named service; the test verifies it against the specification's worked
// example, which signs an iam request.
func (c *Client) signService(req *http.Request, now time.Time, payloadHash, service string) {
	const algo = "AWS4-HMAC-SHA256"
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	// the canonical request: method, path, query, the headers signed (lower-case, sorted), and
	// the payload's hash
	type header struct{ name, value string }
	hs := []header{{"host", req.URL.Host}}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		hs = append(hs, header{"content-type", ct})
	}
	hs = append(hs, header{"x-amz-content-sha256", payloadHash}, header{"x-amz-date", amzDate})
	slices.SortStableFunc(hs, func(a, b header) int { return strings.Compare(a.name, b.name) })
	var headers bytes.Buffer
	names := make([]string, len(hs))
	for i, h := range hs {
		names[i] = h.name
		headers.WriteString(h.name + ":" + h.value + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery(req.URL.RawQuery),
		headers.String(),
		signed,
		payloadHash,
	}, "\n")

	scope := date + "/" + c.region + "/" + service + "/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{algo, amzDate, scope, hex.EncodeToString(sum[:])}, "\n")

	mac := hmac.New(sha256.New, []byte("AWS4"+c.key))
	mac.Write([]byte(date))
	kDate := mac.Sum(nil)
	mac = hmac.New(sha256.New, kDate)
	mac.Write([]byte(c.region))
	kRegion := mac.Sum(nil)
	mac = hmac.New(sha256.New, kRegion)
	mac.Write([]byte(service))
	kService := mac.Sum(nil)
	mac = hmac.New(sha256.New, kService)
	mac.Write([]byte("aws4_request"))
	kSigning := mac.Sum(nil)
	mac = hmac.New(sha256.New, kSigning)
	mac.Write([]byte(toSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("Authorization", algo+" Credential="+c.id+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
}

// canonicalQuery sorts the query's parameters the way the canonical request spells them.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	slices.Sort(parts)
	return strings.Join(parts, "&")
}

// escapeKey percent-encodes each segment of a slash-separated key, as the canonical URI needs.
func escapeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
