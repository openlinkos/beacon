package backup

// Where backups go: local directories and S3-compatible stores, several at once (the plan's §11:
// a local copy plus one off the machine). Every destination can put, list, read back and delete;
// the manager uploads to each in turn and reads every upload back to compare size and hash.
// Everything streams: an archive is a file on disk from the moment it is built, and no whole
// backup is ever held in memory.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Destination is one place backups are written. Exactly one of Dir and S3 is set.
type Destination struct {
	// Name is what logs and lists call it (the directory, or endpoint/bucket/prefix).
	Name string
	Dir  string
	S3   *S3
}

// Entry is one backup at a destination, as a listing shows it.
type Entry struct {
	Name    string
	Size    int64
	When    time.Time
	Kind    string
	ModTime time.Time
}

// List returns the backups at the destination, newest first (a full and an incremental inside
// one second list the incremental first: it follows the full); files that are not backups are
// not listed (and never touched by retention).
func (d Destination) List(ctx context.Context) ([]Entry, error) {
	if d.Dir != "" {
		matches, err := filepath.Glob(filepath.Join(d.Dir, "beacon-*"+Suffix))
		if err != nil {
			return nil, err
		}
		var out []Entry
		for _, m := range matches {
			st, err := os.Stat(m)
			if err != nil {
				continue
			}
			name := filepath.Base(m)
			_, when, kind, ok := ParseName(name)
			if !ok {
				continue
			}
			out = append(out, Entry{Name: name, Size: st.Size(), When: when, Kind: kind, ModTime: st.ModTime()})
		}
		sortEntries(out)
		return out, nil
	}
	objects, err := d.S3.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, o := range objects {
		name := strings.TrimPrefix(o.Key, "/")
		_, when, kind, ok := ParseName(name)
		if !ok {
			continue
		}
		out = append(out, Entry{Name: name, Size: o.Size, When: when, Kind: kind, ModTime: o.LastModified})
	}
	sortEntries(out)
	return out, nil
}

// sortEntries orders newest first; of the same second, file incrementals then database-only
// snapshots then fulls, so a listing reads as the chain a restore wants.
func sortEntries(out []Entry) {
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].When.Equal(out[j].When) {
			return out[i].When.After(out[j].When)
		}
		return kindRank(out[i].Kind) < kindRank(out[j].Kind)
	})
}

func kindRank(kind string) int {
	switch kind {
	case KindIncr:
		return 0
	case KindDB:
		return 1
	default:
		return 2
	}
}

// Put streams src (size bytes) under name and reports the hash it landed with.
func (d Destination) Put(ctx context.Context, name string, src io.Reader, size int64) (string, error) {
	if d.Dir != "" {
		if err := os.MkdirAll(d.Dir, 0o700); err != nil {
			return "", err
		}
		tmp := filepath.Join(d.Dir, "."+name+".part")
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // inside the destination directory
		if err != nil {
			return "", err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(f, h), src)
		cerr := f.Close()
		if err != nil || cerr != nil {
			_ = os.Remove(tmp)
			if err != nil {
				return "", err
			}
			return "", cerr
		}
		if n != size {
			_ = os.Remove(tmp)
			return "", fmt.Errorf("wrote %d of %d bytes", n, size)
		}
		if err := os.Rename(tmp, filepath.Join(d.Dir, name)); err != nil {
			_ = os.Remove(tmp)
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	return d.S3.Put(ctx, name, src, size)
}

// ReadBack streams back what was written under name and reports its size and hash, for the
// after-upload check (the plan's: read back, compare size and hash).
func (d Destination) ReadBack(ctx context.Context, name string) (int64, string, error) {
	if d.Dir != "" {
		f, err := os.Open(filepath.Join(d.Dir, name)) //nolint:gosec // the name is one Beacon wrote
		if err != nil {
			return 0, "", err
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		n, err := io.Copy(h, f)
		if err != nil {
			return 0, "", err
		}
		return n, hex.EncodeToString(h.Sum(nil)), nil
	}
	return d.S3.HashOf(ctx, name)
}

// Delete removes the backup name (or the partial a failed upload may have left).
func (d Destination) Delete(ctx context.Context, name string) error {
	if d.Dir != "" {
		if err := os.Remove(filepath.Join(d.Dir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Remove(filepath.Join(d.Dir, "."+name+".part")); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return d.S3.Delete(ctx, name)
}

// walkFiles lists every regular file under dir, as member paths relative to it, sorted, with
// their modification times (the incremental watermark is compared against them). Symlinks are
// not followed and not packed: a backup holds what the server wrote.
func walkFiles(dir string) ([]Source, map[string]time.Time, error) {
	var out []Source
	mods := map[string]time.Time{}
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() || e.Type() != 0 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		out = append(out, Source{Name: filepath.ToSlash(rel), Path: path})
		mods[filepath.ToSlash(rel)] = info.ModTime()
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, mods, nil
}
