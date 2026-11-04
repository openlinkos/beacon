package backup

// Small file helpers the package keeps to itself: atomic writes (a state file half-written is a
// state file gone), copies (the pre-migration bbolt member), and hashing (the read-back check).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

func jsonMarshalIndent(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil // the state and the manifest are always marshalable
	}
	return b
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// writeFileAtomic writes b to path through a temporary beside it, so a reader (or a crash) sees
// the old file or the new one, never a half of either.
func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// copyFile copies src to dst (mode 0600: a database copy is nobody else's to read).
func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // the pre-migration database the service itself named
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // beside the caller's file
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// hashFile streams path through SHA-256.
func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // the run's own archive
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
