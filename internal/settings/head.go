package settings

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/openlinkos/beacon/internal/kv"
)

func signChange(key []byte, ch Change) []byte {
	mac := hmac.New(sha256.New, key)
	for _, p := range []string{
		strconv.FormatInt(ch.Version, 10), ch.Actor, ch.Via, ch.Action,
		strconv.FormatInt(ch.Reverts, 10), ch.Key, ch.Scope, ch.Target,
		string(ch.Before), string(ch.After), ch.Reason, ch.PrevHash,
		strconv.FormatInt(ch.Time.UnixNano(), 10),
	} {
		mac.Write([]byte(p))
		mac.Write([]byte{0})
	}
	return mac.Sum(nil)
}

func hashChange(ch Change) string {
	mac := sha256.New()
	mac.Write([]byte(strconv.FormatInt(ch.Version, 10)))
	mac.Write([]byte(ch.PrevHash))
	mac.Write(ch.Before)
	mac.Write(ch.After)
	mac.Write([]byte(ch.Key))
	return hex.EncodeToString(mac.Sum(nil))
}

func (ch Change) verifies(key []byte) bool {
	if ch.Hash != hashChange(ch) {
		return false
	}
	return hmac.Equal(ch.Sig, signChange(key, ch))
}

func lastVerifiedChange(tx kv.Tx, key []byte) (Change, bool) {
	var ch Change
	var ok bool
	tx.Range(bucketSettingsChanges, kv.Range{Reverse: true}, func(_, v []byte) bool {
		var c Change
		if json.Unmarshal(v, &c) != nil || !c.verifies(key) {
			return true
		}
		ch, ok = c, true
		return false
	})
	return ch, ok
}
