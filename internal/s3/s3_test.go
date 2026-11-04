package s3

// The signing is pinned to the worked example of the Signature Version 4 specification (the
// iam ListUsers example AWS documents), so the algorithm is checked against something outside this
// package; the client itself is exercised against a local httptest server standing in for the
// bucket, which checks what arrives: the path-style address, the Authorization header, the payload
// hash header.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSignatureVector: the specification's classic worked inputs (service iam, the empty payload,
// the fixed date and keys), signed the way S3 needs them (x-amz-content-sha256 among the signed
// headers). The expected value was cross-checked against AWS's own sample implementation of the
// derivation (aws-samples/sigv4-signing-examples, no-sdk/golang, MIT-0) run over the same
// canonical request. A change here means the signing no longer matches what every S3-compatible
// store checks.
func TestSignatureVector(t *testing.T) {
	c := &Client{region: "us-east-1", id: "AKIDEXAMPLE", key: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	req, err := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	c.signService(req, when, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "iam")
	const want = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, " +
		"Signature=dd479fa8a80364edf2119ec24bebde66712ee9c9cb2b0d92eb3ab9ccdc0c3947"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("the signature does not match the cross-checked derivation:\n got %s\nwant %s", got, want)
	}
}

// testServer stands in for the bucket: it records what arrived and answers what the client expects.
func testServer(t *testing.T) (*Client, *[]*http.Request) {
	t.Helper()
	var got []*http.Request
	objects := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Clone(r.Context()))
		key := strings.TrimPrefix(r.URL.Path, "/beacon-recordings/")
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			if len(b) == 0 {
				w.WriteHeader(400)
				return
			}
			objects[key] = string(b)
			w.WriteHeader(200)
		case http.MethodGet:
			if v, ok := objects[key]; ok {
				_, _ = w.Write([]byte(v))
				return
			}
			http.NotFound(w, r)
		case http.MethodHead:
			if _, ok := objects[key]; !ok {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(objects[key])))
			w.WriteHeader(200)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(204)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{Endpoint: srv.URL, Region: "us-east-1", Bucket: "beacon-recordings",
		AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"})
	if err != nil {
		t.Fatal(err)
	}
	return c, &got
}

func TestClientRoundTrip(t *testing.T) {
	c, reqs := testServer(t)
	ctx := context.Background()
	if err := c.Put(ctx, "2026/11/04/id-1.cast", []byte("the recording")); err != nil {
		t.Fatal(err)
	}
	b, err := c.GetAll(ctx, "2026/11/04/id-1.cast")
	if err != nil || string(b) != "the recording" {
		t.Fatalf("get: %q %v", b, err)
	}
	if n, err := c.Stat(ctx, "2026/11/04/id-1.cast"); err != nil || n != 13 {
		t.Fatalf("stat: %d %v", n, err)
	}
	if _, err := c.Get(ctx, "2026/11/04/missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("a missing object should say so: %v", err)
	}
	if err := c.Remove(ctx, "2026/11/04/id-1.cast"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, "2026/11/04/missing"); err != nil {
		t.Fatalf("removing what is not there is no error: %v", err)
	}
	for i, r := range *reqs {
		if !strings.HasPrefix(r.URL.Path, "/beacon-recordings/") {
			t.Errorf("request %d not path-style: %s", i, r.URL.Path)
		}
		if a := r.Header.Get("Authorization"); !strings.HasPrefix(a, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") {
			t.Errorf("request %d carries no signature: %q", i, a)
		}
		if h := r.Header.Get("x-amz-content-sha256"); len(h) != 64 {
			t.Errorf("request %d carries no payload hash: %q", i, h)
		}
	}
}

func TestClientBadKey(t *testing.T) {
	c, _ := testServer(t)
	if err := c.Put(context.Background(), "../escape", nil); err == nil {
		t.Error("a key that leaves the bucket should be refused")
	}
}

// TestStore: the recordings' Files interface over the bucket — Create buffers and Close sends,
// transcripts write and read whole, Remove cleans up. Nothing arrives before Close: a session that
// never closes leaves nothing.
func TestStore(t *testing.T) {
	c, reqs := testServer(t)
	st := &Store{c: c}
	w, err := st.Create("2026/11/04/id-1.cast")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("the rec")); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile("2026/11/04/id-1.json", []byte("transcript")); err != nil {
		t.Fatal(err)
	}
	if n := len(*reqs); n != 1 { // the recording has not closed; only the transcript went
		t.Fatalf("%d requests before the recording closes; the transcript alone: %d", n, 1)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := st.ReadFile("2026/11/04/id-1.json")
	if err != nil || string(b) != "transcript" {
		t.Fatalf("transcript: %q %v", b, err)
	}
	if r, err := st.Open("2026/11/04/id-1.cast"); err != nil {
		t.Fatalf("open: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, r)
		_ = r.Close()
	}
	if err := st.Remove("2026/11/04/id-1.cast"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil { // closing twice sends nothing more
		t.Fatal(err)
	}
}
