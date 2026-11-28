package store

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// TestStandbyRechecksCachedTokens checks that, while the store is told to recheck (a standby of a
// pair), a token from the cache is accepted only as the database holds it now: one revoked or changed
// by the other instance stops or changes at once.
func TestStandbyRechecksCachedTokens(t *testing.T) {
	s, _ := open(t)
	revoked, _, err := s.CreateToken("revoked", []string{"status:read"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	changed, _, err := s.CreateToken("changed", []string{"status:read", "alerts:write"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	// the other instance revokes one and narrows the other; this one's cache knows nothing of it
	err = s.db.Update(func(tx kv.Tx) error {
		if err := tx.Delete(bucketTokens, []byte("revoked")); err != nil {
			return err
		}
		var t Token
		if err := json.Unmarshal(tx.Get(bucketTokens, []byte("changed")), &t); err != nil {
			return err
		}
		t.Scopes = []string{"status:read"}
		b, _ := json.Marshal(t)
		return tx.Put(bucketTokens, []byte("changed"), b)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(revoked); !ok {
		t.Fatal("without a recheck the cache answers")
	}

	standby := true
	s.RecheckTokens(func() bool { return standby })
	if _, ok := s.VerifyToken(revoked); ok {
		t.Error("a standby accepted a token the database no longer holds")
	}
	if _, ok := s.Authenticate(revoked); ok {
		t.Error("a standby authenticated a token the database no longer holds")
	}
	if tok, ok := s.VerifyToken(changed); !ok || !slices.Equal(tok.Scopes, []string{"status:read"}) {
		t.Errorf("a standby used the cache's scopes: %+v %v", tok, ok)
	}
	if p, ok := s.Authenticate(changed); !ok || p.Has("alerts:write") {
		t.Errorf("a standby's principal: %+v %v", p, ok)
	}
	if s.Issued(revoked) {
		t.Error("a revoked token counts as issued")
	}

	// a renewal on the other instance: the new secret is not in this cache yet, the old one still
	// works in its grace period and not after
	renewed, _, err := s.CreateToken("renewed", []string{"status:read"}, time.Hour, "test")
	if err != nil {
		t.Fatal(err)
	}
	err = s.db.Update(func(tx kv.Tx) error {
		var t Token
		if err := json.Unmarshal(tx.Get(bucketTokens, []byte("renewed")), &t); err != nil {
			return err
		}
		t.OldHash, t.OldUntil, t.Hash = t.Hash, time.Now().Add(-time.Second), hashToken("lbt_somethingelse")
		b, _ := json.Marshal(t)
		return tx.Put(bucketTokens, []byte("renewed"), b)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(renewed); ok {
		t.Error("a standby accepted a replaced secret past its grace period")
	}

	// the leader's cache is the database's: no recheck
	standby = false
	if _, ok := s.VerifyToken(revoked); !ok {
		t.Error("the recheck ran while it was off")
	}
}
