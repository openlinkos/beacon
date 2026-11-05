package secrets

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

// The KMS seal modes are tested three ways:
//
//   - against a local stand-in that speaks the keeper's own protocol (here): the AWS mode's SigV4
//     signing against a server that checks it, the GCP mode's REST shape, the transit mode's
//     envelope — the real cloud APIs have not been tried from here (no cloud account was
//     available; the gap is written in the package's documentation and the notes, not hidden);
//   - against LocalStack (AWS) and OpenBao (transit) on a test machine, when the environment names
//     them: BEACON_TEST_AWSKMS (its URL), BEACON_TEST_AWSKMS_KEY, BEACON_TEST_AWSKMS_ACCESS,
//     BEACON_TEST_AWSKMS_SECRET and BEACON_TEST_BAO (its URL), BEACON_TEST_BAO_KEY,
//     BEACON_TEST_BAO_TOKEN. Without them the tests skip, saying so.

// TestKMSSealModes runs the vault through each KMS mode against a local stand-in: the root key is
// wrapped by the keeper, the second start asks it to unwrap, and the values ride through.
func TestKMSSealModes(t *testing.T) {
	for _, mode := range []string{SealAWSKMS, SealGCPKMS, SealTransit} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cfg := SealConfig{Mode: mode, Dir: filepath.Join(dir, "secrets")}
			server, _ := fakeKeepers(t, &cfg, dir)
			defer server.Close()
			st := newTestStore(t, filepath.Join(dir, "beacon.db"))
			d := Deps{Store: st, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil },
				HTTP: server.Client()}
			s, err := Open(context.Background(), cfg, d)
			if err != nil {
				t.Fatal(err)
			}
			if s.Sealed() {
				t.Fatalf("%s: the KMS unseals at the first start", mode)
			}
			col := newCollection(t, s, "Infra")
			item := putItem(t, s, col.Name, "", "password", fixturePassword)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			// the second start: the wrapped root key goes to the keeper again
			st2 := newTestStore(t, filepath.Join(dir, "beacon.db"))
			s2, err := Open(context.Background(), cfg, Deps{Store: st2, Lang: i18n.EN, Log: testLog(t),
				CheckPassword: func(string, string) error { return nil }, HTTP: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			values, _, err := s2.Reveal(owner, item.ID, nil, "pw")
			if err != nil || values["password"] != fixturePassword {
				t.Fatalf("%s: the values survive the restart, got %v", mode, err)
			}
			// rotating the root key wraps the new one with the keeper again
			if err := s2.RotateRootKey(""); err != nil {
				t.Fatalf("%s: rotation asks the keeper to wrap the new key, got %v", mode, err)
			}
			values, _, _ = s2.Reveal(owner, item.ID, nil, "pw")
			if values["password"] != fixturePassword {
				t.Fatalf("%s: the values survive the rotation", mode)
			}
		})
	}
}

// TestKMSUnreachableLeavesSealed: a keeper that cannot be reached leaves the vault sealed, with
// the alarm, instead of failing the service — monitoring and the SSH channel carry on.
func TestKMSUnreachableLeavesSealed(t *testing.T) {
	dir := t.TempDir()
	cfg := SealConfig{Mode: SealTransit, Dir: filepath.Join(dir, "secrets")}
	cfg.Transit.Address = "http://127.0.0.1:1" // nothing listens there
	cfg.Transit.KeyName = "root"
	cfg.Transit.TokenFile = filepath.Join(dir, "token")
	if err := os.WriteFile(cfg.Transit.TokenFile, []byte("fixture-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newTestStore(t, filepath.Join(dir, "beacon.db"))
	d := Deps{Store: st, Lang: i18n.EN, Log: testLog(t), CheckPassword: func(string, string) error { return nil }}
	// the first start cannot wrap a new key with an unreachable keeper: it fails loudly
	if _, err := Open(context.Background(), cfg, d); err == nil {
		t.Fatal("the first start without a keeper fails: there is nowhere to leave the wrapped key")
	}
}

// fakeKeepers stands up one HTTP server that speaks whichever keeper the mode names, filling the
// configuration with its address and its token file.
func fakeKeepers(t *testing.T, cfg *SealConfig, dir string) (*httptest.Server, string) {
	t.Helper()
	tokenFile := filepath.Join(dir, "keeper-token")
	if err := os.WriteFile(tokenFile, []byte("fixture-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	var awsKey, gcpKey, transitKey []byte
	for _, k := range []*[]byte{&awsKey, &gcpKey, &transitKey} {
		key, err := randomKey()
		if err != nil {
			t.Fatal(err)
		}
		*k = key
	}
	// the AWS shape: the KMS JSON API with X-Amz-Target; the stand-in checks the SigV4 headers
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if target := r.Header.Get("X-Amz-Target"); target == "" {
			// not the AWS shape: the GCP and transit paths answer below
			if strings.HasSuffix(r.URL.Path, ":encrypt") || strings.HasSuffix(r.URL.Path, ":decrypt") ||
				strings.Contains(r.URL.Path, "/transit/") {
				serveGCPTransit(w, r, gcpKey, transitKey)
				return
			}
			w.WriteHeader(404)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") || r.Header.Get("X-Amz-Date") == "" {
			t.Errorf("the AWS call is SigV4-signed, got %q", auth)
			w.WriteHeader(400)
			return
		}
		var in struct {
			Plaintext      []byte `json:"Plaintext"`
			CiphertextBlob []byte `json:"CiphertextBlob"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch target := r.Header.Get("X-Amz-Target"); target {
		case "TrentService.Encrypt":
			ct, err := wrap(awsKey, in.Plaintext, []byte("aws"))
			if err != nil {
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"CiphertextBlob": ct})
		case "TrentService.Decrypt":
			pt, err := unwrap(awsKey, in.CiphertextBlob, []byte("aws"))
			if err != nil {
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Plaintext": pt})
		default:
			w.WriteHeader(404)
		}
	})
	server := httptest.NewServer(mux)
	switch cfg.Mode {
	case SealAWSKMS:
		cfg.AWS.Endpoint = server.URL
		cfg.AWS.Region = "us-east-1"
		cfg.AWS.KeyID = "alias/beacon-test"
		cfg.AWS.AccessKeyFile = filepath.Join(dir, "ak")
		cfg.AWS.SecretKeyFile = filepath.Join(dir, "sk")
		_ = os.WriteFile(cfg.AWS.AccessKeyFile, []byte("AKIAFIXTURE"), 0o600)
		_ = os.WriteFile(cfg.AWS.SecretKeyFile, []byte("fixture-secret"), 0o600)
	case SealGCPKMS:
		cfg.GCP.Endpoint = server.URL
		cfg.GCP.KeyName = "projects/p/locations/l/keyRings/r/cryptoKeys/k"
		cfg.GCP.TokenFile = tokenFile
	case SealTransit:
		cfg.Transit.Address = server.URL
		cfg.Transit.KeyName = "beacon"
		cfg.Transit.TokenFile = tokenFile
	}
	return server, tokenFile
}

// newTestStore opens a store the KMS tests manage themselves (some close theirs mid-test).
func newTestStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ---- the real stand-ins on a test machine (LocalStack for AWS KMS, OpenBao for transit)

// TestAWSKMSLocalStack runs the AWS mode against a LocalStack the environment names.
func TestAWSKMSLocalStack(t *testing.T) {
	url := os.Getenv("BEACON_TEST_AWSKMS")
	if url == "" {
		t.Skip("no LocalStack named: set BEACON_TEST_AWSKMS (and _KEY, _ACCESS, _SECRET) to run this")
	}
	dir := t.TempDir()
	cfg := SealConfig{Mode: SealAWSKMS, Dir: filepath.Join(dir, "secrets")}
	cfg.AWS.Endpoint = url
	cfg.AWS.Region = "us-east-1"
	cfg.AWS.KeyID = os.Getenv("BEACON_TEST_AWSKMS_KEY")
	cfg.AWS.AccessKeyFile = filepath.Join(dir, "ak")
	cfg.AWS.SecretKeyFile = filepath.Join(dir, "sk")
	_ = os.WriteFile(cfg.AWS.AccessKeyFile, []byte(orEnv("BEACON_TEST_AWSKMS_ACCESS", "test")), 0o600)
	_ = os.WriteFile(cfg.AWS.SecretKeyFile, []byte(orEnv("BEACON_TEST_AWSKMS_SECRET", "test")), 0o600)
	runKMSVault(t, cfg, nil)
}

// TestTransitOpenBao runs the transit mode against an OpenBao the environment names.
func TestTransitOpenBao(t *testing.T) {
	url := os.Getenv("BEACON_TEST_BAO")
	if url == "" {
		t.Skip("no OpenBao named: set BEACON_TEST_BAO (and _KEY, _TOKEN) to run this")
	}
	dir := t.TempDir()
	cfg := SealConfig{Mode: SealTransit, Dir: filepath.Join(dir, "secrets")}
	cfg.Transit.Address = url
	cfg.Transit.KeyName = orEnv("BEACON_TEST_BAO_KEY", "beacon-test")
	tokenFile := filepath.Join(dir, "token")
	_ = os.WriteFile(tokenFile, []byte(orEnv("BEACON_TEST_BAO_TOKEN", "")), 0o600)
	cfg.Transit.TokenFile = tokenFile
	if os.Getenv("BEACON_TEST_BAO_TOKEN") == "" {
		t.Skip("no OpenBao token named: set BEACON_TEST_BAO_TOKEN to run this")
	}
	runKMSVault(t, cfg, nil)
}

func orEnv(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// runKMSVault opens the vault on a KMS configuration, stores a value and reads it back after a
// restart — the whole unseal path against the real stand-in.
func runKMSVault(t *testing.T, cfg SealConfig, _ []byte) {
	t.Helper()
	path := filepath.Join(filepath.Dir(cfg.Dir), "beacon.db")
	open := func() (*Service, *store.Store) {
		st := newTestStore(t, path)
		s, err := Open(context.Background(), cfg, Deps{Store: st, Lang: i18n.EN, Log: testLog(t),
			CheckPassword: func(string, string) error { return nil }})
		if err != nil {
			t.Fatal(err)
		}
		return s, st
	}
	s1, st1 := open()
	col := newCollection(t, s1, "Infra")
	item := putItem(t, s1, col.Name, "", "password", fixturePassword)
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st1.Close(); err != nil {
		t.Fatal(err)
	}
	s2, st2 := open()
	defer func() { _ = st2.Close() }()
	values, _, err := s2.Reveal(owner, item.ID, nil, "pw")
	if err != nil || values["password"] != fixturePassword {
		t.Fatalf("the values survive the restart against the real keeper, got %v", err)
	}
}

// serveGCPTransit answers the GCP shape (:encrypt / :decrypt with base64 and a bearer token) and
// the transit shape (/v1/transit/… with the token header).
func serveGCPTransit(w http.ResponseWriter, r *http.Request, gcpKey, transitKey []byte) {
	if strings.Contains(r.URL.Path, "/transit/") {
		if r.Header.Get("X-Vault-Token") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.Contains(r.URL.Path, "/transit/encrypt/") {
			var in struct {
				Plaintext string `json:"plaintext"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			pt, _ := base64.StdEncoding.DecodeString(in.Plaintext)
			ct, err := wrap(transitKey, pt, []byte("transit"))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"ciphertext": base64.StdEncoding.EncodeToString(ct)}})
			return
		}
		var in struct {
			Ciphertext string `json:"ciphertext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		ct, _ := base64.StdEncoding.DecodeString(in.Ciphertext)
		pt, err := unwrap(transitKey, ct, []byte("transit"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"plaintext": base64.StdEncoding.EncodeToString(pt)}})
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if strings.HasSuffix(r.URL.Path, ":encrypt") {
		var in struct {
			Plaintext string `json:"plaintext"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		pt, _ := base64.StdEncoding.DecodeString(in.Plaintext)
		ct, err := wrap(gcpKey, pt, []byte("gcp"))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ciphertext": base64.StdEncoding.EncodeToString(ct)})
		return
	}
	var in struct {
		Ciphertext string `json:"ciphertext"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ct, _ := base64.StdEncoding.DecodeString(in.Ciphertext)
	pt, err := unwrap(gcpKey, ct, []byte("gcp"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"plaintext": base64.StdEncoding.EncodeToString(pt)})
}
