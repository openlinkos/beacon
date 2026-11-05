package backup

// Manifest signing: age encrypts, it does not say who wrote the archive. A public age key is
// shown on the console and in the configuration, so anyone who can write the destination can
// pack a self-consistent archive the server would then restore. Each archive therefore carries
// a signature of the exact manifest.json bytes, made with an ed25519 key the server holds; the
// public half is in the recovery material and a restore refuses an unsigned or wrongly-signed
// archive.

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// SignPublicPrefix marks a manifest-signing public key, as recovery material writes it.
	SignPublicPrefix = "sign1:"
	signKeyFile      = "sign.key"
	signPubFile      = "sign.pub"
	manifestName     = "manifest.json"
	manifestSigName  = "manifest.sig"
)

// EnsureSignKey returns the server's manifest-signing private key, generating it once into dir
// (the public half is written beside it as sign.pub).
func EnsureSignKey(dir string) (ed25519.PrivateKey, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, signKeyFile)
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the key Beacon itself wrote
		if len(b) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("%s: not a manifest signing key", path)
		}
		priv := ed25519.PrivateKey(b)
		if err := writeFileAtomic(filepath.Join(dir, signPubFile), []byte(FormatSignPublic(priv.Public().(ed25519.PublicKey))+"\n")); err != nil {
			return nil, err
		}
		return priv, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(path, []byte(priv)); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(dir, signPubFile), []byte(FormatSignPublic(priv.Public().(ed25519.PublicKey))+"\n")); err != nil {
		return nil, err
	}
	return priv, nil
}

// FormatSignPublic is the sign1:… line recovery material and -verify-key accept.
func FormatSignPublic(pub ed25519.PublicKey) string {
	return SignPublicPrefix + base64.RawStdEncoding.EncodeToString(pub)
}

// ParseSignPublic reads a sign1:… line.
func ParseSignPublic(s string) (ed25519.PublicKey, error) {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, SignPublicPrefix)
	if !ok {
		return nil, fmt.Errorf("not a manifest signing public key")
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(rest))
	if err != nil {
		return nil, fmt.Errorf("manifest signing public key: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("manifest signing public key: want %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	return ed25519.PublicKey(b), nil
}

// ParseVerifyKey finds a sign1:… line in recovery material (or a file that holds only that line).
// Missing is (nil, nil): the caller can then require -verify-key.
func ParseVerifyKey(b []byte) (ed25519.PublicKey, error) {
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if !strings.HasPrefix(line, SignPublicPrefix) {
			continue
		}
		return ParseSignPublic(line)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

// LoadVerifyKey reads a signing public key from path.
func LoadVerifyKey(path string) (ed25519.PublicKey, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	pub, err := ParseVerifyKey(b)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		return nil, fmt.Errorf("%s: no manifest signing public key (a sign1:… line)", path)
	}
	return pub, nil
}

func signManifestBytes(signer ed25519.PrivateKey, manifest []byte) ([]byte, error) {
	if len(signer) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("a backup needs a manifest signing key")
	}
	return []byte(base64.RawStdEncoding.EncodeToString(ed25519.Sign(signer, manifest))), nil
}

func verifyManifestBytes(pub ed25519.PublicKey, manifest, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: a restore needs the manifest signing public key", ErrUnsigned)
	}
	line := strings.TrimSpace(string(sig))
	raw, err := base64.RawStdEncoding.DecodeString(line)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return fmt.Errorf("%w: the manifest signature is unreadable", ErrBadSignature)
	}
	if !ed25519.Verify(pub, manifest, raw) {
		return fmt.Errorf("%w: the manifest signature does not match", ErrBadSignature)
	}
	return nil
}

func signPublicOf(priv ed25519.PrivateKey) ed25519.PublicKey {
	if len(priv) != ed25519.PrivateKeySize {
		return nil
	}
	return priv.Public().(ed25519.PublicKey)
}
