// Package signedhead is the signed snapshot of a table's live rows: a name, a monotonic epoch,
// and the SHA-256 of each stored row. The signature is HMAC-SHA256 with a type prefix and
// length-prefixed fields, so holding the database file alone cannot widen a limit by putting
// rows back, deleting them, or rolling the head to an older pair. An epochs file beside the
// secrets is the rollback anchor; Beacon does not rebuild a head by scanning rows except on
// the explicit Rebuild command.
package signedhead

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
)

// TypePrefix is written first in the HMAC so a signature for something else cannot be reused.
const TypePrefix = "beacon/signed-head/v1\n"

// Errors the tables return.
var (
	ErrFailsafe = errors.New("signed head is failsafe")
	ErrNoKey    = errors.New("no signature key")
)

// Head is one signed snapshot. Entries maps a row key to the hex SHA-256 of the exact stored
// bytes. Sig is the HMAC; it is not part of the canonical form.
type Head struct {
	Name    string
	Epoch   uint64
	Entries map[string]string
	Sig     []byte
}

// HashRow is the hex SHA-256 of the exact stored row bytes.
func HashRow(row []byte) string {
	sum := sha256.Sum256(row)
	return hex.EncodeToString(sum[:])
}

// Sign is HMAC-SHA256 over the type prefix and the length-prefixed canonical fields.
func Sign(sk []byte, h Head) []byte {
	mac := hmac.New(sha256.New, sk)
	mac.Write([]byte(TypePrefix))
	mac.Write(canonical(h))
	return mac.Sum(nil)
}

// Verify reports whether h.Sig matches Sign(sk, h). An empty key never verifies.
func Verify(sk []byte, h Head) bool {
	if len(sk) == 0 || len(h.Sig) == 0 {
		return false
	}
	return hmac.Equal(h.Sig, Sign(sk, h))
}

func canonical(h Head) []byte {
	var buf bytes.Buffer
	writeLP(&buf, []byte(h.Name))
	var ep [8]byte
	binary.BigEndian.PutUint64(ep[:], h.Epoch)
	writeLP(&buf, ep[:])
	keys := make([]string, 0, len(h.Entries))
	for k := range h.Entries {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		writeLP(&buf, []byte(k))
		writeLP(&buf, []byte(h.Entries[k]))
	}
	return buf.Bytes()
}

func writeLP(buf *bytes.Buffer, p []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(p)))
	buf.Write(n[:])
	buf.Write(p)
}

type wire struct {
	Name    string            `json:"name"`
	Epoch   uint64            `json:"epoch"`
	Entries map[string]string `json:"entries"`
	Sig     []byte            `json:"sig"`
}

func encodeHead(h Head) ([]byte, error) {
	ents := h.Entries
	if ents == nil {
		ents = map[string]string{}
	}
	return json.Marshal(wire{Name: h.Name, Epoch: h.Epoch, Entries: ents, Sig: h.Sig})
}

func decodeHead(raw []byte) (Head, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Head{}, err
	}
	if w.Entries == nil {
		w.Entries = map[string]string{}
	}
	return Head(w), nil
}

func cloneEntries(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
