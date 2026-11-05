package secrets

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

func entitlementHeadValue(e Entitlement) string {
	if e.Deny {
		return "deny"
	}
	return "allow"
}

// The signed head of the vault's policy: which collections, tag-policy rows, people-permission
// rows, entitlements, grants and agent-tag rows are current, at which contents. A row the head
// names that is missing, stale or whose signature fails is treated as the strictest (a collection
// or tag as never, a permission as absent, an allowing entitlement or grant as absent, a deny
// entitlement as denying everything) and reported. A tag the head does not name is an ordinary
// label, even if a leftover row sits in the file — imported names that were never registered as
// policy stay labels.
//
// Changing the database file alone cannot widen access (the design's section 8): deleting a
// stricter tag row, or putting an older looser one back, is seen against the head.

var policyHeadKey = []byte("\x00head")

const (
	kindCollection  = "collection"
	kindTag         = "tag"
	kindPerm        = "perm"
	kindItem        = "item"
	kindEntitlement = "entitlement"
	kindGrant       = "grant"
	kindAgentTags   = "agent-tags"
)

// policyHeadEnt is one live policy row as the head records it.
type policyHeadEnt struct {
	Kind  string `json:"kind"`  // collection, tag or perm
	Key   string `json:"key"`   // collection ID, tag name, or collection\0subject
	Hash  string `json:"hash"`  // hex SHA-256 of the row's signed parts
	Value string `json:"value"` // collection/tag level, or the permission
}

// policyHead is the signed snapshot of every live policy row.
type policyHead struct {
	Version int64           `json:"version"`
	Rows    []policyHeadEnt `json:"rows"`
	Hash    string          `json:"hash"`
	Sig     []byte          `json:"sig"`
}

func policyRowKey(kind, key string) string { return kind + "\x00" + key }

func partsHash(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func signPolicyHead(sk []byte, h policyHead) []byte {
	mac := hmac.New(sha256.New, sk)
	mac.Write([]byte(strconv.FormatInt(h.Version, 10)))
	mac.Write([]byte{0})
	for _, e := range h.Rows {
		for _, p := range []string{e.Kind, e.Key, e.Hash, e.Value} {
			mac.Write([]byte(p))
			mac.Write([]byte{0})
		}
	}
	return mac.Sum(nil)
}

func hashPolicyHead(h policyHead) string {
	mac := sha256.New()
	mac.Write([]byte(strconv.FormatInt(h.Version, 10)))
	for _, e := range h.Rows {
		mac.Write([]byte(e.Kind))
		mac.Write([]byte(e.Key))
		mac.Write([]byte(e.Hash))
		mac.Write([]byte(e.Value))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func (h policyHead) verifies(sk []byte) bool {
	if h.Hash != hashPolicyHead(h) {
		return false
	}
	return hmac.Equal(h.Sig, signPolicyHead(sk, h))
}

func (h policyHead) lookup(kind, key string) (policyHeadEnt, bool) {
	want := policyRowKey(kind, key)
	for _, e := range h.Rows {
		if policyRowKey(e.Kind, e.Key) == want {
			return e, true
		}
	}
	return policyHeadEnt{}, false
}

func loadPolicyHead(tx kv.Tx, sk []byte) (policyHead, bool) {
	v := tx.Get(bucketMeta, policyHeadKey)
	if v == nil {
		return policyHead{}, false
	}
	var h policyHead
	if json.Unmarshal(v, &h) != nil || !h.verifies(sk) {
		return policyHead{}, false
	}
	return h, true
}

func policyHeadPresent(tx kv.Tx) bool {
	return tx.Get(bucketMeta, policyHeadKey) != nil
}

func anyPolicyTx(tx kv.Tx) bool {
	found := false
	check := func(bucket []byte) {
		if found {
			return
		}
		tx.Range(bucket, kv.Range{}, func(k, _ []byte) bool {
			if string(k) == string(policyHeadKey) {
				return true
			}
			found = true
			return false
		})
	}
	check(bucketCollections)
	check(bucketTags)
	check(bucketPerms)
	check(bucketEntitlements)
	check(bucketGrants)
	check(bucketAgentTags)
	return found
}

// writePolicyHeadTx rebuilds the signed head from the live, verifying policy rows, dropping the
// named keys (kind\0key) so a delete is recorded in the same transaction as the row's removal.
func writePolicyHeadTx(tx kv.Tx, sk []byte, drop ...string) error {
	dropped := map[string]bool{}
	for _, d := range drop {
		dropped[d] = true
	}
	held := map[string]policyHeadEnt{}
	add := func(kind, key, hash, value string) {
		rk := policyRowKey(kind, key)
		if dropped[rk] {
			return
		}
		held[rk] = policyHeadEnt{Kind: kind, Key: key, Hash: hash, Value: value}
	}
	tx.Range(bucketCollections, kv.Range{}, func(k, v []byte) bool {
		var r signedCollection
		if json.Unmarshal(v, &r) != nil || string(k) != r.ID {
			return true
		}
		if !verifyPolicy(sk, r.Sig, r.signedParts()...) {
			return true
		}
		add(kindCollection, r.ID, partsHash(r.signedParts()), r.Level)
		return true
	})
	tx.Range(bucketTags, kv.Range{}, func(k, v []byte) bool {
		var r signedTag
		if json.Unmarshal(v, &r) != nil || string(k) != r.Name {
			return true
		}
		if !verifyPolicy(sk, r.Sig, r.signedParts()...) {
			return true
		}
		add(kindTag, r.Name, partsHash(r.signedParts()), r.Level)
		return true
	})
	tx.Range(bucketPerms, kv.Range{}, func(k, v []byte) bool {
		var p Permission
		if json.Unmarshal(v, &p) != nil {
			return true
		}
		if string(k) != string(permRowKey(p.Collection, p.Subject)) {
			return true
		}
		if !verifyPolicy(sk, p.Sig, p.signedParts()...) {
			return true
		}
		add(kindPerm, p.Collection+"\x00"+p.Subject, partsHash(p.signedParts()), p.Perm)
		return true
	})
	tx.Range(bucketEntitlements, kv.Range{}, func(k, v []byte) bool {
		var e Entitlement
		if json.Unmarshal(v, &e) != nil || string(k) != e.Name {
			return true
		}
		if !verifyRow(sk, e.Sig, e.signedParts()...) {
			return true
		}
		add(kindEntitlement, e.Name, partsHash(e.signedParts()), entitlementHeadValue(e))
		return true
	})
	tx.Range(bucketGrants, kv.Range{}, func(k, v []byte) bool {
		var g Grant
		if json.Unmarshal(v, &g) != nil || string(k) != g.ID {
			return true
		}
		if !verifyRow(sk, g.Sig, g.signedParts()...) {
			return true
		}
		add(kindGrant, g.ID, partsHash(g.signedParts()), g.Status)
		return true
	})
	tx.Range(bucketAgentTags, kv.Range{}, func(k, v []byte) bool {
		var t AgentTags
		if json.Unmarshal(v, &t) != nil || string(k) != t.Agent {
			return true
		}
		if !verifyRow(sk, t.Sig, t.signedParts()...) {
			return true
		}
		add(kindAgentTags, t.Agent, partsHash(t.signedParts()), strings.Join(t.Tags, "\x01"))
		return true
	})
	var ents []policyHeadEnt
	for _, e := range held {
		ents = append(ents, e)
	}
	sort.Slice(ents, func(i, j int) bool {
		if ents[i].Kind != ents[j].Kind {
			return ents[i].Kind < ents[j].Kind
		}
		return ents[i].Key < ents[j].Key
	})
	prev, _ := loadPolicyHead(tx, sk)
	h := policyHead{Version: prev.Version + 1, Rows: ents}
	h.Hash = hashPolicyHead(h)
	h.Sig = signPolicyHead(sk, h)
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return tx.Put(bucketMeta, policyHeadKey, b)
}

// headMatch reports whether the live row is the one the head names. named is whether the head
// lists this key. When the head is missing and there are no policy rows yet, the live row is
// allowed (the first write will record it). A present head that does not verify, or a missing
// head beside other policy rows, is fail-closed: named is true and ok is false, so the caller
// treats the row as the strictest.
func (s *Service) headMatch(tx kv.Tx, kind, key string, parts []string, value string) (ok, named bool) {
	sk, err := s.sk()
	if err != nil {
		return false, true
	}
	h, headOK := loadPolicyHead(tx, sk)
	if !headOK {
		if policyHeadPresent(tx) || anyPolicyTx(tx) {
			s.reportHeadBreak(kind, key)
			return false, true
		}
		return true, false
	}
	ent, named := h.lookup(kind, key)
	if !named {
		return false, false
	}
	if ent.Hash != partsHash(parts) || ent.Value != value {
		s.reportHeadBreak(kind, key)
		return false, true
	}
	return true, true
}

func (s *Service) reportHeadBreak(kind, key string) {
	label := kind + " " + key
	s.badMu.Lock()
	already := false
	for _, b := range s.bad {
		if b == label {
			already = true
			break
		}
	}
	if !already {
		s.bad = append(s.bad, label)
	}
	s.badMu.Unlock()
	if already {
		return
	}
	s.d.Log.Error("a policy row is missing, stale or bad and is treated as the strictest", "kind", kind, "row", key)
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_policy_head",
		Detail: "a " + kind + " row is missing, stale or bad and is treated as the strictest"})
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("Vault policy rows are missing, stale or bad and are treated as the strictest: %s.", label))
	}
}

// ensurePolicyHead writes a head from the live rows when none is stored yet (the upgrade from
// unsigned heads, and a fresh vault). A stored head that does not verify is left so reads fail
// closed rather than being rewritten from whatever remains in the file.
func (s *Service) ensurePolicyHead() {
	sk, err := s.sk()
	if err != nil {
		return
	}
	_ = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		if policyHeadPresent(tx) {
			return nil
		}
		return writePolicyHeadTx(tx, sk)
	})
}
