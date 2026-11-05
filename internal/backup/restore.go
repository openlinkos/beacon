package backup

// The restore: decrypt with the owner's identity, check the manifest signature, unpack, check
// every member against the manifest, and verify what was the backup's whole reason to exist —
// the database's integrity and its audit chain. A dry run stops in a temporary directory; a
// real one writes the database, the recordings, the configuration and the secrets into the
// target directory (which the operator points at the data directory when recovering for real,
// or at a scratch directory when proving the chain elsewhere, as a drill does).
//
// Several archives may be given, oldest first: an incremental one carries the recordings since
// the one before it, so a complete set is the newest full plus the incrementals that follow.
// Every archive holds a complete database snapshot, so the last one's database is the database.
//
// Nothing is moved into the target until every archive has been extracted and checked. A
// restore into the live data directory needs an explicit confirm, copies the current files
// aside first, writes each member through a temporary name, and rolls back on failure.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// Restored is what one restore left behind and what it found on the way.
type Restored struct {
	Dir       string     // where the members were written
	Databases []string   // the database members it placed, in the order given
	Manifests []Manifest // one per archive, in the order given
	Aside     string     // the copy of the live directory taken before a confirmed live restore
}

// ErrNeedConfirm means a restore into the live data directory was asked without -confirm.
var ErrNeedConfirm = errors.New("restoring into the live data directory needs -confirm")

// ErrDataVersion means the archive's application data version is newer than this Beacon.
var ErrDataVersion = errors.New("the backup's data version is newer than this Beacon")

// RestoreOpts is one restore: the archives, who can decrypt them, the key that checks the
// manifest, and where the members go.
type RestoreOpts struct {
	Paths      []string
	Identities []age.Identity
	Verify     ed25519.PublicKey
	Dir        string
	Live       bool // restoring into the running data directory
	Confirm    bool // required when Live
	Now        func() time.Time
}

// Restore unpacks the archives at paths into dir (created when missing) with the identities,
// verifying the signature, members, the database's integrity and the audit chain of each. Order
// matters for the recordings: give the full first, then the incrementals.
func Restore(ctx context.Context, o RestoreOpts) (*Restored, error) {
	if len(o.Paths) == 0 {
		return nil, errors.New("no backup file given")
	}
	if o.Live && !o.Confirm {
		return nil, ErrNeedConfirm
	}
	if len(o.Verify) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: a restore needs the manifest signing public key", ErrUnsigned)
	}
	now := o.Now
	if now == nil {
		now = timeNow
	}
	parent := filepath.Dir(o.Dir)
	if parent == "" || parent == "." {
		parent = o.Dir
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(parent, filepath.Base(o.Dir)+".restore-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	out := &Restored{Dir: o.Dir}
	for i, p := range o.Paths {
		if i > 0 {
			if _, _, kind, ok := ParseName(filepath.Base(p)); ok && kind == KindFull {
				return nil, fmt.Errorf("%s: a full archive comes before the incrementals, not after", p)
			}
		}
		tmp, err := os.MkdirTemp(parent, filepath.Base(o.Dir)+".extract-")
		if err != nil {
			return nil, err
		}
		ar, err := Extract(p, o.Identities, o.Verify, tmp)
		if err != nil {
			_ = os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		dv := ar.Manifest.DataVersion
		if dv == 0 {
			dv = 1 // archives from this unreleased line before the field was set
		}
		if dv > DataVersion {
			_ = os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s: %w (%d; this Beacon is %d)", p, ErrDataVersion, ar.Manifest.DataVersion, DataVersion)
		}
		for _, m := range ar.Manifest.Members {
			if err := allowedMember(m.Name); err != nil {
				_ = os.RemoveAll(tmp)
				return nil, err
			}
			src := filepath.Join(ar.Dir, filepath.FromSlash(m.Name))
			dst := filepath.Join(staging, filepath.FromSlash(m.Name))
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				_ = os.RemoveAll(tmp)
				return nil, err
			}
			if err := os.Rename(src, dst); err != nil {
				if err := copyFile(src, dst); err != nil {
					_ = os.RemoveAll(tmp)
					return nil, fmt.Errorf("stage %s: %w", m.Name, err)
				}
			}
		}
		_ = os.RemoveAll(tmp)
		if err := verifyStagedDatabase(ctx, staging, ar.Manifest, p); err != nil {
			return nil, err
		}
		out.Manifests = append(out.Manifests, ar.Manifest)
	}

	var aside string
	if o.Live {
		aside = filepath.Join(parent, filepath.Base(o.Dir)+".pre-restore-"+now().UTC().Format("20060102-150405"))
		if err := snapshotLive(o.Dir, aside); err != nil {
			return nil, fmt.Errorf("copy the live data aside: %w", err)
		}
		out.Aside = aside
	}

	if err := applyTree(staging, o.Dir); err != nil {
		if o.Live && aside != "" {
			if rerr := restoreLive(aside, o.Dir); rerr != nil {
				return nil, fmt.Errorf("the restore failed: %w; rolling back from %s: %w", err, aside, rerr)
			}
		} else if !fileExists(o.Dir) || dirEmpty(o.Dir) {
			_ = os.RemoveAll(o.Dir)
		}
		return nil, err
	}
	if fileExists(filepath.Join(o.Dir, "db", "beacon.db")) {
		out.Databases = append(out.Databases, filepath.Join(o.Dir, "db", "beacon.db"))
	}
	return out, nil
}

// Drill proves the chain somewhere else: restore into a scratch directory, verify, remove it
// (keep says otherwise), and report the drill's time so the caller can record it. What comes
// back says what was proved; nothing touches a running service.
func Drill(ctx context.Context, paths []string, identities []age.Identity, verify ed25519.PublicKey, keep bool) (*Restored, time.Duration, error) {
	started := timeNow()
	dir, err := os.MkdirTemp("", "beacon-drill-")
	if err != nil {
		return nil, 0, err
	}
	if !keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}
	res, err := Restore(ctx, RestoreOpts{
		Paths: paths, Identities: identities, Verify: verify, Dir: filepath.Join(dir, "restored"),
	})
	if err != nil {
		if !keep {
			_ = os.RemoveAll(dir)
		}
		return nil, 0, err
	}
	return res, timeNow().Sub(started), nil
}

func verifyStagedDatabase(ctx context.Context, staging string, man Manifest, archive string) error {
	db := filepath.Join(staging, "db", "beacon.db")
	bb := filepath.Join(staging, "db", "status.db")
	switch {
	case fileExists(db):
		if err := IntegrityCheck(ctx, db); err != nil {
			return fmt.Errorf("%s: the restored database: %w", archive, err)
		}
		res, err := VerifyAudit(ctx, db)
		if err != nil {
			return fmt.Errorf("%s: the restored audit chain: %w", archive, err)
		}
		if res.Head != man.AuditHead {
			return fmt.Errorf("%s: the restored chain's head differs from the manifest's", archive)
		}
	case fileExists(bb):
		// a pre-migration archive: its bbolt file migrates when the service next starts
	default:
		return fmt.Errorf("%s: no database member inside", archive)
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func dirEmpty(p string) bool {
	ents, err := os.ReadDir(p)
	return err == nil && len(ents) == 0
}

// PlaceDatabase moves the restored database member to where the service opens it, through a
// temporary file, and reports a failure to remove the old WAL side files instead of ignoring it.
func PlaceDatabase(memberDB, dataDir string) error {
	dst := filepath.Join(dataDir, "beacon.db")
	tmp := dst + ".restore-tmp"
	if err := copyFile(memberDB, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	var failed []string
	for _, side := range []string{dst + "-wal", dst + "-shm"} {
		if err := os.Remove(side); err != nil && !os.IsNotExist(err) {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("the database is in place at %s, but the old WAL files could not be removed: %s", dst, strings.Join(failed, "; "))
	}
	return nil
}

// applyTree writes every regular file in src into dst through a temporary name, and on failure
// removes the files it created so a half-applied tree is not left behind.
func applyTree(src, dst string) (err error) {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	var created []string
	defer func() {
		if err == nil {
			return
		}
		for i := len(created) - 1; i >= 0; i-- {
			_ = os.Remove(created[i])
		}
	}()
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if err := allowedMember(name); err != nil {
			return err
		}
		target, err := secureJoin(dst, name)
		if err != nil {
			return err
		}
		if failApplyName != "" && name == failApplyName {
			return fmt.Errorf("place %s: injected failure", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil { //nolint:gosec // dest is the restore target; names passed allowedMember and secureJoin
			return err
		}
		tmp := target + ".restore-tmp"
		if err := copyFile(path, tmp); err != nil {
			return fmt.Errorf("place %s: %w", name, err)
		}
		if err := os.Rename(tmp, target); err != nil { //nolint:gosec // dest is the restore target; names passed allowedMember and secureJoin
			_ = os.Remove(tmp) //nolint:gosec // the temporary we just wrote
			return fmt.Errorf("place %s: %w", name, err)
		}
		created = append(created, target)
		return nil
	})
}

// failApplyName is the member a test asks applyTree to fail on; empty in production.
var failApplyName string

func snapshotLive(dir, aside string) error {
	if err := os.MkdirAll(aside, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"beacon.db", "beacon.db-wal", "beacon.db-shm", "db", "recordings", "config", "secrets"} {
		src := filepath.Join(dir, name)
		st, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		dst := filepath.Join(aside, name)
		if st.IsDir() {
			if err := copyDir(src, dst); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func restoreLive(aside, dir string) error {
	for _, name := range []string{"beacon.db", "beacon.db-wal", "beacon.db-shm", "db", "recordings", "config", "secrets"} {
		src := filepath.Join(aside, name)
		st, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				_ = os.RemoveAll(filepath.Join(dir, name))
				continue
			}
			return err
		}
		dst := filepath.Join(dir, name)
		_ = os.RemoveAll(dst)
		if st.IsDir() {
			if err := copyDir(src, dst); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return copyFile(path, target)
	})
}

// Describe summarises a manifest for a person: what it holds, without opening anything.
func Describe(m Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "backup of %s, %s, %s", m.Host, m.Created.Format(time.RFC3339), m.Kind)
	if m.Note != "" {
		fmt.Fprintf(&b, " (%s)", m.Note)
	}
	fmt.Fprintf(&b, ": %d members, %d recordings", len(m.Members), m.Recordings)
	if m.DataVersion > 0 {
		fmt.Fprintf(&b, ", data version %d", m.DataVersion)
	}
	if len(m.Tables) > 0 {
		fmt.Fprintf(&b, ", %d tables (schema %d)", len(m.Tables), m.Schema)
	}
	if m.AuditHead != "" {
		fmt.Fprintf(&b, ", audit head %s", short(m.AuditHead))
	}
	return b.String()
}

// SameDataDir reports whether a and b name the same directory.
func SameDataDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}
