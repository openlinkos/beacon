package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

const keySize = 32

var errKey = errors.New("the team's credential key is invalid or does not match the database")

// ReadKey reads a 256-bit key from the file named by the administrator, as 64 hexadecimal digits.
func ReadKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator names the key file
	if err != nil {
		return nil, errors.New("cannot read credentials.key_file")
	}
	defer clear(raw)
	key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != keySize {
		clear(key)
		return nil, errKey
	}
	return key, nil
}

// The AES-GCM envelope and separated HKDF policy key follow the vault's existing key design.
func derives(key []byte) (cipher.AEAD, []byte, error) {
	if len(key) != keySize {
		return nil, nil, errKey
	}
	enc, err := hkdf.Key(sha256.New, key, nil, "beacon/team-credential/encryption/v1", keySize)
	if err != nil {
		return nil, nil, errKey
	}
	defer clear(enc)
	mac, err := hkdf.Key(sha256.New, key, nil, "beacon/team-credential/policy/v1", keySize)
	if err != nil {
		return nil, nil, errKey
	}
	block, err := aes.NewCipher(enc)
	if err != nil {
		return nil, nil, errKey
	}
	a, err := cipher.NewGCM(block)
	return a, mac, err
}

func seal(a cipher.AEAD, value []byte, context string) ([]byte, error) {
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, value, []byte(context)), nil
}

func unseal(a cipher.AEAD, box []byte, context string) ([]byte, error) {
	if len(box) < a.NonceSize()+a.Overhead() {
		return nil, errKey
	}
	p, err := a.Open(nil, box[:a.NonceSize()], box[a.NonceSize():], []byte(context))
	if err != nil {
		return nil, errKey
	}
	return p, nil
}
