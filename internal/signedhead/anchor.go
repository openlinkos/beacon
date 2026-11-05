package signedhead

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

const epochsName = "epochs"

// fsyncFile and fsyncDir are the file and directory Sync calls; tests replace them to observe
// that AfterCommit fsyncs. Directory Sync may no-op on Windows; the file Sync must still run.
var (
	fsyncFile = func(f *os.File) error { return f.Sync() }
	fsyncDir  = func(f *os.File) error {
		err := f.Sync()
		if err == nil {
			return nil
		}
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
)

// FileAnchor is the rollback anchor: Dir+"/epochs", one line per head ("<name> <epoch>\n").
type FileAnchor struct {
	Dir string
}

func (a FileAnchor) path() string {
	if a.Dir == "" {
		return ""
	}
	return filepath.Join(a.Dir, epochsName)
}

// Record writes name's epoch, keeping the other heads, and fsyncs the file and the directory.
func (a FileAnchor) Record(name string, epoch uint64) error {
	if a.Dir == "" {
		return fmt.Errorf("no epochs directory")
	}
	if name == "" || strings.Contains(name, " ") {
		return fmt.Errorf("signed head name %q", name)
	}
	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		return err
	}
	m, err := a.readAll()
	if err != nil {
		// a corrupt file must not be overwritten with a single line; the caller treats it as failsafe
		return err
	}
	if m == nil {
		m = map[string]uint64{}
	}
	m[name] = epoch
	var b strings.Builder
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(&b, "%s %d\n", n, m[n])
	}
	path := a.path()
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // our own epochs file
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := fsyncFile(f); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := renameOver(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	d, err := os.Open(a.Dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return fsyncDir(d)
}

func renameOver(tmp, dest string) error {
	if err := os.Rename(tmp, dest); err == nil {
		return nil
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tmp, dest)
}

// Epoch is name's recorded epoch, or 0 when the file or the name is not there. A corrupt file
// is 0; Load treats a read error as failsafe instead of calling this.
func (a FileAnchor) Epoch(name string) uint64 {
	m, err := a.readAll()
	if err != nil || m == nil {
		return 0
	}
	return m[name]
}

func (a FileAnchor) readAll() (map[string]uint64, error) {
	path := a.path()
	if path == "" {
		return map[string]uint64{}, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // our own epochs file
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]uint64{}, nil
		}
		return nil, err
	}
	m := map[string]uint64{}
	for i, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, " ")
		if !ok || name == "" {
			return nil, fmt.Errorf("epochs file line %d", i+1)
		}
		epoch, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("epochs file line %d: %w", i+1, err)
		}
		m[name] = epoch
	}
	return m, nil
}
