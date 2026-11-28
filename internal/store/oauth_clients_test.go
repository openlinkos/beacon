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

// TestRegisterClientKeepsSignInsUnderWay checks that a full table makes room without forgetting a
// registration made within ClientFresh, or one whose consent page a person opened within it: their
// sign-ins may be under way. A table of only those refuses the new registration.
func TestRegisterClientKeepsSignInsUnderWay(t *testing.T) {
	s, now := openTest(t)
	put := func(c OAuthClient) {
		t.Helper()
		err := s.db.Update(func(tx kv.Tx) error {
			v, _ := json.Marshal(c)
			return tx.Put(bucketClients, []byte(c.ID), v)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// the oldest is being answered on its consent page; the newest half are young
	put(OAuthClient{ID: "lbc_asked", Name: "asked", RedirectURIs: []string{"http://127.0.0.1/cb"}, Created: now.Add(-2 * time.Hour)})
	for i := 1; i < MaxClients; i++ {
		created := now.Add(-time.Hour + time.Duration(i)*time.Second)
		if i >= MaxClients/2 {
			created = now.Add(-ClientFresh + time.Minute + time.Duration(i)*time.Millisecond)
		}
		put(OAuthClient{ID: fmt.Sprintf("lbc_fill%04d", i), Name: "filler", RedirectURIs: []string{"http://127.0.0.1/cb"}, Created: created})
	}
	s.AskClient("lbc_asked")
	if c, ok := s.OAuthClient("lbc_asked"); !ok || !c.Asked.Equal(*now) {
		t.Fatalf("the consent page is not recorded: %+v %v", c, ok)
	}

	if _, err := s.RegisterClient(OAuthClient{Name: "new", RedirectURIs: []string{"http://localhost:5555/callback"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.OAuthClient("lbc_asked"); !ok {
		t.Error("a registration whose consent page is open was forgotten")
	}
	if _, ok := s.OAuthClient("lbc_fill0001"); ok {
		t.Error("the oldest registration that may go is still there")
	}
	if _, ok := s.OAuthClient("lbc_fill0002"); !ok {
		t.Error("more than one registration was forgotten")
	}

	// only young and asked-for registrations are left that have not signed in: the new one waits
	_ = s.db.Update(func(tx kv.Tx) error {
		for i := 2; i < MaxClients/2; i++ {
			c := OAuthClient{ID: fmt.Sprintf("lbc_fill%04d", i), Name: "filler", RedirectURIs: []string{"http://127.0.0.1/cb"}, Created: *now, Used: *now}
			v, _ := json.Marshal(c)
			if err := tx.Put(bucketClients, []byte(c.ID), v); err != nil {
				return err
			}
		}
		return nil
	})
	if _, err := s.RegisterClient(OAuthClient{Name: "late", RedirectURIs: []string{"http://localhost:5555/callback"}}); !errors.Is(err, ErrTooMany) {
		t.Errorf("a registration into a table of sign-ins under way: %v", err)
	}
	// once they are no longer fresh, the oldest makes room again
	*now = now.Add(ClientFresh)
	if _, err := s.RegisterClient(OAuthClient{Name: "later", RedirectURIs: []string{"http://localhost:5555/callback"}}); err != nil {
		t.Errorf("a registration after the sign-ins went stale: %v", err)
	}
	if _, ok := s.OAuthClient("lbc_asked"); ok {
		t.Error("a stale registration outlived the room made for a new one")
	}
}
