package backup

// The backup archive: beacon-<host>-<time>-<full|incr|db>.tar.zst.age — a tar of the members
// (the database snapshot, recordings, configuration, secrets), zstd-compressed, age-encrypted
// to the recipients. The manifest.json member, written last, carries what the plan's §11 asks
// for: the format version, the application data version, the database's schema version, every
// table's row count, the audit chain's head, and each member's SHA-256 — so a restore can check
// everything it unpacks against what was packed. A sibling manifest.sig is an ed25519 signature
// of those exact bytes, so a restore can tell this Beacon wrote the archive.

import (
	"archive/tar"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

// FormatVersion is the archive format's own version; a reader that meets a newer one says so.
const FormatVersion = 1

// DataVersion is Beacon's application data layout as backups record it. SQLite's schema_version
// is only a count of DDL changes and is not this. A restore of a newer data version than this
// binary knows is refused.
const DataVersion = 1

// MaxManifestBytes is how large a manifest.json a backup will write or read. A member adds
// about 180 bytes; 64 MiB is far above any realistic archive (hundreds of thousands of
// recordings) and still a bound, so a corrupt or hostile archive cannot fill memory.
const MaxManifestBytes = 64 << 20

// Suffix is every archive's file name suffix.
const Suffix = ".tar.zst.age"

// The kinds an archive's name carries: a complete recordings set, the recordings since the last
// files run, or the database (and secrets) alone.
const (
	KindFull = "full"
	KindIncr = "incr"
	KindDB   = "db"
)

// Manifest describes one archive. Schema, Tables and AuditHead describe the database member at
// the moment it was snapshotted; Recordings counts the recording files inside; Note carries what
// does not fit elsewhere (a pre-migration backup says so here).
type Manifest struct {
	Format      int              `json:"format"`
	Beacon      string           `json:"beacon"`
	Host        string           `json:"host"`
	Created     time.Time        `json:"created"`
	Kind        string           `json:"kind"` // full, incr or db
	DataVersion int              `json:"data_version,omitempty"`
	Schema      int              `json:"schema,omitempty"`
	Tables      map[string]int64 `json:"tables,omitempty"`
	AuditHead   string           `json:"audit_head,omitempty"`
	AuditSeq    uint64           `json:"audit_seq,omitempty"`
	Recordings  int              `json:"recordings"`
	Note        string           `json:"note,omitempty"`
	Members     []Member         `json:"members"`
}

// Member is one file inside the archive, with the hash it had when packed.
type Member struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	SHA  string `json:"sha256"`
}

// ErrFormat is an archive from a later format version, or not an archive at all.
var ErrFormat = errors.New("not a backup archive of a known format")

// ErrTampered means what was unpacked does not match what was packed.
var ErrTampered = errors.New("the archive does not match its manifest")

// ErrUnsigned means the archive has no manifest signature, or none that this key can check.
var ErrUnsigned = errors.New("the archive's manifest is not signed")

// ErrBadSignature means the manifest was signed, but not with the key a restore was given.
var ErrBadSignature = errors.New("the archive's manifest signature does not match")

// ErrMemberName means a member path is not one of the prefixes a backup may hold.
var ErrMemberName = errors.New("the archive holds a member it should not")

// ErrManifestSize means the manifest is larger than MaxManifestBytes.
var ErrManifestSize = errors.New("the backup's manifest is too large")

// Source is one file to pack: Name is its path inside the archive, Path the file on disk.
type Source struct {
	Name string
	Path string
}

// Create writes the archive to out: every source tarred and hashed, then the manifest carrying
// them all, then a signature of those exact bytes. base is the manifest as far as the caller
// filled it (Members and Recordings are computed here). signer is the server's manifest key.
func Create(out io.Writer, recipients []age.Recipient, signer ed25519.PrivateKey, sources []Source, base Manifest) (Manifest, error) {
	if len(recipients) == 0 {
		return Manifest{}, errors.New("no recipients: a backup needs at least one public key")
	}
	if len(signer) != ed25519.PrivateKeySize {
		return Manifest{}, errors.New("no manifest signing key: a backup needs one")
	}
	if base.Format == 0 {
		base.Format = FormatVersion
	}
	if base.DataVersion == 0 {
		base.DataVersion = DataVersion
	}
	base.Created = base.Created.UTC().Truncate(time.Second)
	base.Members = nil // computed here, whatever the caller carried in
	for _, src := range sources {
		if err := allowedMember(src.Name); err != nil {
			return Manifest{}, err
		}
	}
	aw, err := age.Encrypt(out, recipients...)
	if err != nil {
		return Manifest{}, err
	}
	zw, err := zstd.NewWriter(aw)
	if err != nil {
		_ = aw.Close()
		return Manifest{}, err
	}
	tw := tar.NewWriter(zw)
	for _, src := range sources {
		m, err := packFile(tw, src)
		if err != nil {
			return Manifest{}, fmt.Errorf("pack %s: %w", src.Path, err)
		}
		base.Members = append(base.Members, m)
		if strings.HasPrefix(src.Name, "recordings/") {
			base.Recordings++
		}
	}
	mb, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := checkManifestSize(mb); err != nil {
		return Manifest{}, err
	}
	sig, err := signManifestBytes(signer, mb)
	if err != nil {
		return Manifest{}, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(mb)), ModTime: base.Created}); err != nil {
		return Manifest{}, err
	}
	if _, err := tw.Write(mb); err != nil {
		return Manifest{}, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestSigName, Mode: 0o600, Size: int64(len(sig)), ModTime: base.Created}); err != nil {
		return Manifest{}, err
	}
	if _, err := tw.Write(sig); err != nil {
		return Manifest{}, err
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, err
	}
	if err := zw.Close(); err != nil {
		return Manifest{}, err
	}
	if err := aw.Close(); err != nil {
		return Manifest{}, err
	}
	return base, nil
}

func checkManifestSize(b []byte) error {
	if len(b) > MaxManifestBytes {
		return fmt.Errorf("%w: %d bytes (at most %d)", ErrManifestSize, len(b), MaxManifestBytes)
	}
	return nil
}

func packFile(tw *tar.Writer, src Source) (Member, error) {
	f, err := os.Open(src.Path)
	if err != nil {
		return Member{}, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Member{}, err
	}
	if st.IsDir() {
		return Member{}, fmt.Errorf("is a directory")
	}
	if err := tw.WriteHeader(&tar.Header{Name: src.Name, Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}); err != nil {
		return Member{}, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), f)
	if err != nil {
		return Member{}, err
	}
	return Member{Name: src.Name, Size: n, SHA: hex.EncodeToString(h.Sum(nil))}, nil
}

// Archive is an unpacked backup: the directory the members were written to and the manifest
// they answer to.
type Archive struct {
	Dir      string
	Manifest Manifest
}

// Extract decrypts the archive at path with the identities, unpacks it into dir (created when
// missing) and checks every member against the manifest: a wrong identity never gets this far,
// a changed member or manifest is ErrTampered, and a missing or wrong signature is refused.
func Extract(path string, identities []age.Identity, verify ed25519.PublicKey, dir string) (*Archive, error) {
	f, err := os.Open(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	ar, err := age.Decrypt(f, identities...)
	if err != nil {
		return nil, err
	}
	zr, err := zstd.NewReader(ar)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	out := &Archive{Dir: dir}
	have := map[string]Member{}
	var manifestBytes, sigBytes []byte
	tr := tar.NewReader(zr)
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if hd.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%w: member %s is not a file", ErrFormat, hd.Name)
		}
		switch hd.Name {
		case manifestName:
			manifestBytes, err = readBounded(tr, MaxManifestBytes)
			if err != nil {
				return nil, err
			}
			continue
		case manifestSigName:
			sigBytes, err = readBounded(tr, 4<<10)
			if err != nil {
				return nil, err
			}
			continue
		}
		if err := allowedMember(hd.Name); err != nil {
			return nil, err
		}
		target, err := secureJoin(dir, hd.Name)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		w, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // inside the extraction directory, of a name the manifest vouches for
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(tr, hd.Size+1))
		cerr := w.Close()
		if err != nil {
			return nil, err
		}
		if cerr != nil {
			return nil, cerr
		}
		if n != hd.Size {
			return nil, fmt.Errorf("%w: member %s is short", ErrTampered, hd.Name)
		}
		have[hd.Name] = Member{Name: hd.Name, Size: n, SHA: hex.EncodeToString(h.Sum(nil))}
	}
	if len(manifestBytes) == 0 {
		return nil, fmt.Errorf("%w: no manifest inside", ErrFormat)
	}
	if len(sigBytes) == 0 {
		return nil, fmt.Errorf("%w: no manifest signature inside", ErrUnsigned)
	}
	if err := verifyManifestBytes(verify, manifestBytes, sigBytes); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(manifestBytes, &out.Manifest); err != nil {
		return nil, fmt.Errorf("%w: the manifest is unreadable: %w", ErrFormat, err)
	}
	if out.Manifest.Format == 0 {
		return nil, fmt.Errorf("%w: no manifest inside", ErrFormat)
	}
	if out.Manifest.Format > FormatVersion {
		return nil, fmt.Errorf("%w: format %d is newer than this Beacon knows (%d)", ErrFormat, out.Manifest.Format, FormatVersion)
	}
	byName := map[string]bool{}
	for _, m := range out.Manifest.Members {
		if err := allowedMember(m.Name); err != nil {
			return nil, err
		}
		got, ok := have[m.Name]
		if !ok {
			return nil, fmt.Errorf("%w: member %s is missing", ErrTampered, m.Name)
		}
		if got.Size != m.Size || got.SHA != m.SHA {
			return nil, fmt.Errorf("%w: member %s differs from the manifest", ErrTampered, m.Name)
		}
		byName[m.Name] = true
	}
	for name := range have {
		if !byName[name] {
			return nil, fmt.Errorf("%w: member %s is not in the manifest", ErrTampered, name)
		}
	}
	return out, nil
}

func readBounded(r io.Reader, max int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > max {
		return nil, fmt.Errorf("%w: %d bytes (at most %d)", ErrManifestSize, len(b), max)
	}
	return b, nil
}

// allowedMember reports whether name is a path a backup may hold: db/, recordings/, config/ or
// secrets/, and never a traversal or a file at the archive's root (the live database, the
// signing key).
func allowedMember(name string) error {
	name = filepath.ToSlash(name)
	if name == "" || strings.Contains(name, `\`) {
		return fmt.Errorf("%w: member %s", ErrMemberName, name)
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return fmt.Errorf("%w: member %s", ErrMemberName, name)
	}
	for _, p := range []string{"db/", "recordings/", "config/", "secrets/"} {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			return nil
		}
	}
	return fmt.Errorf("%w: member %s (only db/, recordings/, config/, secrets/)", ErrMemberName, name)
}

// secureJoin keeps the extraction inside dir: no absolute paths, no .., no drive letters.
func secureJoin(dir, name string) (string, error) {
	name = filepath.ToSlash(name)
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, ":") {
		return "", fmt.Errorf("%w: member %s", ErrFormat, name)
	}
	return filepath.Join(dir, filepath.FromSlash(name)), nil
}

// ArchiveName builds the file name for a backup of kind made at t on host.
func ArchiveName(host string, t time.Time, kind string) string {
	return fmt.Sprintf("beacon-%s-%s-%s%s", host, t.UTC().Format("20060102-150405"), kind, Suffix)
}

// ParseName reads a backup's file name back: the host, the time, the kind. The name is the
// file's own name, not a path: a key under another prefix (other/beacon-…) is not a backup of
// ours. The name ends with …-<YYYYMMDD>-<HHMMSS>-<kind> and the host may hold dashes itself, so
// it is read from the right.
func ParseName(name string) (host string, t time.Time, kind string, ok bool) {
	name = filepath.ToSlash(name)
	if name == "" || strings.Contains(name, "/") {
		return "", time.Time{}, "", false
	}
	s, ok := strings.CutPrefix(name, "beacon-")
	if !ok {
		return "", time.Time{}, "", false
	}
	s, ok = strings.CutSuffix(s, Suffix)
	if !ok {
		return "", time.Time{}, "", false
	}
	parts := strings.Split(s, "-")
	if len(parts) < 4 {
		return "", time.Time{}, "", false
	}
	kind = parts[len(parts)-1]
	if !knownKind(kind) {
		return "", time.Time{}, "", false
	}
	when, err := time.ParseInLocation("20060102-150405", parts[len(parts)-3]+"-"+parts[len(parts)-2], time.UTC)
	if err != nil {
		return "", time.Time{}, "", false
	}
	return strings.Join(parts[:len(parts)-3], "-"), when, kind, true
}

func knownKind(kind string) bool {
	return kind == KindFull || kind == KindIncr || kind == KindDB
}
