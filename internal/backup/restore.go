package backup

// The restore: decrypt with the owner's identity, unpack, check every member against the
// manifest, and verify what was the backup's whole reason to exist — the database's integrity
// and its audit chain. A dry run stops in a temporary directory; a real one writes the
// database, the recordings, the configuration and the secrets into the target directory (which
// the operator points at the data directory when recovering for real, or at a scratch directory
// when proving the chain elsewhere, as a drill does).
//
// Several archives may be given, oldest first: an incremental one carries the recordings since
// the one before it, so a complete set is the newest full plus the incrementals that follow.
// Every archive holds a complete database snapshot, so the last one's database is the database.

import (
	"context"
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
}

// Restore unpacks the archives at paths into dir (created when missing) with the identities,
// verifying members, the database's integrity and the audit chain of each. Order matters for the
// recordings: give the full first, then the incrementals.
func Restore(ctx context.Context, paths []string, identities []age.Identity, dir string) (*Restored, error) {
	if len(paths) == 0 {
		return nil, errors.New("no backup file given")
	}
	out := &Restored{Dir: dir}
	for i, p := range paths {
		if i > 0 {
			if _, _, kind, ok := ParseName(p); ok && kind == "full" {
				return nil, fmt.Errorf("%s: a full archive comes before the incrementals, not after", p)
			}
		}
		tmp, err := os.MkdirTemp(filepath.Dir(dir), filepath.Base(dir)+".extract-")
		if err != nil {
			return nil, err
		}
		ar, err := Extract(p, identities, tmp)
		if err != nil {
			_ = os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		// the database member: checked where it landed, before anything is moved
		db := filepath.Join(ar.Dir, "db", "beacon.db")
		bb := filepath.Join(ar.Dir, "db", "status.db")
		switch {
		case fileExists(db):
			if err := IntegrityCheck(ctx, db); err != nil {
				_ = os.RemoveAll(tmp)
				return nil, fmt.Errorf("%s: the restored database: %w", p, err)
			}
			res, err := VerifyAudit(ctx, db)
			if err != nil {
				_ = os.RemoveAll(tmp)
				return nil, fmt.Errorf("%s: the restored audit chain: %w", p, err)
			}
			if res.Head != ar.Manifest.AuditHead {
				_ = os.RemoveAll(tmp)
				return nil, fmt.Errorf("%s: the restored chain's head differs from the manifest's", p)
			}
		case fileExists(bb):
			// a pre-migration archive: its bbolt file migrates when the service next starts
		default:
			_ = os.RemoveAll(tmp)
			return nil, fmt.Errorf("%s: no database member inside", p)
		}
		// the members into place, by the manifest's own list (a later archive's file meets the
		// earlier one's already there, which is the point)
		for _, m := range ar.Manifest.Members {
			src := filepath.Join(ar.Dir, filepath.FromSlash(m.Name))
			dst := filepath.Join(dir, filepath.FromSlash(m.Name))
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				_ = os.RemoveAll(tmp)
				return nil, err
			}
			if err := os.Rename(src, dst); err != nil {
				_ = os.RemoveAll(tmp)
				return nil, fmt.Errorf("place %s: %w", m.Name, err)
			}
		}
		_ = os.RemoveAll(tmp)
		if fileExists(filepath.Join(dir, "db", "beacon.db")) {
			out.Databases = append(out.Databases, filepath.Join(dir, "db", "beacon.db"))
		}
		out.Manifests = append(out.Manifests, ar.Manifest)
	}
	return out, nil
}

// Drill proves the chain somewhere else: restore into a scratch directory, verify, remove it
// (keep says otherwise), and report the drill's time so the caller can record it. What comes
// back says what was proved; nothing touches a running service.
func Drill(ctx context.Context, paths []string, identities []age.Identity, keep bool) (*Restored, time.Duration, error) {
	started := timeNow()
	dir, err := os.MkdirTemp("", "beacon-drill-")
	if err != nil {
		return nil, 0, err
	}
	if !keep {
		defer func() { _ = os.RemoveAll(dir) }()
	}
	res, err := Restore(ctx, paths, identities, filepath.Join(dir, "restored"))
	if err != nil {
		if !keep {
			_ = os.RemoveAll(dir)
		}
		return nil, 0, err
	}
	return res, timeNow().Sub(started), nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Describe summarises a manifest for a person: what it holds, without opening anything.
func Describe(m Manifest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "backup of %s, %s, %s", m.Host, m.Created.Format(time.RFC3339), m.Kind)
	if m.Note != "" {
		fmt.Fprintf(&b, " (%s)", m.Note)
	}
	fmt.Fprintf(&b, ": %d members, %d recordings", len(m.Members), m.Recordings)
	if len(m.Tables) > 0 {
		fmt.Fprintf(&b, ", %d tables (schema %d)", len(m.Tables), m.Schema)
	}
	if m.AuditHead != "" {
		fmt.Fprintf(&b, ", audit head %s", short(m.AuditHead))
	}
	return b.String()
}
