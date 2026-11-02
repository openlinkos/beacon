package store

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// ParseSSHKey reads an authorized_keys line into what a token or an agent keeps: the key without its
// comment, and its SHA256 fingerprint. A certificate is refused: the key itself is given.
func ParseSSHKey(line string) (SSHKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return SSHKey{}, fmt.Errorf("not a public key in the authorized_keys form (ssh-ed25519 AAAA…): %w", err)
	}
	if _, isCert := k.(*ssh.Certificate); isCert {
		return SSHKey{}, errors.New("give the public key itself, not a certificate")
	}
	return SSHKey{Key: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), Fingerprint: ssh.FingerprintSHA256(k)}, nil
}

// CleanComment makes a key's comment safe to show: one line of at most 100 characters.
func CleanComment(c string) string {
	c = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(c))
	if utf8.RuneCountInString(c) > 100 {
		c = string([]rune(c)[:100])
	}
	return strings.TrimSpace(c)
}
