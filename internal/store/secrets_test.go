package store

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Secret("pages")
	if err != nil || len(a) != SecretSize || bytes.Equal(a, make([]byte, SecretSize)) {
		t.Fatalf("first secret: %x %v", a, err)
	}
	b, _ := s.Secret("pages")
	if !bytes.Equal(a, b) {
		t.Error("the secret changes between calls")
	}
	if c, _ := s.Secret("other"); bytes.Equal(a, c) {
		t.Error("two names share a secret")
	}
	_ = s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if d, _ := s.Secret("pages"); !bytes.Equal(a, d) {
		t.Error("the secret does not survive a reopen")
	}
	if s.Time("secret:pages").IsZero() != true || s.Counter("secret:pages") != 0 {
		t.Error("the secret is not read as a time or a counter")
	}
}
