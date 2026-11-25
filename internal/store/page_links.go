package store

import (
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"regexp"
	"strconv"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
)

var (
	bucketPageLinkGenerations = []byte("page_link_generations")
	pageLinkID                = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// ErrPageLinkResource means the supplied kind or identifier cannot name a revocable page.
var ErrPageLinkResource = errors.New("invalid signed page resource")

func pageLinkKey(kind, id string) ([]byte, error) {
	if (kind != "alert" && kind != "session") || !pageLinkID.MatchString(id) {
		return nil, ErrPageLinkResource
	}
	return []byte(kind + "/" + id), nil
}

func pageLinkGeneration(tx kv.Tx, key []byte) (uint64, error) {
	raw := tx.Get(bucketPageLinkGenerations, key)
	if raw == nil {
		return 0, nil
	}
	if len(raw) != 8 {
		return 0, errors.New("invalid signed page generation")
	}
	generation := binary.BigEndian.Uint64(raw)
	if generation == 0 {
		return 0, errors.New("invalid signed page generation")
	}
	return generation, nil
}

// PageLinkGeneration returns the current generation of an alert group or session. A resource whose
// links have never been revoked has generation zero.
func (s *Store) PageLinkGeneration(kind, id string) (uint64, error) {
	key, err := pageLinkKey(kind, id)
	if err != nil {
		return 0, err
	}
	var generation uint64
	err = s.db.View(func(tx kv.Tx) error {
		var err error
		generation, err = pageLinkGeneration(tx, key)
		return err
	})
	if err != nil {
		return 0, err
	}
	return generation, nil
}

// RevokePageLinks advances the generation and records the audit event in the same transaction.
// Generations are kept even after a resource is pruned, so an old link cannot become valid again
// if the same resource identifier is used later.
func (s *Store) RevokePageLinks(kind, id string, actor AuditEntry) (uint64, error) {
	key, err := pageLinkKey(kind, id)
	if err != nil {
		return 0, err
	}
	var generation uint64
	err = s.db.UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		// The bucket sequence serialises concurrent advances, including first use of an ID.
		// Each resource retains its own generation in the bucket's records.
		if _, err := tx.NextSequence(bucketPageLinkGenerations); err != nil {
			return err
		}
		current, err := pageLinkGeneration(tx, key)
		if err != nil {
			return err
		}
		if current == math.MaxUint64 {
			return errors.New("signed page generation exhausted")
		}
		generation = current + 1
		var value [8]byte
		binary.BigEndian.PutUint64(value[:], generation)
		if err := tx.Put(bucketPageLinkGenerations, key, value[:]); err != nil {
			return err
		}
		return s.AppendAuditTx(raw, events.Event{
			Time: actor.Time, Actor: actor.Actor, Via: actor.Via, Action: "revoke_page_links", Object: string(key),
			Detail: "generation " + strconv.FormatUint(generation, 10),
		})
	})
	if err != nil {
		return 0, err
	}
	return generation, nil
}
