package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
)

// TestRegisterClientMakesRoomWhenFull: a full table of registrations takes a new one by forgetting the
// oldest registration never used to sign in. Only registrations that have all signed in fill it.
func TestRegisterClientMakesRoomWhenFull(t *testing.T) {
	s, now := openTest(t)
	fill := func(used bool) {
		t.Helper()
		err := s.db.Update(func(tx kv.Tx) error {
			for i := range MaxClients {
				c := OAuthClient{ID: fmt.Sprintf("lbc_fill%04d", i), Name: "filler", RedirectURIs: []string{"http://127.0.0.1/cb"},
					Created: now.Add(-time.Hour + time.Duration(i)*time.Second)}
				if used {
					c.Used = c.Created
				}
				v, _ := json.Marshal(c)
				if err := tx.Put(bucketClients, []byte(c.ID), v); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		n := 0
		_ = s.db.View(func(tx kv.Tx) error { n = tx.Count(bucketClients); return nil })
		return n
	}

	fill(false)
	c, err := s.RegisterClient(OAuthClient{Name: "Claude Code", RedirectURIs: []string{"http://localhost:5555/callback"}})
	if err != nil {
		t.Fatalf("a registration into a full table of unused ones: %v", err)
	}
	if _, ok := s.OAuthClient(c.ID); !ok {
		t.Error("the new registration is not kept")
	}
	if _, ok := s.OAuthClient("lbc_fill0000"); ok {
		t.Error("the oldest unused registration is still there")
	}
	if _, ok := s.OAuthClient("lbc_fill0001"); !ok {
		t.Error("more than the oldest was forgotten")
	}
	if n := count(); n != MaxClients {
		t.Errorf("%d registrations kept, want %d", n, MaxClients)
	}

	// every registration has signed in: nothing to forget, so a new one waits
	_ = s.db.Update(func(tx kv.Tx) error {
		var keys [][]byte
		tx.Range(bucketClients, kv.Range{}, func(k, _ []byte) bool { keys = append(keys, append([]byte(nil), k...)); return true })
		for _, k := range keys {
			_ = tx.Delete(bucketClients, k)
		}
		return nil
	})
	fill(true)
	if _, err := s.RegisterClient(OAuthClient{Name: "late", RedirectURIs: []string{"http://localhost:5555/callback"}}); !errors.Is(err, ErrTooMany) {
		t.Errorf("a registration into a table of used ones: %v", err)
	}
	if _, ok := s.OAuthClient("lbc_fill0000"); !ok {
		t.Error("a used registration was forgotten")
	}
}
