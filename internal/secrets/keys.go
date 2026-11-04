package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// The key hierarchy (the design's section 8.1):
//
//	root key RK (256 bits)
//	 ├─ collection key CK (one per collection, random 256 bits, wrapped by RK with AES-256-GCM;
//	 │   every version is kept, so it can be rotated)
//	 │   └─ item versions' details (AES-256-GCM, 96-bit random nonce;
//	 │        AAD = "beacon/item/v1" | item ID | version | collection ID | CK version)
//	 └─ signature key SK = HKDF(RK, "beacon/policy-mac/v1")
//	      signs the policy rows (collections, permissions, tags, items) and the settings rows
//
// Plaintext keys are held only while one operation runs; nothing caches them. Go cannot promise to
// zero freed memory, so a copied key may linger until the runtime reuses the pages — the documents
// say so plainly rather than promise what the language does not.

// Key sizes in bytes.
const (
	RKSize = 32
	CKSize = 32
	SKSize = 32
)

// The HKDF info strings, fixed by the design.
const (
	infoPolicyMAC = "beacon/policy-mac/v1"
	infoRootKey   = "beacon/root-key/v1"
)

// ErrCrypto is every failure of the cryptography: a ciphertext that does not authenticate under
// the key and additional data it claims, or a key of the wrong size. It never says what the data
// was.
var ErrCrypto = errors.New("the ciphertext does not authenticate under this key")

// aead is the AES-256-GCM cipher a 32-byte key gives. Plaintext keys are zeroed as soon as their
// operation ends, best effort.
func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != CKSize {
		return nil, fmt.Errorf("%w: a key is %d bytes", ErrCrypto, CKSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// randomKey makes a new random 256-bit key.
func randomKey() ([]byte, error) {
	k := make([]byte, CKSize)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, err
	}
	return k, nil
}

// policyKey is SK = HKDF-SHA256(RK, "beacon/policy-mac/v1").
func policyKey(rk []byte) []byte {
	out := make([]byte, SKSize)
	_, _ = io.ReadFull(hkdf.Expand(sha256.New, rk, []byte(infoPolicyMAC)), out)
	return out
}

// rootKeyFromPassphrase is the passphrase mode's two-part derivation: the argon2id digest of the
// passphrase is the input key material, the file key the salt — both parts are needed, so taking
// the disk alone is not enough (RK = HKDF(argon2id(passphrase), file key)).
func rootKeyFromPassphrase(digest, fileKey []byte) []byte {
	prk := hkdf.Extract(sha256.New, digest, fileKey)
	out := make([]byte, RKSize)
	_, _ = io.ReadFull(hkdf.Expand(sha256.New, prk, []byte(infoRootKey)), out)
	return out
}

// wrap encrypts plaintext under key with AES-256-GCM and the additional data ad, giving
// nonce ‖ ciphertext ‖ tag.
func wrap(key, plaintext, ad []byte) ([]byte, error) {
	a, err := aead(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plaintext, ad), nil
}

// unwrap is wrap's other side; a wrong key or additional data is ErrCrypto, never the plaintext.
func unwrap(key, ct, ad []byte) ([]byte, error) {
	a, err := aead(key)
	if err != nil {
		return nil, err
	}
	if len(ct) < a.NonceSize()+a.Overhead() {
		return nil, ErrCrypto
	}
	nonce, body := ct[:a.NonceSize()], ct[a.NonceSize():]
	out, err := a.Open(nil, nonce, body, ad)
	if err != nil {
		return nil, fmt.Errorf("%w", ErrCrypto)
	}
	return out, nil
}

// The AAD strings that bind a ciphertext to what it is.
const (
	adItemV1       = "beacon/item/v1"
	adAttachmentV1 = "beacon/attachment/v1"
	adKeyCheckV1   = "beacon/keycheck/v1"
	adKeyWrapV1    = "beacon/collection-key/v1"
)

// itemAAD is the additional data of an item version's details: the design's
// "beacon/item/v1" | item ID | version | collection ID | CK version. Swapping ciphertexts between
// items, versions or collections fails the authentication, which is what the binding is for.
func itemAAD(item string, version int64, collection string, ckVersion int64) []byte {
	return lengthPrefixed(adItemV1, item, i64(version), collection, i64(ckVersion))
}

// attachmentAAD binds an attachment's ciphertext to its item, version, collection, key version and
// its own name.
func attachmentAAD(item string, version int64, collection string, ckVersion int64, name string) []byte {
	return lengthPrefixed(adAttachmentV1, item, i64(version), collection, i64(ckVersion), name)
}

// keyWrapAAD binds a wrapped collection key to its collection and version.
func keyWrapAAD(collection string, ckVersion int64) []byte {
	return lengthPrefixed(adKeyWrapV1, collection, i64(ckVersion))
}

// keyCheckAAD is the additional data of the key check, which tells a right root key from a wrong
// one before anything is attempted with it.
func keyCheckAAD() []byte { return []byte(adKeyCheckV1) }

// lengthPrefixed joins the parts with each one's length in front, so no two lists of parts can be
// the same bytes.
func lengthPrefixed(parts ...string) []byte {
	var out []byte
	for _, p := range parts {
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(p)))
		out = append(out, l[:]...)
		out = append(out, p...)
	}
	return out
}

func i64(v int64) string { return fmt.Sprintf("%d", v) }

// signRow is the policy rows' signature: HMAC-SHA256 under SK over the row's fields, each
// NUL-terminated (none of them may hold a NUL), without the signature itself.
func signRow(sk []byte, parts ...string) []byte {
	h := hmac.New(sha256.New, sk)
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

// verifyRow is signRow's check, in constant time.
func verifyRow(sk []byte, sig []byte, parts ...string) bool {
	return hmac.Equal(sig, signRow(sk, parts...))
}

// keyID is a short, stable name of a key for the logs: the first eight hex digits of its SHA-256.
// It identifies without revealing.
func keyID(key []byte) string {
	sum := sha256.Sum256(key)
	return fmt.Sprintf("%x", sum[:4])
}
