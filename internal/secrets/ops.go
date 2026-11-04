package secrets

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

// ---- reading

// Item finds an item by its ID, by "collection/item" or by its title inside a collection, always
// through the signature check.
func (s *Service) Item(ref string) (Item, Collection, bool) {
	collectionRef, itemRef, hasCol := strings.Cut(ref, "/")
	var col Collection
	var ok bool
	if hasCol {
		col, ok = s.Collection(collectionRef)
		if !ok {
			return Item{}, Collection{}, false
		}
	}
	var out Item
	found := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) {
				return true
			}
			match := r.ID == ref || r.Title == ref
			if hasCol {
				match = r.Collection == col.ID && (r.ID == itemRef || r.Title == itemRef)
			}
			if match {
				out, found = r.Item, true
				return false
			}
			return true
		})
		return nil
	})
	if !found {
		return Item{}, Collection{}, false
	}
	if !hasCol {
		col, _ = s.collectionByID(out.Collection)
	}
	return out, col, true
}

// collectionByID finds a collection by ID (the row keys are the IDs).
func (s *Service) collectionByID(id string) (Collection, bool) {
	var out Collection
	ok := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		out, ok = s.collectionByIDTx(tx, id)
		return nil
	})
	return out, ok
}

// Items lists a collection's items (normal state unless state names another), by title.
func (s *Service) Items(collection, state string) []Item {
	col, ok := s.Collection(collection)
	if !ok {
		return nil
	}
	if state == "" {
		state = StateNormal
	}
	var out []Item
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) == nil && s.itemVerifies(r) && r.Collection == col.ID && r.State == state {
				out = append(out, r.Item)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// Version is one version's row of an item (the fields list and who made it, not the values).
func (s *Service) Version(item string, version int64) (ItemVersion, bool) {
	var ver ItemVersion
	ok := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		ver, ok = getVersionTx(tx, item, version)
		return nil
	})
	return ver, ok
}

// Versions lists an item's versions, newest first, as the console and the CLI show them.
func (s *Service) Versions(item string) []ItemVersion {
	var out []ItemVersion
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketVersions, kv.Range{Prefix: []byte(item + "\x00"), Reverse: true}, func(_, v []byte) bool {
			var ver ItemVersion
			if json.Unmarshal(v, &ver) == nil {
				out = append(out, ver)
			}
			return true
		})
		return nil
	})
	return out
}

// Attachment reads one attachment of a version, decrypted under the key version the version's own
// row names.
func (s *Service) Attachment(item string, version int64, name string) ([]byte, int64, error) {
	itemRow, col, ok := s.Item(item)
	if !ok {
		return nil, 0, fmt.Errorf("%w: no item %q", ErrNotFound, item)
	}
	var out []byte
	var size int64
	err := s.d.Store.Storage().View(func(tx kv.Tx) error {
		ver, ok := getVersionTx(tx, itemRow.ID, version)
		if !ok {
			return fmt.Errorf("%w: no version %d", ErrNotFound, version)
		}
		v := tx.Get(bucketFiles, attachmentRowKey(itemRow.ID, version, name))
		if v == nil {
			return fmt.Errorf("%w: no attachment %q", ErrNotFound, name)
		}
		var att Attachment
		if json.Unmarshal(v, &att) != nil {
			return fmt.Errorf("%w: the attachment's row is unreadable", ErrNotFound)
		}
		ck, err := s.collectionKeyByVersionTx(tx, col.ID, ver.KeyVersion)
		if err != nil {
			return err
		}
		defer clear(ck)
		plain, err := unwrap(ck, att.CT, attachmentAAD(itemRow.ID, version, col.ID, ver.KeyVersion, name))
		if err != nil {
			return err
		}
		out, size = plain, att.Size
		return nil
	})
	return out, size, err
}

// ---- state changes

// ArchiveItem moves an item out of the search (and the normal list) into the archived state.
func (s *Service) ArchiveItem(who Actor, item string) (Item, error) {
	return s.changeState(who, item, StateArchived, "archived the item", "secrets_item_archive")
}

// TrashItem removes an item into the trash, where it stays for the retention and can be restored.
func (s *Service) TrashItem(who Actor, item string) (Item, error) {
	return s.changeState(who, item, StateTrash, "removed the item to the trash", "secrets_item_delete")
}

// RestoreItem brings an item back from the archive or the trash.
func (s *Service) RestoreItem(who Actor, item string) (Item, error) {
	return s.changeState(who, item, StateNormal, "restored the item", "secrets_item_restore")
}

func (s *Service) changeState(who Actor, item, state, verb, action string) (Item, error) {
	if s.keys.sealed() {
		return Item{}, ErrSealed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, err := s.sk()
	if err != nil {
		return Item{}, err
	}
	var out Item
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		row, col, ok := s.itemTx(tx, item)
		if !ok {
			return fmt.Errorf("%w: no item %q", ErrNotFound, item)
		}
		if !s.mayTx(tx, who, col.ID, PermEdit) {
			return fmt.Errorf("%w: you may not change items in this collection", ErrForbidden)
		}
		row.State = state
		if state == StateTrash {
			row.TrashedAt = s.now()
		} else {
			row.TrashedAt = time.Time{}
		}
		if err := putItemTx(tx, row, sk); err != nil {
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	s.echo(who, verb, out.Title+" in "+s.collectionName(out.Collection), action)
	return out, nil
}

func putItemTx(tx kv.Tx, item Item, sk []byte) error {
	r := signedItem{Item: item}
	r.Sig = signRow(sk, item.signedParts()...)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return tx.Put(bucketItems, []byte(item.ID), b)
}

// itemTx finds an item inside a transaction, verified.
func (s *Service) itemTx(tx kv.Tx, ref string) (Item, Collection, bool) {
	collectionRef, itemRef, hasCol := strings.Cut(ref, "/")
	var col Collection
	var colOK bool
	if hasCol {
		col, colOK = s.Collection(collectionRef)
		if !colOK {
			return Item{}, Collection{}, false
		}
	}
	var out Item
	found := false
	tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
		var r signedItem
		if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) {
			return true
		}
		match := r.ID == ref || r.Title == ref
		if hasCol {
			match = r.Collection == col.ID && (r.ID == itemRef || r.Title == itemRef)
		}
		if match {
			out, found = r.Item, true
			return false
		}
		return true
	})
	if !found {
		return Item{}, Collection{}, false
	}
	if !hasCol {
		col, _ = s.collectionByIDTx(tx, out.Collection)
	}
	return out, col, true
}

func (s *Service) collectionName(id string) string {
	if col, ok := s.collectionByID(id); ok {
		return col.Name
	}
	return id
}

// PurgeTrash drops the items that have sat in the trash longer than the retention, with their
// versions and attachments. It returns how many went; the wiring runs it from time to time.
func (s *Service) PurgeTrash() int {
	if s.keys.sealed() {
		return 0
	}
	cutoff := s.now().Add(-s.trashKeep())
	var gone []string
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) == nil && r.State == StateTrash && !r.TrashedAt.IsZero() && r.TrashedAt.Before(cutoff) {
				gone = append(gone, r.ID)
			}
			return true
		})
		return nil
	})
	if len(gone) == 0 {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		for _, id := range gone {
			_ = tx.Delete(bucketItems, []byte(id))
			var keys [][]byte
			tx.Range(bucketVersions, kv.Range{Prefix: []byte(id + "\x00")}, func(k, _ []byte) bool {
				keys = append(keys, append([]byte(nil), k...))
				return true
			})
			for _, k := range keys {
				_ = tx.Delete(bucketVersions, k)
			}
			var fkeys [][]byte
			tx.Range(bucketFiles, kv.Range{Prefix: []byte(id + "\x00")}, func(k, _ []byte) bool {
				fkeys = append(fkeys, append([]byte(nil), k...))
				return true
			})
			for _, k := range fkeys {
				_ = tx.Delete(bucketFiles, k)
			}
		}
		return nil
	})
	_ = s.auditEntry(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_trash_purged",
		Detail: fmt.Sprintf("%d item(s) passed the trash retention and were dropped", len(gone))})
	return len(gone)
}

// ---- rollback

// RollbackItem makes the item's version given hold its next version: the details of that version
// (and its attachments) are copied into a new version, so nothing is lost and the history shows
// what happened.
func (s *Service) RollbackItem(who Actor, item string, version int64, reason string) (Item, error) {
	if s.keys.sealed() {
		return Item{}, ErrSealed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sk, err := s.sk()
	if err != nil {
		return Item{}, err
	}
	var out Item
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		row, col, ok := s.itemTx(tx, item)
		if !ok {
			return fmt.Errorf("%w: no item %q", ErrNotFound, item)
		}
		if !s.mayTx(tx, who, col.ID, PermEdit) {
			return fmt.Errorf("%w: you may not change items in this collection", ErrForbidden)
		}
		if s.rotatingNow(col.ID) {
			return fmt.Errorf("%w: the collection's key is being rotated; try again shortly", ErrConflict)
		}
		old, ok := getVersionTx(tx, row.ID, version)
		if !ok {
			return fmt.Errorf("%w: no version %d", ErrNotFound, version)
		}
		if old.Version == row.Version {
			return fmt.Errorf("%w: version %d is the current one", ErrBadInput, version)
		}
		d, err := s.decryptDetailsTx(tx, col, old)
		if err != nil {
			return fmt.Errorf("the version could not be opened: %w", err)
		}
		ckOld, err := s.collectionKeyByVersionTx(tx, col.ID, old.KeyVersion)
		if err != nil {
			return err
		}
		defer clear(ckOld)
		ckNew, err := s.collectionKeyByVersionTx(tx, col.ID, col.KeyVersion)
		if err != nil {
			return err
		}
		defer clear(ckNew)
		next := row.Version + 1
		plain, err := json.Marshal(d)
		if err != nil {
			return err
		}
		ct, err := wrap(ckNew, plain, itemAAD(row.ID, next, col.ID, col.KeyVersion))
		if err != nil {
			return err
		}
		ver := ItemVersion{Item: row.ID, Version: next, Fields: old.Fields, KeyVersion: col.KeyVersion,
			Actor: who.Name, Via: who.Via, At: s.now(), Reason: reason, CT: ct}
		if err := putVersionTx(tx, ver); err != nil {
			return err
		}
		// the attachments of the old version come along, re-bound to the new version
		fprefix := []byte(fmt.Sprintf("%s\x00%016x\x00", row.ID, old.Version))
		tx.Range(bucketFiles, kv.Range{Prefix: fprefix}, func(_, v []byte) bool {
			var att Attachment
			if json.Unmarshal(v, &att) != nil {
				return true
			}
			plain, err := unwrap(ckOld, att.CT, attachmentAAD(row.ID, old.Version, col.ID, old.KeyVersion, att.Name))
			if err != nil {
				return true
			}
			ct, err := wrap(ckNew, plain, attachmentAAD(row.ID, next, col.ID, col.KeyVersion, att.Name))
			if err != nil {
				return true
			}
			att2 := Attachment{Item: row.ID, Version: next, Name: att.Name, Size: att.Size, CT: ct}
			b, err := json.Marshal(att2)
			if err != nil {
				return true
			}
			_ = tx.Put(bucketFiles, attachmentRowKey(row.ID, next, att.Name), b)
			return true
		})
		row.Version = next
		if err := putItemTx(tx, row, sk); err != nil {
			return err
		}
		out = row
		s.pruneVersions(tx, row.ID, next)
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	s.echo(who, "rolled the item back to version "+i64(version)+" as version "+i64(out.Version),
		out.Title+" in "+s.collectionName(out.Collection), "secrets_item_rollback")
	return out, nil
}

// ---- generate

// GenSpec is how Generate makes a new value: its length and its character set (letters, letters
// and digits, or letters, digits and symbols).
type GenSpec struct {
	Length  int
	Charset string // letters (default), alnum, all
}

// Generate makes a new value for a field by the spec, stores it as the item's next version and
// returns the item with that version number. The value itself is not returned: an agent may turn a
// secret without ever seeing it, which is the point (the use actions put it where it belongs).
func (s *Service) Generate(who Actor, item, section, field, reason string, spec GenSpec) (Item, error) {
	value, err := GenerateValue(spec)
	if err != nil {
		return Item{}, err
	}
	row, _, ok := s.Item(item)
	if !ok {
		return Item{}, fmt.Errorf("%w: no item %q", ErrNotFound, item)
	}
	in := ItemIn{ID: row.ID, Collection: row.Collection, Reason: reason,
		Fields: []FieldIn{{Section: section, Name: field, Type: FieldConcealed, Purpose: PurposePassword, Value: value}}}
	out, err := s.SetItem(who, in)
	if err != nil {
		return Item{}, err
	}
	// the audit says a value was generated, never what it is
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_item_generate",
		Target: row.Title, Detail: fmt.Sprintf("a new value was generated for field %s, stored as version %d", field, out.Version)})
	return out, nil
}

// GenerateValue makes a random value by the spec, with the crypto random source.
func GenerateValue(spec GenSpec) (string, error) {
	if spec.Length <= 0 {
		spec.Length = 24
	}
	if spec.Length > 512 {
		return "", fmt.Errorf("%w: a generated value is at most 512 characters", ErrBadInput)
	}
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const digits = "0123456789"
	const symbols = "!@#$%^&*()-_=+[]{};:,.<>?"
	set := spec.Charset
	if set == "" {
		set = "letters"
	}
	switch set {
	case "letters":
		set = letters
	case "alnum":
		set = letters + digits
	case "all":
		set = letters + digits + symbols
	default:
		return "", fmt.Errorf("%w: the charset is letters, alnum or all", ErrBadInput)
	}
	out := make([]byte, spec.Length)
	buf := make([]byte, spec.Length)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	for i, b := range buf {
		out[i] = set[int(b)%len(set)]
	}
	return string(out), nil
}

// ---- reveal

// Reveal is a person viewing an item's values. The account password is re-entered, unless one was
// within the window the settings give (reveal.reauth_window, five minutes by default); the view is
// recorded to the audit log before the values are returned, and a record that cannot be written
// refuses the view — people view values under the same rule agents will take values with. Fields
// empty gives every field. An otp field answers with the current code and the seconds it has left,
// not its seed. Agents are refused: they come to values through grants, not the console's door.
func (s *Service) Reveal(who Actor, item string, fields []string, password string) (map[string]string, []Field, error) {
	if !who.Person() {
		return nil, nil, ErrAgent
	}
	if s.keys.sealed() {
		return nil, nil, ErrSealed
	}
	row, col, ok := s.Item(item)
	if !ok {
		return nil, nil, fmt.Errorf("%w: no item %q", ErrNotFound, item)
	}
	if !s.May(who, col.ID, PermRead) {
		return nil, nil, fmt.Errorf("%w: you may not read values in this collection", ErrForbidden)
	}
	if password != "" {
		if err := s.d.CheckPassword(who.Name, password); err != nil {
			return nil, nil, ErrReauth
		}
		s.reauthMu.Lock()
		s.reauth[who.Name] = s.now().Add(s.reauthWindow())
		s.reauthMu.Unlock()
	} else if !s.reauthed(who.Name) {
		return nil, nil, ErrReauth
	}
	// the record comes first: no record, no value
	named := ""
	if len(fields) > 0 {
		named = " (fields: " + strings.Join(fields, ", ") + ")"
	}
	if err := s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_item_view",
		Target: row.Title, Detail: "viewed the item's values in " + col.Name + named}); err != nil {
		return nil, nil, fmt.Errorf("the view could not be recorded, so the values are not given: %w", err)
	}
	var out map[string]string
	var list []Field
	err := s.d.Store.Storage().View(func(tx kv.Tx) error {
		ver, ok := getVersionTx(tx, row.ID, row.Version)
		if !ok {
			return fmt.Errorf("%w: the item's current version is missing", ErrNotFound)
		}
		d, err := s.decryptDetailsTx(tx, col, ver)
		if err != nil {
			return err
		}
		out, list = map[string]string{}, nil
		now := s.now()
		for _, f := range ver.Fields {
			if len(fields) > 0 && !containsString(fields, fieldKey(f.Section, f.Name)) && !containsString(fields, f.Name) {
				continue
			}
			value, okV := d.Values[fieldKey(f.Section, f.Name)]
			if !okV {
				continue
			}
			if f.Type == FieldOTP {
				code, left, err := TOTPNow(value, now)
				if err != nil {
					return fmt.Errorf("%w: the field %s has no valid one-time-passcode seed", ErrInvalid, f.Name)
				}
				out[fieldKey(f.Section, f.Name)] = code
				out[fieldKey(f.Section, f.Name)+"\x00expiry"] = i64(int64(left.Seconds()))
				list = append(list, f)
				continue
			}
			out[fieldKey(f.Section, f.Name)] = value
			list = append(list, f)
		}
		if d.Notes != "" && (len(fields) == 0 || containsString(fields, "notes")) {
			out["notes"] = d.Notes
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("%s viewed \"%s\" in %s via %s", who.Name, row.Title, col.Name, s.viaWords(who.Via)))
	}
	return out, list, nil
}

// reauthed reports whether the user re-entered their password within the window.
func (s *Service) reauthed(user string) bool {
	s.reauthMu.Lock()
	defer s.reauthMu.Unlock()
	return s.now().Before(s.reauth[user])
}

// ---- search

// SearchResult is one item the search found: its reference and its overview.
type SearchResult struct {
	Ref        string `json:"ref"`
	Collection string `json:"collection"`
	Item       Item   `json:"item"`
	Level      string `json:"level"`
}

// Search looks for items the actor may describe: by a word in the title, the category or the
// URLs, with the tags narrowing, inside one collection when it names one. The trash and the
// archive both stay out of the results (1Password's rule, which the design follows).
func (s *Service) Search(who Actor, query, collection string, tags []string) []SearchResult {
	var out []SearchResult
	q := strings.ToLower(strings.TrimSpace(query))
	var col Collection
	var colOK bool
	if collection != "" {
		col, colOK = s.Collection(collection)
		if !colOK {
			return nil
		}
	}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(_, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemVerifies(r) {
				return true
			}
			if r.State != StateNormal {
				return true // the trash and the archive both stay out of the search
			}
			if colOK && r.Collection != col.ID {
				return true
			}
			if len(tags) > 0 {
				for _, t := range tags {
					if !containsString(r.Tags, t) {
						return true
					}
				}
			}
			if q != "" {
				hay := strings.ToLower(r.Title + " " + r.Category + " " + strings.Join(r.URLs, " ") + " " + strings.Join(r.Tags, " "))
				if !strings.Contains(hay, q) {
					return true
				}
			}
			if !s.mayTx(tx, who, r.Collection, PermDescribe) {
				return true
			}
			cname := r.Collection
			if c, ok := s.collectionByIDTx(tx, r.Collection); ok {
				cname = c.Name
			}
			out = append(out, SearchResult{Ref: RefOf(cname, r.Title, "", ""), Collection: cname, Item: r.Item})
			return true
		})
		return nil
	})
	for i := range out {
		out[i].Level = s.EffectiveLevel(out[i].Item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item.Title < out[j].Item.Title })
	return out
}
