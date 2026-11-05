package secrets

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/settings"
)

// ---- items

// AttachmentIn is an attachment as it is given to SetItem: its name and its bytes.
type AttachmentIn struct {
	Name string
	Data []byte
}

// FieldIn is one field with its value; Clear removes the field.
type FieldIn struct {
	Section string `json:"section,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`    // concealed when empty
	Purpose string `json:"purpose,omitempty"` // username, password or notes
	Value   string `json:"value"`
	Clear   bool   `json:"clear,omitempty"`
}

// ItemIn is an item as it is given: the overview's parts, the fields with their values, the notes
// and the attachments. ID empty makes a new item; otherwise the item gains a new version. Fields
// and notes fold into the current version's details (what is not named keeps its value), so a
// caller may change one field without reading the item first. Tags and the level are policy:
// changing them needs manage.
type ItemIn struct {
	ID          string         `json:"id,omitempty"`
	Collection  string         `json:"collection"` // name or ID
	Title       string         `json:"title,omitempty"`
	Category    string         `json:"category,omitempty"`
	URLs        []string       `json:"urls,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Level       string         `json:"level,omitempty"`   // "" keeps the item's own; "-" clears it
	Actions     []string       `json:"actions,omitempty"` // "" keeps; "-" clears
	Source      string         `json:"source,omitempty"`
	RotateDays  int            `json:"rotate_days,omitempty"`
	Machines    []string       `json:"machines,omitempty"`
	Hosts       []string       `json:"hosts,omitempty"`
	Fields      []FieldIn      `json:"fields,omitempty"`
	Notes       string         `json:"notes,omitempty"`
	HasNotes    bool           `json:"has_notes,omitempty"`
	Attachments []AttachmentIn `json:"-"`
	Reason      string         `json:"reason,omitempty"`
}

// fieldKey is a value's key in the details: "section\x00name", or "name" with no section.
func fieldKey(section, name string) string {
	if section == "" {
		return name
	}
	return section + "\x00" + name
}

// SetItem creates an item or writes its next version. The values are encrypted under the
// collection's key with the additional data that binds the ciphertext to this item, this version,
// this collection and this key version; the overview row is signed. What the caller does not name
// keeps its value, so the details are decrypted from the current version inside the same
// transaction and folded together before they are encrypted again.
func (s *Service) SetItem(who Actor, in ItemIn) (Item, error) {
	return s.setItem(who, in, false)
}

// setItem is SetItem; rotation skips the people's edit check because RotateItem has already
// verified a live use grant, and refuses a change to tags, level, actions or bindings.
func (s *Service) setItem(who Actor, in ItemIn, rotation bool) (Item, error) {
	if s.keys.sealed() {
		return Item{}, ErrSealed
	}
	col, ok := s.Collection(in.Collection)
	if !ok {
		return Item{}, fmt.Errorf("%w: no collection %q", ErrNotFound, in.Collection)
	}
	if s.rotatingNow(col.ID) {
		return Item{}, fmt.Errorf("%w: the collection's key is being rotated; try again shortly", ErrConflict)
	}
	if err := checkItemIn(in); err != nil {
		return Item{}, err
	}
	maxSize := s.attachmentMax()
	for _, a := range in.Attachments {
		if int64(len(a.Data)) > maxSize {
			return Item{}, fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrAttachment, a.Name, len(a.Data), maxSize)
		}
	}
	var out Item
	var policyTightened, valueChanged bool
	var before Item
	created := false
	var err error
	s.mu.Lock()
	sk, lockErr := s.sk()
	if lockErr != nil {
		s.mu.Unlock()
		return Item{}, lockErr
	}
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		var cur signedItem
		exists := false
		if in.ID != "" {
			v := tx.Get(bucketItems, []byte(in.ID))
			if v == nil {
				return fmt.Errorf("%w: no item %q", ErrNotFound, in.ID)
			}
			if json.Unmarshal(v, &cur) != nil {
				return fmt.Errorf("%w: the item's row is unreadable", ErrNotFound)
			}
			if !s.itemVerifies(cur) {
				return fmt.Errorf("%w: the item's row does not verify", ErrNotFound)
			}
			if cur.ID != in.ID {
				return fmt.Errorf("%w: the item's row does not match its key", ErrNotFound)
			}
			if cur.Collection != col.ID {
				return fmt.Errorf("%w: the item belongs to another collection", ErrBadInput)
			}
			exists = true
			before = cur.Item // the overview as it stands, before this change folds in
		}
		// permissions: writing needs edit; tags, level, actions, machines and hosts are policy, so manage.
		// a rotation already verified a live use grant and must not change policy.
		policy := len(in.Tags) > 0 || in.Level != "" || len(in.Actions) > 0 || len(in.Machines) > 0 || len(in.Hosts) > 0
		if rotation {
			if policy {
				return fmt.Errorf("%w: a rotation cannot change an item's tags, level, actions or bindings", ErrForbidden)
			}
		} else {
			if policy && !s.mayTx(tx, who, col.ID, PermManage) {
				return fmt.Errorf("%w: changing an item's tags, level, actions or bindings needs manage", ErrForbidden)
			}
			if !s.mayTx(tx, who, col.ID, PermEdit) {
				return fmt.Errorf("%w: you may not write in this collection", ErrForbidden)
			}
		}
		item := cur.Item
		var details Details
		var fields []Field
		if exists {
			item = cur.Item
			ver, ok := getVersionTx(tx, cur.ID, cur.Version)
			if !ok {
				return fmt.Errorf("%w: the item's current version is missing", ErrNotFound)
			}
			d, err := s.decryptDetailsTx(tx, col, ver, cur.ID, cur.Version)
			if err != nil {
				return fmt.Errorf("the current version could not be opened: %w", err)
			}
			details, fields = *d, ver.Fields
		} else {
			id := in.ID
			if id == "" {
				var err error
				id, err = newID()
				if err != nil {
					return err
				}
			}
			item = Item{ID: id, Collection: col.ID, State: StateNormal, Created: s.now(), CreatedBy: who.Name}
			details = Details{Values: map[string]string{}}
			created = true
		}
		if in.Title != "" {
			item.Title = in.Title
		}
		if !exists && item.Title == "" {
			return fmt.Errorf("%w: a new item needs a title", ErrBadInput)
		}
		if in.Category != "" {
			item.Category = in.Category
		}
		if len(in.URLs) > 0 {
			item.URLs = compact(in.URLs)
		}
		if len(in.Tags) > 0 {
			item.Tags = compactLower(in.Tags)
		}
		switch in.Level {
		case "":
		case "-":
			item.Level = ""
		default:
			item.Level = in.Level
		}
		if len(in.Actions) > 0 {
			if len(in.Actions) == 1 && in.Actions[0] == "-" {
				item.Actions = nil
			} else {
				for _, a := range in.Actions {
					if a != "describe" && a != "read" && a != "use" {
						return fmt.Errorf("%w: an item's actions are describe, read and use", ErrBadInput)
					}
				}
				item.Actions = compact(in.Actions)
			}
		}
		if in.Source != "" {
			item.Source = in.Source
		}
		if in.RotateDays != 0 {
			item.RotateDays = in.RotateDays
		}
		if len(in.Machines) > 0 {
			item.Machines = compact(in.Machines)
		}
		if len(in.Hosts) > 0 {
			item.Hosts = compact(in.Hosts)
		}
		// fold the caller's fields and notes into the details
		for _, f := range in.Fields {
			typ := f.Type
			if typ == "" {
				typ = FieldConcealed
			}
			key := fieldKey(f.Section, f.Name)
			if f.Clear {
				delete(details.Values, key)
				fields = removeField(fields, f.Section, f.Name)
				continue
			}
			if details.Values == nil {
				details.Values = map[string]string{}
			}
			details.Values[key] = f.Value
			if !hasField(fields, f.Section, f.Name) {
				fields = append(fields, Field{Section: f.Section, Name: f.Name, Type: typ, Purpose: f.Purpose})
			} else {
				fields = withFieldType(fields, f.Section, f.Name, typ, f.Purpose)
			}
		}
		if in.HasNotes {
			details.Notes = in.Notes
		}
		next := item.Version + 1
		details.Fields = fields
		ver := ItemVersion{Item: item.ID, Version: next, Fields: fields, KeyVersion: col.KeyVersion,
			Actor: who.Name, Via: who.Via, At: s.now(), Reason: in.Reason}
		ck, err := s.collectionKeyByVersionTx(tx, col.ID, col.KeyVersion)
		if err != nil {
			return err
		}
		plain, err := json.Marshal(details)
		if err != nil {
			clear(ck)
			return err
		}
		ver.CT, err = wrap(ck, plain, itemAAD(item.ID, next, col.ID, col.KeyVersion))
		if err != nil {
			clear(ck)
			return err
		}
		if err := putVersionTx(tx, ver); err != nil {
			clear(ck)
			return err
		}
		for _, a := range in.Attachments {
			if a.Name == "" {
				clear(ck)
				return fmt.Errorf("%w: an attachment has a name", ErrBadInput)
			}
			ct, err := wrap(ck, a.Data, attachmentAAD(item.ID, next, col.ID, col.KeyVersion, a.Name))
			if err != nil {
				clear(ck)
				return err
			}
			att := Attachment{Item: item.ID, Version: next, Name: a.Name, Size: int64(len(a.Data)), CT: ct}
			b, err := json.Marshal(att)
			if err != nil {
				clear(ck)
				return err
			}
			if err := tx.Put(bucketFiles, attachmentRowKey(item.ID, next, a.Name), b); err != nil {
				clear(ck)
				return err
			}
			if !containsString(details.Attachments, a.Name) {
				details.Attachments = append(details.Attachments, a.Name)
			}
		}
		clear(ck)
		if len(in.Attachments) > 0 {
			// the details' attachment list moved: encrypt the version again with it inside
			plain, err := json.Marshal(details)
			if err != nil {
				return err
			}
			ck2, err := s.collectionKeyByVersionTx(tx, col.ID, col.KeyVersion)
			if err != nil {
				return err
			}
			ver.CT, err = wrap(ck2, plain, itemAAD(item.ID, next, col.ID, col.KeyVersion))
			clear(ck2)
			if err != nil {
				return err
			}
			if err := putVersionTx(tx, ver); err != nil {
				return err
			}
		}
		item.Version = next
		if exists {
			policyTightened = before.Level != item.Level || !equalStrings(before.Tags, item.Tags) || !equalStrings(before.Actions, item.Actions)
			valueChanged = true // a new version is a new value, whatever else changed
		}
		r := signedItem{Item: item}
		r.Sig = signRow(sk, item.signedParts()...)
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := tx.Put(bucketItems, []byte(item.ID), b); err != nil {
			return err
		}
		out = item
		s.pruneVersions(tx, item.ID, next)
		return nil
	})
	s.mu.Unlock()
	if err != nil {
		return Item{}, err
	}
	verb, action := "changed the item", "secrets_item_update"
	if created {
		verb, action = "created the item", "secrets_item_create"
	}
	s.echo(who, verb, out.Title+" in "+col.Name+" (version "+i64(out.Version)+")", action)
	if policyTightened {
		s.reassessGrants() // outside the lock: it takes the lock itself per grant
	}
	if valueChanged {
		s.notifyItemChanged(out, before.Version)
	}
	return out, nil
}

// checkItemIn is the shape check done before any lock is taken.
func checkItemIn(in ItemIn) error {
	if hasNUL(in.Title, in.Category, in.Level, in.Source, in.Notes, in.Reason) {
		return fmt.Errorf("%w: a value may not hold a NUL", ErrBadInput)
	}
	if hasNUL(in.URLs...) || hasNUL(in.Tags...) || hasNUL(in.Actions...) || hasNUL(in.Machines...) || hasNUL(in.Hosts...) {
		return fmt.Errorf("%w: a value may not hold a NUL", ErrBadInput)
	}
	for _, f := range in.Fields {
		if hasNUL(f.Section, f.Name, f.Type, f.Purpose, f.Value) {
			return fmt.Errorf("%w: a value may not hold a NUL", ErrBadInput)
		}
	}
	if in.Title != "" && !nameRe.MatchString(in.Title) {
		return fmt.Errorf("%w: an item's title is up to 64 letters, digits, spaces, - _ and ., starting with a letter or digit; the reference syntax needs this shape (or the ID)", ErrBadInput)
	}
	if in.Level != "" && in.Level != "-" && !ValidLevel(in.Level) {
		return fmt.Errorf("%w: a level is one of %s", ErrBadInput, joinStrings(Levels))
	}
	for _, t := range in.Tags {
		if !tagRe.MatchString(t) {
			return fmt.Errorf("%w: the tag %q is not lower-case and layered with /", ErrBadInput, t)
		}
	}
	for _, f := range in.Fields {
		if f.Name == "" {
			return fmt.Errorf("%w: a field has a name", ErrBadInput)
		}
		typ := f.Type
		if typ == "" {
			typ = FieldConcealed
		}
		if !containsString(FieldTypes, typ) {
			return fmt.Errorf("%w: a field's type is one of %s", ErrBadInput, joinStrings(FieldTypes))
		}
		if f.Purpose != "" && !containsString(Purposes, f.Purpose) {
			return fmt.Errorf("%w: a field's purpose is one of %s", ErrBadInput, joinStrings(Purposes))
		}
	}
	return nil
}

func hasField(fields []Field, section, name string) bool {
	for _, f := range fields {
		if f.Section == section && f.Name == name {
			return true
		}
	}
	return false
}

func removeField(fields []Field, section, name string) []Field {
	var out []Field
	for _, f := range fields {
		if f.Section != section || f.Name != name {
			out = append(out, f)
		}
	}
	return out
}

func withFieldType(fields []Field, section, name, typ, purpose string) []Field {
	for i, f := range fields {
		if f.Section == section && f.Name == name {
			if typ != "" {
				fields[i].Type = typ
			}
			if purpose != "" {
				fields[i].Purpose = purpose
			}
		}
	}
	return fields
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func compact(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return compactUnique(out)
}

func compactLower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSpace(s))
	}
	sort.Strings(out)
	return compactUnique(out)
}

func compactUnique(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

func versionRowKey(item string, version int64) []byte {
	return []byte(fmt.Sprintf("%s\x00%016x", item, version))
}

func attachmentRowKey(item string, version int64, name string) []byte {
	return []byte(fmt.Sprintf("%s\x00%016x\x00%s", item, version, name))
}

func putVersionTx(tx kv.Tx, ver ItemVersion) error {
	b, err := json.Marshal(ver)
	if err != nil {
		return err
	}
	return tx.Put(bucketVersions, versionRowKey(ver.Item, ver.Version), b)
}

func getVersionTx(tx kv.Tx, item string, version int64) (ItemVersion, bool) {
	var ver ItemVersion
	v := tx.Get(bucketVersions, versionRowKey(item, version))
	return ver, v != nil && json.Unmarshal(v, &ver) == nil
}

// collectionByIDTx finds a collection row by ID inside a transaction.
func (s *Service) collectionByIDTx(tx kv.Tx, id string) (Collection, bool) {
	v := tx.Get(bucketCollections, []byte(id))
	var r signedCollection
	if v == nil || json.Unmarshal(v, &r) != nil || r.ID != id || !s.collectionLive(tx, r) {
		return Collection{}, false
	}
	return r.Collection, true
}

// collectionKeyByVersionTx unwraps a collection key of one version inside a transaction.
func (s *Service) collectionKeyByVersionTx(tx kv.Tx, collection string, version int64) ([]byte, error) {
	rk, _, err := s.keys.keys()
	if err != nil {
		return nil, err
	}
	v := tx.Get(bucketKeys, keyRowKey(collection, version))
	if v == nil {
		return nil, fmt.Errorf("%w: collection key version %d", ErrNotFound, version)
	}
	var kr keyRow
	if json.Unmarshal(v, &kr) != nil {
		return nil, fmt.Errorf("%w: collection key version %d is unreadable", ErrNotFound, version)
	}
	return unwrap(rk, kr.Wrapped, keyWrapAAD(collection, version))
}

// decryptDetailsTx opens a version's details. AAD is the caller's expected item and version, not
// whatever the stored row claims, so a ciphertext copied onto another item does not open.
func (s *Service) decryptDetailsTx(tx kv.Tx, col Collection, ver ItemVersion, item string, version int64) (*Details, error) {
	if ver.Item != "" && ver.Item != item {
		return nil, fmt.Errorf("%w: the version row names another item", ErrCrypto)
	}
	if ver.Version != 0 && ver.Version != version {
		return nil, fmt.Errorf("%w: the version row names another version", ErrCrypto)
	}
	ck, err := s.collectionKeyByVersionTx(tx, col.ID, ver.KeyVersion)
	if err != nil {
		return nil, err
	}
	defer clear(ck)
	plain, err := unwrap(ck, ver.CT, itemAAD(item, version, col.ID, ver.KeyVersion))
	if err != nil {
		return nil, err
	}
	var d Details
	if err := json.Unmarshal(plain, &d); err != nil {
		return nil, fmt.Errorf("%w: the version's details are unreadable", ErrCrypto)
	}
	return &d, nil
}

// mayTx is May inside a transaction.
func (s *Service) mayTx(tx kv.Tx, who Actor, collectionID, perm string) bool {
	have, ok := s.permOfTx(tx, who, collectionID)
	return ok && PermAtLeast(have, perm)
}

func (s *Service) permOfTx(tx kv.Tx, who Actor, collectionID string) (string, bool) {
	if who.Owner() {
		return PermManage, true
	}
	if !who.Person() || s.keys.sealed() {
		return "", false
	}
	best, found := "", false
	for _, subject := range []string{permSubjectPrefix + who.Name, "role:" + who.Role} {
		key := permRowKey(collectionID, subject)
		v := tx.Get(bucketPerms, key)
		if v == nil {
			continue
		}
		var p Permission
		if json.Unmarshal(v, &p) != nil || p.Collection != collectionID || p.Subject != subject || !s.permLive(tx, key, p) {
			continue // a row that does not verify, or that was copied under another key, is not there
		}
		if !found || permRank[p.Perm] > permRank[best] {
			best, found = p.Perm, true
		}
	}
	return best, found
}

// itemVerifies is the signature check an item's row passes to be served at all.
func (s *Service) itemVerifies(r signedItem) bool {
	sk, err := s.sk()
	if err != nil {
		return false
	}
	return verifyPolicy(sk, r.Sig, r.signedParts()...)
}

// attachmentMax is the attachment size limit, from the settings (10 MB by default).
func (s *Service) attachmentMax() int64 {
	if s.d.Settings != nil {
		if res, err := s.d.Settings.System("attachments.max_size"); err == nil {
			if n, ok := jsonInt(res.Value); ok && n > 0 {
				return n
			}
		}
	}
	return 10 << 20
}

// jsonInt reads a whole number out of a settings value.
func jsonInt(raw json.RawMessage) (int64, bool) {
	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	return 0, false
}

// jsonMap reads an object out of a settings value.
func jsonMap(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// settingDuration reads a duration setting with the settings package's own parser; def when there
// are no settings or the row does not answer.
func (s *Service) settingDuration(key string, def time.Duration) time.Duration {
	if s.d.Settings != nil {
		if res, err := s.d.Settings.System(key); err == nil {
			var str string
			if err := json.Unmarshal(res.Value, &str); err == nil {
				if d, err := settings.ParseDuration(str); err == nil {
					return d
				}
			}
		}
	}
	return def
}

// settingInt reads an integer setting.
func (s *Service) settingInt(key string, def int64) int64 {
	if s.d.Settings != nil {
		if res, err := s.d.Settings.System(key); err == nil {
			if n, ok := jsonInt(res.Value); ok {
				return n
			}
		}
	}
	return def
}

// reauthWindow is how long after re-entering a password a person may view values again.
func (s *Service) reauthWindow() time.Duration {
	return s.settingDuration("reveal.reauth_window", 5*time.Minute)
}

// trashKeep is how long removed items stay in the trash.
func (s *Service) trashKeep() time.Duration {
	return s.settingDuration("retention.trash", 30*24*time.Hour)
}

// pruneVersions drops the versions older than the settings keep, always keeping the newest
// min_versions whatever their age. The current version is never dropped.
func (s *Service) pruneVersions(tx kv.Tx, item string, current int64) {
	keep := s.settingInt("retention.item_versions.min", 10)
	age := 365 * 24 * time.Hour
	if s.d.Settings != nil {
		if res, err := s.d.Settings.System("retention.item_versions"); err == nil {
			if m := jsonMap(res.Value); m != nil {
				if n, ok := jsonInt(m["min_versions"]); ok && n > 0 {
					keep = n
				}
				var str string
				if err := json.Unmarshal(m["keep"], &str); err == nil {
					if d, err := settings.ParseDuration(str); err == nil {
						age = d
					}
				}
			}
		}
	}
	var versions []int64
	prefix := []byte(item + "\x00")
	tx.Range(bucketVersions, kv.Range{Prefix: prefix}, func(k, _ []byte) bool {
		parts := strings.SplitN(string(k), "\x00", 2)
		if len(parts) == 2 {
			var v int64
			if _, err := fmt.Sscanf(parts[1], "%x", &v); err == nil {
				versions = append(versions, v)
			}
		}
		return true
	})
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] }) // newest first
	now := s.now()
	for i, v := range versions {
		if i < int(keep) || v >= current {
			continue
		}
		ver, ok := getVersionTx(tx, item, v)
		if !ok {
			continue
		}
		if now.Sub(ver.At) < age {
			continue
		}
		_ = tx.Delete(bucketVersions, versionRowKey(item, v))
		fprefix := []byte(fmt.Sprintf("%s\x00%016x\x00", item, v))
		tx.Range(bucketFiles, kv.Range{Prefix: fprefix}, func(k, _ []byte) bool {
			_ = tx.Delete(bucketFiles, append([]byte(nil), k...))
			return true
		})
	}
}
