package store

import (
	"crypto/rand"

	"github.com/openlinkos/beacon/internal/kv"
)

// SecretSize is the length of the secrets Secret keeps.
const SecretSize = 32

// Secret returns the 32-byte secret called name, making it at random the first time it is asked for.
// Such a secret signs what Beacon itself checks later, such as the links to its pages; it never leaves
// the database and is never shown.
func (s *Store) Secret(name string) ([]byte, error) {
	key := []byte("secret:" + name)
	var out []byte
	err := s.db.Update(func(tx kv.Tx) error {
		if v := tx.Get(bucketMeta, key); len(v) == SecretSize {
			out = append([]byte(nil), v...)
			return nil
		}
		out = make([]byte, SecretSize)
		if _, err := rand.Read(out); err != nil {
			return err
		}
		return tx.Put(bucketMeta, key, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteSecret removes the named secret. The settings use this once, after their rows have
// moved onto the vault's signature key, so a key that lived in the same database cannot
// re-sign a forged row on the next start.
func (s *Store) DeleteSecret(name string) error {
	return s.db.Update(func(tx kv.Tx) error {
		return tx.Delete(bucketMeta, []byte("secret:"+name))
	})
}
