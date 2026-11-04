package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The S3 client against a stub store: what it sends must be a well-formed SigV4 request the
// store accepts, and the round trip — put, hash-of, list, delete — must behave. The real stores
// (MinIO on the acceptance machine, R2, S3) are exercised by the acceptance run; this keeps the
// wire format honest on every test run.

type stubStore struct {
	t        *testing.T
	objects  map[string][]byte
	prefixes map[string]bool // the prefixes listing was asked for
}

func (s *stubStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	// the shape of a SigV4 header, with our service and the required headers signed
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=test-access/") ||
		!strings.Contains(auth, "/s3/aws4_request") {
		s.t.Errorf("Authorization is not the SigV4 we promise: %s", auth)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	signed := ""
	if _, err := fmt.Sscanf(auth, "AWS4-HMAC-SHA256 Credential=%*s SignedHeaders=%s", &signed); err == nil {
		for _, need := range []string{"host", "x-amz-content-sha256", "x-amz-date"} {
			if !strings.Contains(signed, need) {
				s.t.Errorf("a required header is not signed (%s): %s", need, auth)
			}
		}
	}
	if r.Header.Get("x-amz-date") == "" {
		s.t.Error("no x-amz-date")
	}
	key := strings.TrimPrefix(r.URL.Path, "/stub-bucket/")
	switch {
	case r.Method == http.MethodPut:
		if r.Header.Get("x-amz-content-sha256") != "UNSIGNED-PAYLOAD" {
			s.t.Error("a streaming upload did not say UNSIGNED-PAYLOAD")
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			s.t.Error(err)
		}
		s.objects[key] = b
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && key == "" && r.URL.Query().Get("list-type") == "2":
		s.prefixes[r.URL.Query().Get("prefix")] = true
		type obj struct {
			Key          string `xml:"Key"`
			Size         int    `xml:"Size"`
			LastModified string `xml:"LastModified"`
		}
		out := struct {
			XMLName xml.Name `xml:"ListBucketResult"`
			Content []obj    `xml:"Contents"`
		}{}
		for k, v := range s.objects {
			out.Content = append(out.Content, obj{Key: k, Size: len(v), LastModified: "2026-11-05T12:00:00Z"})
		}
		b, _ := xml.Marshal(out)
		_, _ = w.Write(append([]byte(xml.Header), b...))
	case r.Method == http.MethodGet:
		b, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case r.Method == http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func stubS3(t *testing.T) (*S3, *stubStore) {
	t.Helper()
	dir := t.TempDir()
	access, secret := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(access, []byte("test-access\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := &stubStore{t: t, objects: map[string][]byte{}, prefixes: map[string]bool{}}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	return &S3{Endpoint: srv.URL, Bucket: "stub-bucket", Prefix: "beacon/",
		AccessKeyFile: access, SecretKeyFile: secret, hc: srv.Client()}, stub
}

func TestS3PutListHashDelete(t *testing.T) {
	s3, stub := stubS3(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	name := ArchiveName("srv", timeNow().UTC(), "full")
	body := []byte("an archive fixture, bytes of it")
	if _, err := s3.Put(ctx, name, strings.NewReader(string(body)), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	if got := stub.objects["beacon/"+name]; string(got) != string(body) {
		t.Fatalf("the store holds %q", got)
	}
	n, h, err := s3.HashOf(ctx, name)
	if err != nil || n != int64(len(body)) {
		t.Fatalf("hash-of: %d %v", n, err)
	}
	sum := sha256.Sum256(body)
	if h != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash-of: %s", h)
	}
	entries, err := s3.List(ctx)
	if err != nil || len(entries) != 1 || entries[0].Key != name || entries[0].Size != int64(len(body)) {
		t.Fatalf("list: %+v %v", entries, err)
	}
	if !stub.prefixes["beacon/"] {
		t.Error("the listing did not ask for the destination's prefix")
	}
	if err := s3.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	if len(stub.objects) != 0 {
		t.Error("the delete left the object behind")
	}
	if err := s3.Delete(ctx, name); err != nil {
		t.Fatalf("deleting what is gone is fine: %v", err)
	}
}

func TestS3SignatureIsSigV4(t *testing.T) {
	// a signature computed over a known request, checked against the pattern: the credential
	// scope, the signed headers and a 64-hex signature
	s3, _ := stubS3(t)
	req, err := http.NewRequest(http.MethodPut, s3.Endpoint+"/stub-bucket/x", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(nil)
	req = signS3(req, "AKIA-TEST", "SECRET-TEST", "us-east-1", hex.EncodeToString(sum[:]),
		timeNow())
	auth := req.Header.Get("Authorization")
	if !regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=AKIA-TEST/\d{8}/us-east-1/s3/aws4_request, SignedHeaders=[a-z0-9;-]+, Signature=[0-9a-f]{64}$`).MatchString(auth) {
		t.Fatalf("the signature is not well formed: %s", auth)
	}
	// a different secret signs differently (the signature is really computed, not decoration)
	req2, _ := http.NewRequest(http.MethodPut, s3.Endpoint+"/stub-bucket/x", strings.NewReader("body"))
	req2 = signS3(req2, "AKIA-TEST", "OTHER-SECRET", "us-east-1", hex.EncodeToString(sum[:]), timeNow())
	if req2.Header.Get("Authorization") == auth {
		t.Fatal("two secrets produced one signature")
	}
}
