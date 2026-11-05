package settings

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/openlinkos/beacon/internal/kv"
)

// The signed head of the settings: which rows are current, at which version, with which value.
// A row that is missing, stale or whose signature fails is not treated as absent (that would
// fall back to a looser default); the head's own value is served, and the break is reported.
// Changes are chained and signed the same way, so a revert cannot pick a value someone wrote
// into the history by hand.

var headKey = []byte("\x00head")

// HeadEnt is one live row as the head records it.
type HeadEnt struct {
	Scope   string          `json:"scope"`
	Target  string          `json:"target,omitempty"`
	Key     string          `json:"key"`
	Version int64           `json:"version"`
	Value   json.RawMessage `json:"value"`
}

// Head is the signed snapshot of every live row. Version is the latest change.
type Head struct {
	Version int64     `json:"version"`
	Rows    []HeadEnt `json:"rows"`
	Hash    string    `json:"hash"`
	Sig     []byte    `json:"sig"`
}

func signHead(key []byte, h Head) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(strconv.FormatInt(h.Version, 10)))
	mac.Write([]byte{0})
	for _, e := range h.Rows {
		for _, p := range []string{e.Scope, e.Target, e.Key, strconv.FormatInt(e.Version, 10), string(e.Value)} {
			mac.Write([]byte(p))
			mac.Write([]byte{0})
		}
	}
	return mac.Sum(nil)
}

func hashHead(h Head) string {
	mac := sha256.New()
	mac.Write([]byte(strconv.FormatInt(h.Version, 10)))
	for _, e := range h.Rows {
		mac.Write([]byte(e.Scope))
		mac.Write([]byte(e.Target))
		mac.Write([]byte(e.Key))
		mac.Write([]byte(strconv.FormatInt(e.Version, 10)))
		mac.Write(e.Value)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func (h Head) verifies(key []byte) bool {
	if h.Hash != hashHead(h) {
		return false
	}
	return hmac.Equal(h.Sig, signHead(key, h))
}

func writeHead(tx kv.Tx, key []byte, latest int64, drop ...string) error {
	prev, _ := loadHead(tx, key)
	return writeHeadFrom(tx, key, latest, prev, drop...)
}

func writeHeadFrom(tx kv.Tx, key []byte, latest int64, prev Head, drop ...string) error {
	dropped := map[string]bool{}
	for _, d := range drop {
		dropped[d] = true
	}
	held := map[string]HeadEnt{}
	for _, e := range prev.Rows {
		k := rowKey(e.Scope, e.Target, e.Key)
		if dropped[k] {
			continue
		}
		held[k] = e
	}
	tx.Range(bucketSettings, kv.Range{}, func(k, v []byte) bool {
		if string(k) == string(headKey) {
			return true
		}
		var r Row
		if json.Unmarshal(v, &r) != nil {
			return true
		}
		if !hmac.Equal(r.Sig, sign(key, r)) {
			return true // keep the previous head's value for this row, if any
		}
		rk := rowKey(r.Scope, r.Target, r.Key)
		if dropped[rk] {
			return true
		}
		held[rk] = HeadEnt{Scope: r.Scope, Target: r.Target, Key: r.Key, Version: r.Version, Value: r.Value}
		return true
	})
	var ents []HeadEnt
	for _, e := range held {
		ents = append(ents, e)
	}
	sort.Slice(ents, func(i, j int) bool {
		if ents[i].Scope != ents[j].Scope {
			return ents[i].Scope < ents[j].Scope
		}
		if ents[i].Target != ents[j].Target {
			return ents[i].Target < ents[j].Target
		}
		return ents[i].Key < ents[j].Key
	})
	h := Head{Version: latest, Rows: ents}
	h.Hash = hashHead(h)
	h.Sig = signHead(key, h)
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return tx.Put(bucketSettings, headKey, b)
}

func loadHead(tx kv.Tx, key []byte) (Head, bool) {
	v := tx.Get(bucketSettings, headKey)
	if v == nil {
		return Head{}, false
	}
	var h Head
	if json.Unmarshal(v, &h) != nil || !h.verifies(key) {
		return Head{}, false
	}
	return h, true
}

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

func lastChange(tx kv.Tx) (Change, bool) {
	var ch Change
	var ok bool
	tx.Range(bucketSettingsChanges, kv.Range{Reverse: true}, func(_, v []byte) bool {
		if json.Unmarshal(v, &ch) != nil {
			return true
		}
		ok = true
		return false
	})
	return ch, ok
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

func anySignedChange(tx kv.Tx) bool {
	found := false
	tx.Range(bucketSettingsChanges, kv.Range{}, func(_, v []byte) bool {
		var ch Change
		if json.Unmarshal(v, &ch) == nil && len(ch.Sig) > 0 {
			found = true
			return false
		}
		return true
	})
	return found
}
