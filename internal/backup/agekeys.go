package backup

// The age key material: recipients the backups are encrypted to (age X25519 public keys or
// ssh-ed25519 public keys, as the plan's §11 names them) and the identities a restore decrypts
// with. The server only ever holds the public halves; the private key belongs to the owner's
// recovery material, shown once when it is made and never stored by Beacon beyond that first
// hand-over.

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
)

// ParseRecipients turns public key lines into age recipients. A line may be an age public key
// (age1…) or an ssh-ed25519 public key, with or without the type prefix and comment an
// authorized_keys line carries; anything else is an error naming the line — a mistyped key must
// stop the configuration, not silently narrow who can read the backups.
func ParseRecipients(lines []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var r age.Recipient
		var err error
		if strings.HasPrefix(line, "age1") {
			r, err = age.ParseX25519Recipient(line)
		} else {
			r, err = agessh.ParseRecipient(line)
			if err != nil && !strings.HasPrefix(line, "ssh-") {
				// a bare base64 blob: try it as the key body of an ssh-ed25519 line
				r, err = agessh.ParseRecipient("ssh-ed25519 " + line)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("recipient %d: %w", i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// LoadRecipientsFile reads one public key per line from path (empty lines and # comments aside).
func LoadRecipientsFile(path string) ([]age.Recipient, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	var lines []string
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	return ParseRecipients(lines)
}

// ParseIdentities turns identity material into age identities: an age secret key
// (AGE-SECRET-KEY-1…, wherever the line sits — a bare key, age's own key file with its comment
// lines, or the recovery material Beacon writes, prose and all), or an unencrypted
// ssh-ed25519 private key (a PEM block). A key that needs a passphrase is an error: recovery
// material is kept where it can be read without one.
func ParseIdentities(b []byte) ([]age.Identity, error) {
	var out []age.Identity
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if !strings.HasPrefix(line, "AGE-SECRET-KEY-") {
			continue
		}
		id, err := age.ParseX25519Identity(line)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	// no age key line: an ssh private key, whole, as one file
	if strings.Contains(string(b), "-----BEGIN") {
		id, err := agessh.ParseIdentity(b)
		if err != nil {
			return nil, fmt.Errorf("the identity is neither an age secret key nor an unencrypted ssh-ed25519 key: %w", err)
		}
		return []age.Identity{id}, nil
	}
	return nil, fmt.Errorf("no identity in the material given: an age secret key (AGE-SECRET-KEY-1…) or an unencrypted ssh-ed25519 key file")
}

// LoadIdentity reads the identity (an age key file, an AGE-SECRET-KEY line, or an unencrypted
// ssh-ed25519 private key) from path.
func LoadIdentity(path string) ([]age.Identity, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	return ParseIdentities(b)
}

// GeneratedKey is a freshly made recovery key pair. The private half leaves the process through
// the caller (printed once, or written to the one-time recovery file); only the public half stays.
type GeneratedKey struct {
	Public  string // the age1… line, for the recipients list
	Private string // the AGE-SECRET-KEY-1… line, for the owner alone
}

// GenerateKey makes a pair.
func GenerateKey() (GeneratedKey, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return GeneratedKey{}, err
	}
	return GeneratedKey{Public: id.Recipient().String(), Private: id.String()}, nil
}

// RecoveryMaterial is the text handed to the owner exactly once when Beacon generates the key
// pair itself: the private key, the public key, and how to recover with them.
func RecoveryMaterial(k GeneratedKey, host string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Beacon backup recovery material for %s\n", host)
	fmt.Fprintf(&b, "=====================================================\n\n")
	fmt.Fprintf(&b, "Keep this file somewhere safe that is not the server (a password manager,\n")
	fmt.Fprintf(&b, "an offline drive). Whoever holds it can read every backup; the server itself\n")
	fmt.Fprintf(&b, "holds only the public key and cannot.\n\n")
	fmt.Fprintf(&b, "Private key (the identity a restore decrypts with):\n\n    %s\n\n", k.Private)
	fmt.Fprintf(&b, "Public key (already recorded on the server):\n\n    %s\n\n", k.Public)
	fmt.Fprintf(&b, "To recover, on any machine with the backup file:\n\n")
	fmt.Fprintf(&b, "    beacon backup restore beacon-<host>-<time>-<kind>.tar.zst.age \\\n")
	fmt.Fprintf(&b, "        -identity <file with the private key above> -into <empty directory>\n\n")
	fmt.Fprintf(&b, "To prove the chain works before you need it, run a drill there:\n\n")
	fmt.Fprintf(&b, "    beacon backup drill <the same file> -identity <the same file>\n\n")
	fmt.Fprintf(&b, "The archive carries the database, the configuration and the secrets directory,\n")
	fmt.Fprintf(&b, "so this key and one backup file are all a recovery needs. Delete this file from\n")
	fmt.Fprintf(&b, "the server once you have stored it elsewhere; Beacon reminds you until you do.\n")
	return b.String()
}
