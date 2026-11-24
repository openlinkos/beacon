package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/kv"
)

// ParseSSHKey reads an authorized_keys line into a public key and its SHA256 fingerprint.
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

func recordWithSSHKey[T any](tx kv.Tx, bucket []byte, fp string, keys func(T) []SSHKey) (T, bool) {
	var out T
	found := false
	tx.Range(bucket, kv.Range{}, func(_, v []byte) bool {
		var row T
		if json.Unmarshal(v, &row) == nil && hasSSHKey(keys(row), fp) {
			out, found = row, true
		}
		return !found
	})
	return out, found
}

func hasSSHKey(keys []SSHKey, fp string) bool {
	return slices.ContainsFunc(keys, func(k SSHKey) bool { return k.Fingerprint == fp })
}

// KeyHolder names the account, agent or token that holds the SSH key, even if it cannot sign in.
func (s *Store) KeyHolder(fp string) string {
	var holder string
	_ = s.db.View(func(tx kv.Tx) error {
		holder = keyHolder(tx, fp)
		return nil
	})
	return holder
}

func keyHolder(tx kv.Tx, fp string) string {
	if t, ok := recordWithSSHKey(tx, bucketTokens, fp, func(t Token) []SSHKey { return t.SSHKeys }); ok {
		return "token " + t.Name
	}
	if a, ok := recordWithSSHKey(tx, bucketAgents, fp, func(a Agent) []SSHKey { return a.SSHKeys }); ok {
		return "agent " + a.Name
	}
	if a, ok := accountBySSHKey(tx, fp); ok {
		return "user " + a.Username
	}
	return ""
}

func accountBySSHKey(tx kv.Tx, fp string) (Account, bool) {
	return recordWithSSHKey(tx, bucketAccounts, fp, func(a Account) []SSHKey { return a.SSHKeys })
}

func checkSSHKeyOwner(tx kv.Tx, fp, owner string) error {
	if holder := keyHolder(tx, fp); holder != "" && holder != owner {
		return fmt.Errorf("%w: the key %s belongs to %s", ErrExists, fp, holder)
	}
	if m, ok := machineByConnectorKey(tx, fp); ok {
		return fmt.Errorf("%w: the key %s is machine %s's connector key", ErrExists, fp, m.Name)
	}
	return nil
}

// AddAccountSSHKey registers a person's public key while the account has SSH access.
func (s *Store) AddAccountSSHKey(username string, k SSHKey) error {
	return s.db.Update(func(tx kv.Tx) error {
		a, ok := getAccount(tx, username)
		if !ok {
			return ErrNotFound
		}
		if a.Disabled || !slices.Contains(RoleScopes(a.Role), "machines:ssh") {
			return ErrDisabled
		}
		if err := checkSSHKeyOwner(tx, k.Fingerprint, "user "+username); err != nil {
			return err
		}
		if hasSSHKey(a.SSHKeys, k.Fingerprint) {
			return nil
		}
		if len(a.SSHKeys) >= MaxSSHKeys {
			return fmt.Errorf("an account holds at most %d keys; remove one first", MaxSSHKeys)
		}
		a.SSHKeys = append(a.SSHKeys, k)
		return putAccount(tx, a)
	})
}

// RemoveAccountSSHKey revokes a person's SSH key, including after the person lost SSH access.
func (s *Store) RemoveAccountSSHKey(username, fp string) error {
	return s.db.Update(func(tx kv.Tx) error {
		a, ok := getAccount(tx, username)
		if !ok || !hasSSHKey(a.SSHKeys, fp) {
			return ErrNotFound
		}
		a.SSHKeys = slices.DeleteFunc(a.SSHKeys, func(k SSHKey) bool { return k.Fingerprint == fp })
		return putAccount(tx, a)
	})
}
