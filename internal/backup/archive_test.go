package backup

import (
	"archive/tar"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/crypto/ssh"
)

// A fixture value that must never appear in any output, log or error the package produces; the
// gate greps a test run for it.
const fixtureSecret = " fixture-root-key-ZHQ9vzwKm3-not-real "

func testKeys(t *testing.T) (age.Recipient, age.Identity, age.Identity) {
	t.Helper()
	a, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	b, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return a.Recipient(), a, b
}

func testSigner(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func writeSource(t *testing.T, dir, name, content string) Source {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return Source{Name: name, Path: p}
}

func TestArchiveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec, mine, other := testKeys(t)
	signer := testSigner(t)
	src := writeSource(t, dir, "db/beacon.db", "a database fixture")
	src2 := writeSource(t, dir, "secrets/root-key", fixtureSecret)
	created := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	out := filepath.Join(dir, "beacon-srv-20260101-000000-full"+Suffix)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	man, err := Create(f, []age.Recipient{rec}, signer, []Source{src, src2},
		Manifest{Beacon: "test", Host: "srv", Created: created, Kind: "full", Schema: 7})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(man.Members) != 2 || man.Recordings != 0 {
		t.Fatalf("members %+v", man.Members)
	}
	// the fixture value is nowhere in the encrypted file
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if containsPlain(b, fixtureSecret) || containsPlain(b, "a database fixture") {
		t.Fatal("the archive holds plaintext")
	}

	// with the right identity: the members and the manifest come back, and match
	ar, err := Extract(out, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if ar.Manifest.Schema != 7 || !ar.Manifest.Created.Equal(created) || len(ar.Manifest.Members) != 2 {
		t.Fatalf("manifest %+v", ar.Manifest)
	}
	got, err := os.ReadFile(filepath.Join(ar.Dir, "secrets/root-key"))
	if err != nil || string(got) != fixtureSecret {
		t.Fatalf("the member came back wrong: %q %v", got, err)
	}

	// with no identity: nothing; with the wrong identity: nothing either — the encryption is
	// real, not a costume
	if _, err := Extract(out, nil, signPublicOf(signer), filepath.Join(dir, "out2")); err == nil {
		t.Fatal("an archive without an identity opened")
	}
	if _, err := Extract(out, []age.Identity{other}, signPublicOf(signer), filepath.Join(dir, "out3")); err == nil {
		t.Fatal("an archive opened with the wrong identity")
	}

	// a flipped byte inside the archive: the tamper is seen, not decrypted into garbage
	flip := make([]byte, len(b))
	copy(flip, b)
	flip[len(flip)/2] ^= 0xff
	bad := filepath.Join(dir, "bad"+Suffix)
	if err := os.WriteFile(bad, flip, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(bad, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "out4")); err == nil {
		t.Fatal("a changed archive opened")
	}
}

func containsPlain(b []byte, s string) bool {
	j := 0
	for i := 0; i < len(b) && j < len(s); i++ {
		if b[i] == s[j] {
			j++
		}
	}
	return j == len(s)
}

// ed25519Key makes an unencrypted openssh ed25519 pair for the recipient/identity tests.
func ed25519Key(t *testing.T) (pub string, priv []byte, err error) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	block, err := ssh.MarshalPrivateKey(key, "backup test key")
	if err != nil {
		return "", nil, err
	}
	priv = pem.EncodeToMemory(block)
	sshPub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		return "", nil, err
	}
	return string(ssh.MarshalAuthorizedKey(sshPub)), priv, nil
}

// packForTest writes an archive the way Create does, but with the manifest (and optional
// signature) handed in verbatim — how a tamperer would replay a manifest over changed members.
func packForTest(t *testing.T, out string, rec age.Recipient, sources []Source, manifest, sig []byte) {
	t.Helper()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	aw, err := age.Encrypt(f, rec)
	if err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(aw)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, src := range sources {
		b, err := os.ReadFile(src.Path)
		if err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: src.Name, Mode: 0o600, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if sig != nil {
		if err := tw.WriteHeader(&tar.Header{Name: manifestSigName, Mode: 0o600, Size: int64(len(sig))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(sig); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := aw.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustSign(t *testing.T, signer ed25519.PrivateKey, manifest []byte) []byte {
	t.Helper()
	sig, err := signManifestBytes(signer, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestArchiveTamperedMemberIsSeen(t *testing.T) {
	dir := t.TempDir()
	rec, mine, _ := testKeys(t)
	signer := testSigner(t)
	src := writeSource(t, dir, "db/beacon.db", "fixture")
	out := filepath.Join(dir, "a"+Suffix)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Create(f, []age.Recipient{rec}, signer, []Source{src}, Manifest{Kind: "full", Created: time.Now().UTC()})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	ar, err := Extract(out, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "good"))
	if err != nil {
		t.Fatal(err)
	}
	mb := jsonMarshalIndent(ar.Manifest)
	sig := mustSign(t, signer, mb)
	p := filepath.Join(dir, "changed")
	if err := os.WriteFile(p, []byte("changed after the fact"), 0o600); err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(dir, "b"+Suffix)
	packForTest(t, tampered, rec, []Source{{Name: "db/beacon.db", Path: p}}, mb, sig)
	if _, err := Extract(tampered, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "check")); err == nil || !errors.Is(err, ErrTampered) {
		t.Fatalf("a changed member passed: %v", err)
	}
	// a member the manifest does not know is refused too
	extra := writeSource(t, dir, "secrets/extra", "smuggled")
	packForTest(t, tampered, rec, []Source{src, extra}, mb, sig)
	if _, err := Extract(tampered, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "check2")); err == nil || !errors.Is(err, ErrTampered) {
		t.Fatalf("a smuggled member passed: %v", err)
	}
	// and a member that vanished
	packForTest(t, tampered, rec, nil, mb, sig)
	if _, err := Extract(tampered, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "check3")); err == nil || !errors.Is(err, ErrTampered) {
		t.Fatalf("a missing member passed: %v", err)
	}
}

func TestRecipientsAndIdentities(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	// one ssh-ed25519 pair, used as both a recipient and an identity (the plan allows both kinds)
	pub, priv, err := ed25519Key(t)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := ParseRecipients([]string{id.Recipient().String(), pub, "# a comment", ""})
	if err != nil || len(rs) != 2 {
		t.Fatalf("recipients: %d %v", len(rs), err)
	}
	ids, err := ParseIdentities([]byte(id.String() + "\n"))
	if err != nil || len(ids) != 1 {
		t.Fatalf("an age identity: %v", err)
	}
	sshIDs, err := ParseIdentities(priv)
	if err != nil || len(sshIDs) != 1 {
		t.Fatalf("an ssh identity: %v", err)
	}
	// both identities open an archive encrypted to both
	recs, _ := ParseRecipients([]string{id.Recipient().String(), pub})
	dir := t.TempDir()
	src := writeSource(t, dir, "db/beacon.db", "fixture")
	out := filepath.Join(dir, "a"+Suffix)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	signer := testSigner(t)
	if _, err := Create(f, recs, signer, []Source{src}, Manifest{Kind: "full", Created: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for name, list := range map[string][]age.Identity{"age": ids, "ssh": sshIDs} {
		if _, err := Extract(out, list, signPublicOf(signer), filepath.Join(dir, "out-"+name)); err != nil {
			t.Fatalf("the %s identity could not open the archive: %v", name, err)
		}
	}
	if _, err := ParseRecipients([]string{"not-a-key"}); err == nil {
		t.Fatal("a bad recipient parsed")
	}
}

func TestExtractReadsAManifestLargerThanOneMiB(t *testing.T) {
	dir := t.TempDir()
	rec, mine, _ := testKeys(t)
	signer := testSigner(t)
	src := writeSource(t, dir, "db/beacon.db", "fixture")
	man := Manifest{
		Format: FormatVersion, Kind: KindFull, Created: time.Now().UTC().Truncate(time.Second),
		Note:    strings.Repeat("n", 2<<20), // 2 MiB: the old 1 MiB cap refused this
		Members: []Member{{Name: "db/beacon.db", Size: 7, SHA: "placeholder"}},
	}
	st, err := os.Stat(src.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256File(t, src.Path)
	man.Members[0].Size, man.Members[0].SHA = st.Size(), sum
	mb, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(mb) <= 1<<20 {
		t.Fatalf("the fixture is %d bytes, need more than 1 MiB", len(mb))
	}
	out := filepath.Join(dir, "big"+Suffix)
	packForTest(t, out, rec, []Source{src}, mb, mustSign(t, signer, mb))
	ar, err := Extract(out, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("a 2 MiB manifest was refused: %v", err)
	}
	if ar.Manifest.Note != man.Note {
		t.Fatal("the large note did not come back")
	}
}

func TestCreateRefusesAnOversizedManifest(t *testing.T) {
	dir := t.TempDir()
	rec, _, _ := testKeys(t)
	src := writeSource(t, dir, "db/beacon.db", "fixture")
	out := filepath.Join(dir, "a"+Suffix)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	_, err = Create(f, []age.Recipient{rec}, testSigner(t), []Source{src},
		Manifest{Kind: KindFull, Created: time.Now().UTC(), Note: strings.Repeat("x", MaxManifestBytes+1)})
	if err == nil || !errors.Is(err, ErrManifestSize) {
		t.Fatalf("an oversized manifest was written: %v", err)
	}
}

func TestExtractRefusesUnsignedAndWrongKey(t *testing.T) {
	dir := t.TempDir()
	rec, mine, _ := testKeys(t)
	signer := testSigner(t)
	other := testSigner(t)
	src := writeSource(t, dir, "db/beacon.db", "fixture")
	man := Manifest{Format: FormatVersion, Kind: KindFull, Created: time.Now().UTC().Truncate(time.Second),
		Members: []Member{{Name: "db/beacon.db", Size: 7, SHA: sha256File(t, src.Path)}}}
	st, _ := os.Stat(src.Path)
	man.Members[0].Size = st.Size()
	mb := jsonMarshalIndent(man)
	unsigned := filepath.Join(dir, "u"+Suffix)
	packForTest(t, unsigned, rec, []Source{src}, mb, nil)
	if _, err := Extract(unsigned, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "u")); err == nil || !errors.Is(err, ErrUnsigned) {
		t.Fatalf("an unsigned archive opened: %v", err)
	}
	wrong := filepath.Join(dir, "w"+Suffix)
	packForTest(t, wrong, rec, []Source{src}, mb, mustSign(t, other, mb))
	if _, err := Extract(wrong, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "w")); err == nil || !errors.Is(err, ErrBadSignature) {
		t.Fatalf("a wrongly-signed archive opened: %v", err)
	}
}

func TestExtractRefusesDisallowedMemberPrefixes(t *testing.T) {
	dir := t.TempDir()
	rec, mine, _ := testKeys(t)
	signer := testSigner(t)
	db := writeSource(t, dir, "db/beacon.db", "fixture")
	for _, name := range []string{"beacon.db", "backup/age-public.key", "../outside", "recordings/../../etc/passwd", "etc/passwd"} {
		evil := writeSource(t, dir, "evil", "no")
		evil.Name = name
		man := Manifest{Format: FormatVersion, Kind: KindFull, Created: time.Now().UTC().Truncate(time.Second),
			Members: []Member{{Name: name, Size: 2, SHA: sha256File(t, evil.Path)}}}
		mb := jsonMarshalIndent(man)
		out := filepath.Join(dir, "e"+Suffix)
		packForTest(t, out, rec, []Source{evil}, mb, mustSign(t, signer, mb))
		_, err := Extract(out, []age.Identity{mine}, signPublicOf(signer), filepath.Join(dir, "x-"+strings.Map(func(r rune) rune {
			if r == '/' || r == '.' || r == ':' || r == '\\' {
				return '-'
			}
			return r
		}, name)))
		if err == nil || (!errors.Is(err, ErrMemberName) && !errors.Is(err, ErrFormat)) {
			t.Errorf("member %q was accepted: %v", name, err)
		}
	}
	if _, err := Create(mustCreate(t, filepath.Join(dir, "c"+Suffix)), []age.Recipient{rec}, signer,
		[]Source{{Name: "beacon.db", Path: db.Path}}, Manifest{Kind: KindFull, Created: time.Now().UTC()}); err == nil || !errors.Is(err, ErrMemberName) {
		t.Fatalf("Create packed a root member: %v", err)
	}
}

func TestParseNameRejectsAnotherPrefix(t *testing.T) {
	at := time.Date(2026, 11, 5, 7, 8, 9, 0, time.UTC)
	if _, _, _, ok := ParseName("other/" + ArchiveName("srv", at, KindFull)); ok {
		t.Fatal("a key under another prefix parsed as a backup")
	}
	name := ArchiveName("srv-edge-1", at, KindDB)
	host, when, kind, ok := ParseName(name)
	if !ok || host != "srv-edge-1" || !when.Equal(at) || kind != KindDB {
		t.Fatalf("parse db %q: %q %v %q %v", name, host, when, kind, ok)
	}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	h, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustCreate(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
