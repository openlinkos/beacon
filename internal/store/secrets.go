package store

import (
	"crypto/rand"

	bolt "go.etcd.io/bbolt"
)

// SecretSize is the length of the secrets Secret keeps.
const SecretSize = 32

// Secret returns the 32-byte secret called name, making it at random the first time it is asked for.
// Such a secret signs what Beacon itself checks later, such as the links to its pages; it never leaves
// the database and is never shown.
func (s *Store) Secret(name string) ([]byte, error) {
	key := []byte("secret:" + name)
	var out []byte
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketMeta)
		if v := b.Get(key); len(v) == SecretSize {
			out = append([]byte(nil), v...)
			return nil
		}
		out = make([]byte, SecretSize)
		if _, err := rand.Read(out); err != nil {
			return err
		}
		return b.Put(key, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
