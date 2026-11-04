package s3

// Store is the recordings' Files interface (internal/sshgate) over one bucket: what a
// primary/standby pair shares, so the standby replays what the primary recorded. A recording is
// buffered as it is written — a session's recording is capped at what the channel records anyway
// (16 MiB) — and sent as one object when the session closes it.

import (
	"bytes"
	"context"
	"fmt"
	"io"
)

// Store implements sshgate.Files over the bucket.
type Store struct {
	c *Client
}

// NewStore keeps one client for the bucket.
func NewStore(o Options) (*Store, error) {
	c, err := New(o)
	if err != nil {
		return nil, err
	}
	return &Store{c: c}, nil
}

// uploader buffers one recording and sends it as one object when it closes. Close sends it; a
// session that never closes leaves nothing.
type uploader struct {
	c    *Client
	name string
	buf  bytes.Buffer
	done bool
}

// Create implements the Files interface.
func (s *Store) Create(name string) (io.WriteCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return &uploader{c: s.c, name: name}, nil
}

// WriteFile implements the Files interface.
func (s *Store) WriteFile(name string, b []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	return s.c.Put(context.Background(), name, b)
}

// ReadFile implements the Files interface.
func (s *Store) ReadFile(name string) ([]byte, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return s.c.GetAll(context.Background(), name)
}

// Open implements the Files interface.
func (s *Store) Open(name string) (io.ReadCloser, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	return s.c.Get(context.Background(), name)
}

// Remove implements the Files interface.
func (s *Store) Remove(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	return s.c.Remove(context.Background(), name)
}

func (u *uploader) Write(b []byte) (int, error) { return u.buf.Write(b) }

// Close sends the recording.
func (u *uploader) Close() error {
	if u.done {
		return nil
	}
	u.done = true
	return u.c.Put(context.Background(), u.name, u.buf.Bytes())
}

// checkName keeps object keys plain relative paths.
func checkName(name string) error {
	if name == "" || name[0] == '/' || containsDotDot(name) {
		return fmt.Errorf("s3: the object key %q is not a plain relative path", name)
	}
	return nil
}

func containsDotDot(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}
