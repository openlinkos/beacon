package secrets

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

// The hygiene the vault keeps by itself: rotation, the health of the passwords it holds, and the
// grants nobody has used. An item may carry a rotation period (rotate_days); as its current value
// nears the period's end the sweep tells the chat and every agent holding a live grant over the
// item — the same notice window the grants' own expiry uses (grant.expiry_notice) and the same
// doors: the chat for people, the hook for agents. Rotation itself never shows a value: the vault
// generates the new one by the spec given and stores it as the next version, and the agent that
// turns the secret works it into the target system through the use action, never through read.

// RotateState names where an item's rotation stands.
const (
	RotateDueSoon = "due_soon" // inside the notice window
	RotateOverdue = "overdue"  // past the period's end
)

// RotationRow is one item with a rotation period, as the console lists them.
type RotationRow struct {
	Ref        string    `json:"ref"`
	Item       string    `json:"item"`
	Collection string    `json:"collection"`
	Title      string    `json:"title"`
	State      string    `json:"state"` // due_soon or overdue
	DueAt      time.Time `json:"due_at"`
	Days       int       `json:"days"` // whole days until due (negative: past it)
}

// rotationState works an item's rotation out from its current version's age. dueAt is when the
// period ends (zero when the item carries no period).
func (s *Service) rotationState(item Item) (dueAt time.Time, verAt time.Time) {
	if item.RotateDays <= 0 {
		return time.Time{}, time.Time{}
	}
	ver, ok := s.Version(item.ID, item.Version)
	if !ok {
		return time.Time{}, time.Time{}
	}
	at := ver.At
	return at.Add(time.Duration(item.RotateDays) * 24 * time.Hour), at
}

// RotationDue lists the items whose rotation period ends inside the notice window or has already
// passed, oldest due first. The notice window is grant.expiry_notice, the same setting the grants'
// own expiry notices take (3 days by default); a period shorter than the window counts as due from
// the start, for the value is always close to its end.
func (s *Service) RotationDue() []RotationRow {
	notice := s.settingDuration("grant.expiry_notice", 72*time.Hour)
	now := s.now()
	var out []RotationRow
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) || r.RotateDays <= 0 || r.State != StateNormal {
				return true
			}
			dueAt, _ := s.rotationState(r.Item)
			if dueAt.IsZero() {
				return true
			}
			state := ""
			switch {
			case now.After(dueAt):
				state = RotateOverdue
			case notice <= 0 || now.After(dueAt.Add(-notice)):
				state = RotateDueSoon
			default:
				return true
			}
			col, _ := s.collectionByIDTx(tx, r.Collection)
			out = append(out, RotationRow{Item: r.ID, Collection: col.Name, Title: r.Title,
				Ref: RefOfItem(col.Name, itemRefTitle(r.Item)), State: state, DueAt: dueAt,
				Days: int(dueAt.Sub(now).Hours() / 24)})
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].DueAt.Before(out[j].DueAt) })
	return out
}

// itemRefTitle is the title when the reference syntax takes it, else the ID.
func itemRefTitle(item Item) string {
	if RefNameOK(item.Title) {
		return item.Title
	}
	return item.ID
}

// rotateNoticedAt reads the rotation notice marker of an item: the version the last notice went
// out for, and when.
func (s *Service) rotateNoticeMarker(item string) (int64, time.Time) {
	var m struct {
		Version int64     `json:"version"`
		At      time.Time `json:"at"`
	}
	if b, ok := s.metaGet("rotatenotice\x00" + item); ok {
		_ = json.Unmarshal(b, &m)
	}
	return m.Version, m.At
}

// putRotateNoticeMarkerTx writes the marker inside a transaction.
func putRotateNoticeMarkerTx(tx kv.Tx, item string, version int64, at time.Time) error {
	b, err := json.Marshal(struct {
		Version int64     `json:"version"`
		At      time.Time `json:"at"`
	}{version, at})
	if err != nil {
		return err
	}
	return tx.Put(bucketMeta, []byte("rotatenotice\x00"+item), b)
}

// rotateDue tells whether the item's rotation is due for its notice: its period ends inside the
// notice window or has passed, and no notice went out for this version yet.
func (s *Service) rotateDue(item Item, notice time.Duration, now time.Time) bool {
	if item.RotateDays <= 0 || item.State != StateNormal {
		return false
	}
	dueAt, _ := s.rotationState(item)
	if dueAt.IsZero() {
		return false
	}
	if now.After(dueAt) {
		// past the end: due whatever the window
	} else if notice <= 0 || !now.After(dueAt.Add(-notice)) {
		return false
	}
	version, _ := s.rotateNoticeMarker(item.ID)
	return version != item.Version
}

// RotateItem writes the item's next value by the spec, for an agent that holds a live use grant
// over the item: the new value is generated on the server, stored as the next version and never
// given out — the answer is the version number, so an agent can turn a secret without ever seeing
// it and then work the new value into the target system through the use action. The reason is
// required, as every use is.
func (s *Service) RotateItem(agentName, via, ref, reason string, spec GenSpec) (Item, error) {
	if s.keys.sealed() {
		return Item{}, ErrSealed
	}
	agent, ok := s.agentInfoOf(agentName)
	if !ok {
		return Item{}, fmt.Errorf("%w: no agent %q", ErrNotFound, agentName)
	}
	if strings.TrimSpace(reason) == "" {
		return Item{}, fmt.Errorf("%w: a rotation says why", ErrBadInput)
	}
	item, col, ok := s.Item(ref)
	if !ok {
		return Item{}, fmt.Errorf("%w: no item %q", ErrNotFound, ref)
	}
	if s.EffectiveLevel(item) == LevelNever {
		return Item{}, fmt.Errorf("%w: this item is not given to agents", ErrForbidden)
	}
	// the entitlement to turn a secret is a live use grant over the item; a read grant is not
	// enough, for the point is that the value never reaches the agent
	if _, ok := s.activeGrant(agentName, item.ID, "", ActUse); !ok {
		if _, read := s.activeGrant(agentName, item.ID, "", ActRead); read {
			return Item{}, fmt.Errorf("%w: turning a secret needs a use grant, so the value never reaches you", ErrNoGrant)
		}
		return Item{}, ErrNoGrant
	}
	if !s.ownerMayRead(agent.Owner, col.ID) {
		return Item{}, fmt.Errorf("%w: the agent's owner may no longer read this collection", ErrForbidden)
	}
	value, err := GenerateValue(spec)
	if err != nil {
		return Item{}, err
	}
	who := Actor{Name: agentName, Via: via}
	out, err := s.setItem(who, ItemIn{ID: item.ID, Collection: col.ID, Reason: reason,
		Fields: []FieldIn{{Name: s.rotationField(item), Type: FieldConcealed, Purpose: PurposePassword, Value: value}}}, true)
	if err != nil {
		return Item{}, err
	}
	// the record says a value was turned, never what it is
	_ = s.auditEntry(store.AuditEntry{Actor: agentName, Via: via, Action: "secrets_item_rotate",
		Target: item.Title, Detail: fmt.Sprintf("the value was turned by the server and stored as version %d; the agent never saw it", out.Version)})
	return out, nil
}

// rotationField is the field a rotation writes: the item's password field when it has one, else
// "password".
func (s *Service) rotationField(item Item) string {
	if ver, ok := s.Version(item.ID, item.Version); ok {
		for _, f := range ver.Fields {
			if f.Purpose == PurposePassword || passwordFieldName(f.Name) {
				return f.Name
			}
		}
	}
	return "password"
}

// ---- unused grants

// grantIdleSince is when a grant last moved: its last use when it has one, else its birth.
func grantIdleSince(g Grant) time.Time {
	if !g.LastUsed.IsZero() {
		return g.LastUsed
	}
	return g.Created
}

// UnusedGrants lists the live grants that have sat idle longer than grant.unused_after allows,
// longest idle first — what the console marks and the sweep acts on.
func (s *Service) UnusedGrants() []Grant {
	after := s.unusedAfter()
	now := s.now()
	var out []Grant
	s.grants(func(g Grant) bool {
		if g.Status != GrantActive {
			return true
		}
		if after > 0 && now.Sub(grantIdleSince(g)) < after {
			return true
		}
		out = append(out, g)
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		return grantIdleSince(out[i]).Before(grantIdleSince(out[j]))
	})
	return out
}

// unusedAfter is the idle a grant may sit for before the sweep acts (grant.unused_after).
func (s *Service) unusedAfter() time.Duration {
	return s.settingDuration("grant.unused_after", 30*24*time.Hour)
}

// onUnused is what happens to an idle grant (grant.on_unused): marked in the console (the
// default), notified, or released.
func (s *Service) onUnused() string {
	return s.settingString("grant.on_unused", "mark")
}

// ---- the sweep

// SweepHygiene is the clock's hygiene work: the items whose rotation period ends inside the notice
// window are announced once per version (the chat for people, the hook for the agents holding a
// live grant, an audit entry), and the grants that have sat idle longer than grant.unused_after
// meet grant.on_unused — marked (the console lists them, nothing happens), notified once, or
// released. It answers how many things it changed, for the log; the wiring runs it beside the
// grants' own sweep.
func (s *Service) SweepHygiene() int {
	if s.keys.sealed() {
		return 0
	}
	changed := 0
	now := s.now()
	notice := s.settingDuration("grant.expiry_notice", 72*time.Hour)

	// rotation notices: once per version of the item
	type toNotice struct {
		item Item
		due  time.Time
	}
	var due []toNotice
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) {
				return true
			}
			if s.rotateDue(r.Item, notice, now) {
				dueAt, _ := s.rotationState(r.Item)
				due = append(due, toNotice{item: r.Item, due: dueAt})
			}
			return true
		})
		return nil
	})
	for _, d := range due {
		s.mu.Lock()
		marked := false
		_ = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			// the marker moves inside the lock, so two sweeps cannot notice one version twice
			if s.rotateDue(d.item, notice, s.now()) {
				if err := putRotateNoticeMarkerTx(tx, d.item.ID, d.item.Version, s.now()); err != nil {
					return err
				}
				marked = true
			}
			return nil
		})
		s.mu.Unlock()
		if !marked {
			continue
		}
		changed++
		col, _ := s.collectionByID(d.item.Collection)
		days := int(d.due.Sub(now).Hours() / 24)
		if days < 0 {
			days = -days
			_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_item_rotate_due",
				Target: d.item.Title, Detail: fmt.Sprintf("the rotation period ended %d day(s) ago, in %s", days, col.Name)})
			if s.d.Bot != nil {
				_ = s.d.Bot.Send(s.ctx, s.t("The rotation period of \"%s\" in %s ended %d day(s) ago.", d.item.Title, col.Name, days))
			}
		} else {
			_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_item_rotate_due",
				Target: d.item.Title, Detail: fmt.Sprintf("the rotation period ends in %d day(s), in %s", days, col.Name)})
			if s.d.Bot != nil {
				_ = s.d.Bot.Send(s.ctx, s.t("The rotation period of \"%s\" in %s ends in %d day(s).", d.item.Title, col.Name, days))
			}
		}
		// the agents holding a live grant are the ones that turn the value; they hear of it too
		notified := map[string]bool{}
		s.grants(func(g Grant) bool {
			if g.Item == d.item.ID && g.Status == GrantActive && !notified[g.Agent] {
				notified[g.Agent] = true
				s.sendSecretHook(g.Agent, "secret.rotate_due", hookPayload{Item: d.item.ID, Grant: g.ID,
					Version: d.item.Version, ExpiresAt: d.due,
					Reason: fmt.Sprintf("the item's rotation period is due; version %d is current", d.item.Version)})
			}
			return true
		})
	}

	// idle grants: what grant.on_unused says
	switch s.onUnused() {
	case "notify":
		after := s.unusedAfter()
		var idle []Grant
		s.grants(func(g Grant) bool {
			if g.Status == GrantActive && g.UnusedNoticedAt.IsZero() && now.Sub(grantIdleSince(g)) >= after {
				idle = append(idle, g)
			}
			return true
		})
		if len(idle) > 0 {
			s.mu.Lock()
			sk, err := s.sk()
			var noticed []Grant
			if err == nil {
				err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
					for _, g := range idle {
						v := tx.Get(bucketGrants, []byte(g.ID))
						var cur Grant
						if v == nil || json.Unmarshal(v, &cur) != nil || cur.Status != GrantActive || !cur.UnusedNoticedAt.IsZero() {
							continue
						}
						cur.UnusedNoticedAt = now
						if err := putGrantTx(tx, cur, sk); err != nil {
							return err
						}
						noticed = append(noticed, cur)
					}
					if len(noticed) == 0 {
						return nil
					}
					// UnusedNoticedAt is in signedParts(); the head has to name the new hash
					// or grants() drops the row as stale.
					return writePolicyHeadTx(tx, sk)
				})
			}
			s.mu.Unlock()
			if err == nil {
				for _, g := range noticed {
					_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_grant_unused",
						Target: g.ID, Detail: fmt.Sprintf("the grant of %s has sat idle since %s", g.Agent, grantIdleSince(g).Format(time.RFC3339))})
					if s.d.Bot != nil {
						_ = s.d.Bot.Send(s.ctx, s.t("The grant of %s over \"%s\" has not been used since %s.", g.Agent, s.itemTitleOf(g.Item), s.durWordsIdle(now.Sub(grantIdleSince(g)))))
					}
					changed++
				}
			}
		}
	case "auto_release":
		after := s.unusedAfter()
		var idle []Grant
		s.grants(func(g Grant) bool {
			if g.Status == GrantActive && now.Sub(grantIdleSince(g)) >= after {
				idle = append(idle, g)
			}
			return true
		})
		if len(idle) > 0 {
			s.mu.Lock()
			sk, err := s.sk()
			var released []Grant
			if err == nil {
				err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
					for _, g := range idle {
						v := tx.Get(bucketGrants, []byte(g.ID))
						var cur Grant
						if v == nil || json.Unmarshal(v, &cur) != nil || cur.Status != GrantActive {
							continue
						}
						if now.Sub(grantIdleSince(cur)) < after {
							continue // used in the meantime
						}
						cur.Status = GrantReleased
						if err := putGrantTx(tx, cur, sk); err != nil {
							return err
						}
						released = append(released, cur)
					}
					if len(released) == 0 {
						return nil
					}
					return writePolicyHeadTx(tx, sk)
				})
			}
			s.mu.Unlock()
			if err != nil {
				released = nil
			}
			for _, g := range released {
				_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_grant_unused",
					Target: g.ID, Detail: fmt.Sprintf("the grant of %s had sat idle since %s and was released (grant.on_unused: auto_release)",
						g.Agent, grantIdleSince(g).Format(time.RFC3339))})
				s.sendSecretHook(g.Agent, "secret.revoked", hookPayload{Grant: g.ID, Item: g.Item,
					Reason: "the grant was released: it had not been used in the time the setting allows"})
				changed++
			}
			if len(released) > 0 && s.d.Bot != nil {
				_ = s.d.Bot.Send(s.ctx, s.t("%d grant(s) were released: they had not been used in the time the setting allows.", len(released)))
			}
		}
	}
	// mark (the default): the console lists them live; the sweep has nothing to do
	return changed
}

// itemTitleOf names an item for the chat, by its title or its ID.
func (s *Service) itemTitleOf(item string) string {
	if row, _, ok := s.Item(item); ok {
		return row.Title
	}
	return item
}

// durWordsIdle writes an idle span for people.
func (s *Service) durWordsIdle(d time.Duration) string {
	if d < time.Hour {
		return s.t("%d minutes", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return s.t("%d hours", int(d.Hours()))
	}
	return s.t("%d days", int(d.Hours()/24))
}
