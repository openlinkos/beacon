package secrets

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The KMS unseal modes reach their key keeper over plain HTTPS with the keeper's own API — no
// vendor SDK, so the service keeps the one binary and the small dependency tree it has:
//
//   - aws-kms: the KMS JSON API (TrentService.Encrypt/Decrypt) with SigV4 signing; an endpoint of
//     its own points at a local one for the tests.
//   - gcp-kms: Cloud KMS's REST API with a bearer token from a file.
//   - transit: Vault's or OpenBao's transit engine with a token from a file.
//
// The real clouds have not been tried from here (no cloud account was available): the AWS and GCP
// paths are exercised against local stand-ins, and the gap is written in the notes, not hidden.

// kmsClient is what a seal mode's key keeper does: wrap and unwrap the root key.
type kmsClient interface {
	encrypt(ctx context.Context, plaintext []byte) ([]byte, error)
	decrypt(ctx context.Context, wrapped []byte) ([]byte, error)
}

// errKMS marks a keeper's refusal, without the material it saw.
var errKMS = errors.New("the key keeper answered with an error")

// kmsClientFor makes the client the configured mode needs.
func (s *Service) kmsClient() (kmsClient, error) {
	hc := s.d.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	switch s.cfg.Mode {
	case SealAWSKMS:
		return &awsKMS{cfg: s.cfg, hc: hc}, nil
	case SealGCPKMS:
		return &gcpKMS{cfg: s.cfg, hc: hc}, nil
	case SealTransit:
		return &transitKMS{cfg: s.cfg, hc: hc}, nil
	}
	return nil, fmt.Errorf("the seal mode %q has no key keeper", s.cfg.Mode)
}

// ---- AWS KMS

// awsKMS speaks the KMS JSON API with SigV4. Credentials come from files (the configuration names
// them) or, failing that, the standard environment, as the AWS tools read it.
type awsKMS struct {
	cfg SealConfig
	hc  *http.Client
}

// awsCreds is what SigV4 needs.
type awsCreds struct {
	accessKey, secretKey, session string
}

func (a *awsKMS) creds() (awsCreds, error) {
	var c awsCreds
	read := func(path string) (string, error) {
		b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	var err error
	if a.cfg.AWS.AccessKeyFile != "" {
		if c.accessKey, err = read(a.cfg.AWS.AccessKeyFile); err != nil {
			return c, err
		}
		if c.secretKey, err = read(a.cfg.AWS.SecretKeyFile); err != nil {
			return c, err
		}
		if a.cfg.AWS.SessionTokenFile != "" {
			if c.session, err = read(a.cfg.AWS.SessionTokenFile); err != nil {
				return c, err
			}
		}
		return c, nil
	}
	c.accessKey, c.secretKey, c.session = os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")
	if c.accessKey == "" || c.secretKey == "" {
		return c, fmt.Errorf("the AWS KMS mode needs its credentials: aws.access_key_file and aws.secret_key_file, or the AWS environment")
	}
	return c, nil
}

// call signs and sends one KMS API call; target is "TrentService.Encrypt" or "TrentService.Decrypt".
func (a *awsKMS) call(ctx context.Context, target string, in, out any) error {
	host := a.cfg.AWS.Endpoint
	if host == "" {
		if a.cfg.AWS.Region == "" {
			return fmt.Errorf("the AWS KMS mode needs its region (aws.region)")
		}
		host = "https://kms." + a.cfg.AWS.Region + ".amazonaws.com"
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	creds, err := a.creds()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	signAWS(req, body, creds, a.cfg.AWS.Region, "kms")
	resp, err := a.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: the KMS answered %d", errKMS, resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

func (a *awsKMS) encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	var out struct {
		CiphertextBlob []byte `json:"CiphertextBlob"`
	}
	if err := a.call(ctx, "TrentService.Encrypt", struct {
		KeyID     string `json:"KeyId"`
		Plaintext []byte `json:"Plaintext"`
	}{KeyID: a.cfg.AWS.KeyID, Plaintext: plaintext}, &out); err != nil {
		return nil, err
	}
	if len(out.CiphertextBlob) == 0 {
		return nil, fmt.Errorf("%w: the KMS gave no ciphertext", errKMS)
	}
	return out.CiphertextBlob, nil
}

func (a *awsKMS) decrypt(ctx context.Context, wrapped []byte) ([]byte, error) {
	var out struct {
		Plaintext []byte `json:"Plaintext"`
	}
	if err := a.call(ctx, "TrentService.Decrypt", struct {
		CiphertextBlob []byte `json:"CiphertextBlob"`
	}{CiphertextBlob: wrapped}, &out); err != nil {
		return nil, err
	}
	if len(out.Plaintext) == 0 {
		return nil, fmt.Errorf("%w: the KMS gave no plaintext", errKMS)
	}
	return out.Plaintext, nil
}

// signAWS is Signature Version 4 for a JSON API call with an X-Amz-Target header (what the SDK
// does, in the open): the canonical request, the string to sign, the signing key and the header.
func signAWS(req *http.Request, body []byte, creds awsCreds, region, service string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if creds.session != "" {
		req.Header.Set("X-Amz-Security-Token", creds.session)
	}
	req.Host = req.URL.Host

	var canonicalHeaders, signedHeaders strings.Builder
	headers := []struct{ name, value string }{
		{"content-type", req.Header.Get("Content-Type")},
		{"host", req.Host},
		{"x-amz-date", amzDate},
		{"x-amz-security-token", creds.session},
		{"x-amz-target", req.Header.Get("X-Amz-Target")},
	}
	names := []string{}
	for _, h := range headers {
		if h.value == "" {
			continue
		}
		names = append(names, h.name)
		canonicalHeaders.WriteString(h.name + ":" + h.value + "\n")
	}
	signedHeaders.WriteString(strings.Join(names, ";"))

	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaders.String(),
		signedHeaders.String(),
		payloadHash,
	}, "\n")
	crSum := sha256.Sum256([]byte(canonicalRequest))
	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(crSum[:]),
	}, "\n")

	hmacBytes := func(key []byte, data string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(data))
		return m.Sum(nil)
	}
	kDate := hmacBytes([]byte("AWS4"+creds.secretKey), dateStamp)
	kRegion := hmacBytes(kDate, region)
	kService := hmacBytes(kRegion, service)
	kSigning := hmacBytes(kService, "aws4_request")
	signature := hex.EncodeToString(hmacBytes(kSigning, stringToSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+creds.accessKey+"/"+scope+
		", SignedHeaders="+signedHeaders.String()+", Signature="+signature)
}

// ---- GCP KMS

// gcpKMS speaks Cloud KMS's REST API with a bearer token from a file.
type gcpKMS struct {
	cfg SealConfig
	hc  *http.Client
}

func (g *gcpKMS) endpoint() (string, error) {
	if g.cfg.GCP.KeyName == "" {
		return "", fmt.Errorf("the GCP KMS mode needs its key name (gcp.key_name)")
	}
	host := g.cfg.GCP.Endpoint
	if host == "" {
		host = "https://cloudkms.googleapis.com"
	}
	return host + "/v1/" + g.cfg.GCP.KeyName, nil
}

func (g *gcpKMS) token() (string, error) {
	if g.cfg.GCP.TokenFile == "" {
		return "", fmt.Errorf("the GCP KMS mode needs its bearer token in a file (gcp.token_file)")
	}
	b, err := os.ReadFile(g.cfg.GCP.TokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (g *gcpKMS) call(ctx context.Context, verb string, body []byte, out any) error {
	base, err := g.endpoint()
	if err != nil {
		return err
	}
	var url string
	switch verb {
	case http.MethodPost:
		url = base + ":encrypt"
	default:
		url = base + ":decrypt"
	}
	req, err := http.NewRequestWithContext(ctx, verb, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	token, err := g.token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: Cloud KMS answered %d", errKMS, resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

func (g *gcpKMS) encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	var out struct {
		Ciphertext string `json:"ciphertext"`
	}
	in, err := json.Marshal(struct {
		Plaintext string `json:"plaintext"`
	}{Plaintext: base64.StdEncoding.EncodeToString(plaintext)})
	if err != nil {
		return nil, err
	}
	if err := g.call(ctx, http.MethodPost, in, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Ciphertext)
}

func (g *gcpKMS) decrypt(ctx context.Context, wrapped []byte) ([]byte, error) {
	var out struct {
		Plaintext string `json:"plaintext"`
	}
	in, err := json.Marshal(struct {
		Ciphertext string `json:"ciphertext"`
	}{Ciphertext: base64.StdEncoding.EncodeToString(wrapped)})
	if err != nil {
		return nil, err
	}
	if err := g.call(ctx, http.MethodGet, in, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Plaintext)
}

// ---- Vault / OpenBao transit

// transitKMS speaks the transit engine's API: POST /v1/transit/encrypt/<key> and decrypt/<key>,
// with a token from a file.
type transitKMS struct {
	cfg SealConfig
	hc  *http.Client
}

func (t *transitKMS) call(ctx context.Context, op string, body []byte, out any) error {
	if t.cfg.Transit.Address == "" || t.cfg.Transit.KeyName == "" {
		return fmt.Errorf("the transit mode needs its address (transit.address) and key (transit.key_name)")
	}
	if t.cfg.Transit.TokenFile == "" {
		return fmt.Errorf("the transit mode needs its token in a file (transit.token_file)")
	}
	token, err := os.ReadFile(t.cfg.Transit.TokenFile)
	if err != nil {
		return err
	}
	url := strings.TrimRight(t.cfg.Transit.Address, "/") + "/v1/transit/" + op + "/" + t.cfg.Transit.KeyName
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: transit answered %d", errKMS, resp.StatusCode)
	}
	return json.Unmarshal(b, out)
}

// The transit engine's ciphertext ("vault:v1:…") is an opaque string: it is kept exactly as the
// engine wrote it, never decoded, for it names the key version as well as the bytes.
func (t *transitKMS) encrypt(ctx context.Context, plaintext []byte) ([]byte, error) {
	var out struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	in, err := json.Marshal(struct {
		Plaintext string `json:"plaintext"`
	}{Plaintext: base64.StdEncoding.EncodeToString(plaintext)})
	if err != nil {
		return nil, err
	}
	if err := t.call(ctx, "encrypt", in, &out); err != nil {
		return nil, err
	}
	if out.Data.Ciphertext == "" {
		return nil, fmt.Errorf("%w: transit gave no ciphertext", errKMS)
	}
	return []byte(out.Data.Ciphertext), nil
}

func (t *transitKMS) decrypt(ctx context.Context, wrapped []byte) ([]byte, error) {
	var out struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	in, err := json.Marshal(struct {
		Ciphertext string `json:"ciphertext"`
	}{Ciphertext: string(wrapped)})
	if err != nil {
		return nil, err
	}
	if err := t.call(ctx, "decrypt", in, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Data.Plaintext)
}
