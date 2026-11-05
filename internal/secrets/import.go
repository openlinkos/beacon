package secrets

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/secrets/importer"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

// The import: what the importer package parses, written into the vault. The levels come from the
// settings (import.levels.by_source and by_category, which the settings package registers with
// their defaults: the personal 1Password vaults and Chrome at person, every other source at
// review, the identity and finance categories at never), the conflicts are resolved by
// import.on_conflict or the call's own choice, and a dry-run answers the same report without
// writing anything. No value ever appears in a report: it counts items, fields, attachments and
// one-time-passcode fields, names the categories and the levels, and says which incoming items
// meet an item that is already there — the same place, by the normalised URL, with the same user
// name. The user names themselves are values: they stay out.

// ImportOptions is how one import runs. OnConflict picks skip (the default), new_version (the
// item that is there gains a version with the imported values) or keep_both; Levels overrides the
// level a collection's items start at, by collection name; Collection folds the whole import into
// one named collection.
type ImportOptions struct {
	DryRun     bool              `json:"dry_run,omitempty"`
	OnConflict string            `json:"on_conflict,omitempty"` // new_version, skip or keep_both
	Levels     map[string]string `json:"levels,omitempty"`      // collection name → level
	Collection string            `json:"collection,omitempty"`  // one target collection for everything
	Reason     string            `json:"reason,omitempty"`
}

// ImportReport is what an import answers: per collection the counts, the categories and the level
// the items start at, and after a real import what happened to each item. Titles name the
// conflicts; values never appear.
type ImportReport struct {
	Source      string             `json:"source"`
	DryRun      bool               `json:"dry_run"`
	Collections []CollectionReport `json:"collections"`
	Conflicts   []ConflictReport   `json:"conflicts,omitempty"`
	Created  int `json:"created,omitempty"`
	Versions int `json:"versions_added,omitempty"` // new versions of items already there
	Skipped  int `json:"skipped,omitempty"`
	KeptBoth int `json:"kept_both,omitempty"`
	Merged   int `json:"merged,omitempty"` // rows folded together before the import (Chrome)
}

// CollectionReport is one target collection: whether it would be made, the level its items start
// at, the counts, and how many items a category lifts to never whatever the collection says.
type CollectionReport struct {
	Name        string          `json:"name"`
	New         bool            `json:"new"`
	Level       string          `json:"level"`
	Items       int             `json:"items"`
	Fields      int             `json:"fields"`
	Attachments int             `json:"attachments"`
	OTPs        int             `json:"one_time_passcodes"`
	Archived    int             `json:"archived"`
	Never       int             `json:"never"`   // items a category starts at never
	History     int             `json:"history"` // older values that become versions
	Categories  []CategoryCount `json:"categories"`
}

// CategoryCount is one category's share of a collection.
type CategoryCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ConflictReport names one conflict: the incoming item, the item that is already there, and how
// many places they share.
type ConflictReport struct {
	Collection string `json:"collection"`
	Incoming   string `json:"incoming"` // the incoming item's title
	Existing   string `json:"existing"` // the item that is already there
	SharedURLs int    `json:"shared_urls"`
}

// importLevels are the level maps the settings hold, with their registered defaults for when no
// settings are wired.
type importLevels struct {
	bySource   map[string]string
	byCategory map[string]string
	onConflict string
}

// defaultBySource and defaultByCategory mirror the settings package's registered defaults, so an
// import with no settings wired behaves exactly like one with them at their defaults.
var (
	defaultBySource   = map[string]string{"default": LevelReview, "1password:personal": LevelPerson, "1password:private": LevelPerson, "chrome": LevelPerson}
	defaultByCategory = map[string]string{"credit_card": LevelNever, "bank_account": LevelNever, "identity": LevelNever,
		"passport": LevelNever, "driver_license": LevelNever, "social_security_number": LevelNever,
		"medical_record": LevelNever, "membership": LevelNever, "reward_program": LevelNever}
)

// importLevelsOf reads the import's settings, falling back to the registered defaults.
func (s *Service) importLevelsOf() importLevels {
	out := importLevels{bySource: map[string]string{}, byCategory: map[string]string{}, onConflict: "skip"}
	for k, v := range defaultBySource {
		out.bySource[k] = v
	}
	for k, v := range defaultByCategory {
		out.byCategory[k] = v
	}
	if s.d.Settings == nil {
		return out
	}
	if res, err := s.d.Settings.System("import.levels.by_source"); err == nil {
		for k, v := range levelMap(res.Value) {
			out.bySource[k] = v
		}
	}
	if res, err := s.d.Settings.System("import.levels.by_category"); err == nil {
		for k, v := range levelMap(res.Value) {
			out.byCategory[k] = v
		}
	}
	if res, err := s.d.Settings.System("import.on_conflict"); err == nil {
		var v string
		if json.Unmarshal(res.Value, &v) == nil && v != "" {
			if v == "ask" {
				v = "skip" // ask used to drop conflicts; skip is the remaining default
			}
			out.onConflict = v
		}
	}
	return out
}

// levelMap reads a {"name": "level"} settings value.
func levelMap(raw json.RawMessage) map[string]string {
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// collectionLevel is the level one target collection's items start at: the call's own choice for
// it, else the source's level, else the source's default, else review.
func (l importLevels) collectionLevel(name, sourceKey string, overrides map[string]string) string {
	if lvl, ok := overrides[name]; ok && ValidLevel(lvl) {
		return lvl
	}
	if lvl, ok := l.bySource[sourceKey]; ok && ValidLevel(lvl) {
		return lvl
	}
	if lvl, ok := l.bySource["default"]; ok && ValidLevel(lvl) {
		return lvl
	}
	return LevelReview
}

// itemLevel is the level one item starts at: its category's own when it has one (the identity and
// finance categories start at never), else empty, which inherits the collection's default.
func (l importLevels) itemLevel(category string) string {
	return l.byCategory[category]
}

// conflictIndex is what an incoming item is matched against: every normalised URL a collection's
// items hold, with the user names each URL's item uses, and the items' titles for the report.
type conflictIndex struct {
	urls   map[string][]string // normalised URL → item IDs
	names  map[string][]string // item ID → the item's user names
	titles map[string]string   // item ID → title
}

func newConflictIndex() *conflictIndex {
	return &conflictIndex{urls: map[string][]string{}, names: map[string][]string{}, titles: map[string]string{}}
}

// add remembers one item: its places and its user names.
func (x *conflictIndex) add(itemID, title string, urls, usernames []string) {
	x.titles[itemID] = title
	x.names[itemID] = usernames
	for _, u := range urls {
		n := importer.NormalizeURL(u)
		if n == "" || !containsString(x.urls[n], itemID) {
			if n != "" {
				x.urls[n] = append(x.urls[n], itemID)
			}
		}
	}
}

// conflictHit is one item an incoming item met.
type conflictHit struct {
	item   string
	title  string
	shared []string // the normalised URLs both hold
}

// hit says whether the incoming item meets the item named: a shared normalised URL and a shared
// user name. An incoming item with no user name, or none of the index's URLs, meets nothing.
func (x *conflictIndex) hit(item importer.ItemImport) *conflictHit {
	users := usernamesOf(item.Fields)
	if len(users) == 0 {
		return nil
	}
	byItem := map[string][]string{}
	for _, u := range item.URLs {
		n := importer.NormalizeURL(u)
		if n == "" {
			continue
		}
		for _, id := range x.urls[n] {
			for _, want := range users {
				if containsString(x.names[id], want) {
					byItem[id] = appendUnique(byItem[id], n)
				}
			}
		}
	}
	if len(byItem) == 0 {
		return nil
	}
	// the first item met, by ID, so the answer is steady
	ids := make([]string, 0, len(byItem))
	for id := range byItem {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return &conflictHit{item: ids[0], title: x.titles[ids[0]], shared: byItem[ids[0]]}
}

// usernamesOf picks the fields that name a user: the purpose says it, or the name does.
func usernamesOf(fields []importer.FieldImport) []string {
	var out []string
	for _, f := range fields {
		if f.Value == "" {
			continue
		}
		if f.Purpose == importer.PurposeUsername || strings.EqualFold(f.Name, "username") {
			out = appendUnique(out, f.Value)
		}
	}
	return out
}

// appendUnique appends s when it is not there.
func appendUnique(in []string, s string) []string {
	if containsString(in, s) {
		return in
	}
	return append(in, s)
}

// conflictIndexOf reads one collection's items and their current details, and indexes every
// item's places and user names. The details open under the collection's key of the version's own
// key version, as everywhere else in the vault; an item whose details will not open is still
// there, just not matchable.
func (s *Service) conflictIndexOf(collectionID string) *conflictIndex {
	idx := newConflictIndex()
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketItems, kv.Range{}, func(k, v []byte) bool {
			var r signedItem
			if json.Unmarshal(v, &r) != nil || !s.itemLive(tx, k, v, r) || r.Collection != collectionID || r.State == StateTrash {
				return true
			}
			ver, ok := getVersionTx(tx, r.ID, r.Version)
			if !ok {
				return true
			}
			col, ok := s.collectionByIDTx(tx, collectionID)
			if !ok {
				return true
			}
			d, err := s.decryptDetailsTx(tx, col, ver, r.ID, r.Version)
			if err != nil {
				return true
			}
			users := []string{}
			for _, f := range ver.Fields {
				if f.Purpose == PurposeUsername || strings.EqualFold(f.Name, "username") {
					if v := d.Values[fieldKey(f.Section, f.Name)]; v != "" {
						users = appendUnique(users, v)
					}
				}
			}
			idx.add(r.ID, r.Title, r.URLs, users)
			return true
		})
		return nil
	})
	return idx
}

// Import runs one import. A dry-run answers the report and writes nothing; a real import writes
// the items, resolves the conflicts by the strategy, and answers the report with what happened.
// The caller is a person (an agent is refused); an existing target collection needs edit, and a
// collection that does not exist yet is made — the doors that reach here (the console and the
// command line's socket) decide who may import at all.
func (s *Service) Import(who Actor, batch importer.Batch, opts ImportOptions) (ImportReport, error) {
	if !who.Person() {
		return ImportReport{}, ErrAgent
	}
	if s.keys.sealed() {
		return ImportReport{}, ErrSealed
	}
	if batch.Source == "" || len(batch.Collections) == 0 {
		return ImportReport{}, fmt.Errorf("%w: an import is a source with collections of items", ErrBadInput)
	}
	if opts.Collection != "" {
		batch = batch.IntoCollection(opts.Collection)
	}
	levels := s.importLevelsOf()
	onConflict := opts.OnConflict
	if onConflict == "" {
		onConflict = levels.onConflict
	}
	switch onConflict {
	case "new_version", "skip", "keep_both":
	default:
		return ImportReport{}, fmt.Errorf("%w: on_conflict is new_version, skip or keep_both", ErrBadInput)
	}
	for name, lvl := range opts.Levels {
		if !ValidLevel(lvl) {
			return ImportReport{}, fmt.Errorf("%w: the level for %s is not one of %s", ErrBadInput, name, joinStrings(Levels))
		}
	}
	// the targets: every collection of the batch, existing (checked for edit) or to be made
	type target struct {
		imported importer.CollectionImport
		col      Collection
		level    string
		new      bool
		index    *conflictIndex
	}
	var targets []target
	seen := map[string]bool{}
	for _, c := range batch.Collections {
		if strings.TrimSpace(c.Name) == "" {
			return ImportReport{}, fmt.Errorf("%w: an import's collection needs a name", ErrBadInput)
		}
		if seen[c.Name] {
			return ImportReport{}, fmt.Errorf("%w: the collection %q comes twice", ErrBadInput, c.Name)
		}
		seen[c.Name] = true
		t := target{imported: c, level: levels.collectionLevel(c.Name, c.LevelKey, opts.Levels)}
		if existing, ok := s.Collection(c.Name); ok {
			if !s.May(who, existing.ID, PermEdit) {
				return ImportReport{}, fmt.Errorf("%w: you may not import into %s", ErrForbidden, c.Name)
			}
			t.col, t.new = existing, false
			t.index = s.conflictIndexOf(existing.ID)
		} else {
			t.new = true
			t.index = newConflictIndex()
		}
		targets = append(targets, t)
	}
	report := ImportReport{Source: batch.Source, DryRun: opts.DryRun, Merged: batch.Merged}
	end := func(failed bool) {
		if opts.DryRun {
			return
		}
		total := report.Created + report.Versions + report.KeptBoth
		_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_import_end",
			Detail: fmt.Sprintf("imported %d item(s) from %s (%d created, %d versions, %d skipped, %d kept both; failed=%t)",
				total, batch.Source, report.Created, report.Versions, report.Skipped, report.KeptBoth, failed)})
		if !failed && s.d.Bot != nil {
			_ = s.d.Bot.Send(s.ctx, s.t("%s imported %d item(s) from %s via %s", who.Name, total, batch.Source, s.viaWords(who.Via)))
		}
	}
	if !opts.DryRun {
		maxSize := s.attachmentMax()
		for _, t := range targets {
			for _, item := range t.imported.Items {
				for _, a := range item.Attachments {
					if int64(len(a.Data)) > maxSize {
						return ImportReport{}, fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrAttachment, a.Name, len(a.Data), maxSize)
					}
				}
			}
		}
	}
	for i := range targets {
		t := &targets[i]
		cr := CollectionReport{Name: t.imported.Name, New: t.new, Level: t.level}
		cats := map[string]int{}
		for _, item := range t.imported.Items {
			if opts.DryRun {
				if _, err := checkImportedItem(item, levels); err != nil {
					return report, err
				}
			}
			cr.Items++
			cr.Fields += len(item.Fields)
			cr.Attachments += len(item.Attachments)
			cr.History += len(item.History)
			if item.State == importer.StateArchived {
				cr.Archived++
			}
			if item.Category != "" {
				cats[item.Category]++
			}
			if levels.itemLevel(item.Category) == LevelNever {
				cr.Never++
			}
			for _, f := range item.Fields {
				if f.Type == importer.TypeOTP {
					cr.OTPs++
				}
			}
			hit := t.index.hit(item)
			if hit != nil {
				report.Conflicts = append(report.Conflicts, ConflictReport{Collection: t.imported.Name,
					Incoming: item.Title, Existing: hit.title, SharedURLs: len(hit.shared)})
			}
		}
		for _, name := range sortedKeys(cats) {
			cr.Categories = append(cr.Categories, CategoryCount{Name: name, Count: cats[name]})
		}
		report.Collections = append(report.Collections, cr)
	}
	if opts.DryRun {
		return report, nil
	}
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_import_begin",
		Detail: fmt.Sprintf("import from %s into %d collection(s)", batch.Source, len(targets))})
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refuseIfRotating(); err != nil {
		end(true)
		return report, err
	}
	if _, err := s.sk(); err != nil {
		end(true)
		return report, err
	}
	policyPuts := map[string][]byte{}
	itemPuts := map[string][]byte{}
	var createdCols []Collection
	var itemAudits []store.AuditEntry
	var policyHead, itemsHead signedhead.Head
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		for i := range targets {
			t := &targets[i]
			if t.new {
				col, b, err := s.insertCollectionTx(tx, who, t.imported.Name, t.imported.Description, t.level)
				if err != nil {
					return err
				}
				policyPuts[policyRowKey(kindCollection, col.ID)] = b
				t.col = col
				createdCols = append(createdCols, col)
			}
			for _, item := range t.imported.Items {
				hit := t.index.hit(item)
				switch {
				case hit == nil:
					written, b, err := s.importCreateTx(tx, who, t.col, item, levels, opts)
					if err != nil {
						return err
					}
					itemPuts[written.ID] = b
					t.index.add(written.ID, written.Title, written.URLs, importedUsernames(item.Fields))
					report.Created++
					itemAudits = append(itemAudits, store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_item_create",
						Target: written.Title, Detail: "imported into " + t.col.Name})
				case onConflict == "new_version":
					written, b, err := s.importNewVersionTx(tx, who, hit.item, item, opts)
					if err != nil {
						return err
					}
					itemPuts[written.ID] = b
					t.index.add(written.ID, written.Title, written.URLs, importedUsernames(item.Fields))
					report.Versions++
					itemAudits = append(itemAudits, store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_item_update",
						Target: written.Title, Detail: "imported a new version in " + t.col.Name})
				case onConflict == "keep_both":
					written, b, err := s.importCreateTx(tx, who, t.col, item, levels, opts)
					if err != nil {
						return err
					}
					itemPuts[written.ID] = b
					t.index.add(written.ID, written.Title, written.URLs, importedUsernames(item.Fields))
					report.KeptBoth++
					itemAudits = append(itemAudits, store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_item_create",
						Target: written.Title, Detail: "imported into " + t.col.Name})
				default:
					report.Skipped++
				}
			}
		}
		var err error
		if len(policyPuts) > 0 {
			policyHead, err = s.commitPolicy(tx, policyPuts, nil)
			if err != nil {
				return err
			}
		}
		if len(itemPuts) > 0 {
			itemsHead, err = s.commitItems(tx, itemPuts, nil)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		report.Created, report.Versions, report.KeptBoth, report.Skipped = 0, 0, 0, 0
		end(true)
		return report, err
	}
	s.afterHeads(policyHead, itemsHead)
	for _, e := range itemAudits {
		_ = s.auditEntry(e)
	}
	for _, col := range createdCols {
		s.echo(who, "created the collection", col.Name, "secrets_collection_create")
	}
	end(false)
	return report, nil
}

// importedUsernames picks the user names out of the imported fields, for the index.
func importedUsernames(fields []importer.FieldImport) []string {
	var out []string
	for _, f := range fields {
		if f.Value != "" && (f.Purpose == importer.PurposeUsername || strings.EqualFold(f.Name, "username")) {
			out = appendUnique(out, f.Value)
		}
	}
	return out
}

// sortedKeys is a map's keys, sorted, for steady reports.
func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// importField checks and keeps one imported field: the type and the purpose land in the vault's
// own vocabulary, a nameless field goes, and the section rides along.
func importField(f importer.FieldImport) (Field, string, bool) {
	if strings.TrimSpace(f.Name) == "" {
		return Field{}, "", false
	}
	typ := f.Type
	switch typ {
	case importer.TypeConcealed, importer.TypeString, importer.TypeEmail, importer.TypeURL, importer.TypeOTP, importer.TypeDate, importer.TypeFile:
	default:
		typ = importer.TypeString
	}
	purpose := f.Purpose
	if purpose != importer.PurposeUsername && purpose != importer.PurposePassword && purpose != importer.PurposeNotes {
		purpose = ""
	}
	return Field{Section: strings.TrimSpace(f.Section), Name: strings.TrimSpace(f.Name), Type: typ, Purpose: purpose}, f.Value, true
}

// importedItem is one import item, checked and folded into the vault's own shapes: the fields with
// their values, the notes, and the overview's parts.
type importedItem struct {
	title    string
	category string
	urls     []string
	tags     []string
	state    string
	level    string
	source   string
	fields   []Field
	values   map[string]string
	notes    string
}

// checkImportedItem shapes one item, with its history count said for the versions it will need.
func checkImportedItem(in importer.ItemImport, levels importLevels) (importedItem, error) {
	if strings.TrimSpace(in.Title) == "" {
		return importedItem{}, fmt.Errorf("%w: an item needs a title", ErrBadInput)
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Title)) > 128 {
		return importedItem{}, fmt.Errorf("%w: an item's title is at most 128 characters (the reference addresses it by ID)", ErrBadInput)
	}
	out := importedItem{title: strings.TrimSpace(in.Title), category: in.Category, state: StateNormal,
		level: levels.itemLevel(in.Category), source: in.Source, values: map[string]string{}}
	if in.State == importer.StateArchived {
		out.state = StateArchived
	}
	out.urls = compact(in.URLs)
	out.tags = compactLower(in.Tags)
	for _, f := range in.Fields {
		field, value, ok := importField(f)
		if !ok {
			continue
		}
		key := fieldKey(field.Section, field.Name)
		out.values[key] = value
		if !hasField(out.fields, field.Section, field.Name) {
			out.fields = append(out.fields, field)
		}
	}
	out.notes = in.Notes
	return out, nil
}

// importCreateTx writes one item as new inside the import's transaction: its password history
// becomes the earlier versions (the oldest first, the values the history gives), the item's own
// values the current one, and the attachments ride with the current version. Tags are policy, so
// they land only when the caller may manage the collection. The bytes go back so the import can
// commit the items head once for the whole batch.
func (s *Service) importCreateTx(tx kv.Tx, who Actor, col Collection, in importer.ItemImport, levels importLevels, opts ImportOptions) (Item, []byte, error) {
	item, err := checkImportedItem(in, levels)
	if err != nil {
		return Item{}, nil, err
	}
	if s.rotatingNow(col.ID) {
		return Item{}, nil, fmt.Errorf("%w: the collection's key is being rotated; try again shortly", ErrConflict)
	}
	maxSize := s.attachmentMax()
	for _, a := range in.Attachments {
		if int64(len(a.Data)) > maxSize {
			return Item{}, nil, fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrAttachment, a.Name, len(a.Data), maxSize)
		}
	}
	id, err := newID()
	if err != nil {
		return Item{}, nil, err
	}
	now := s.now()
	tags := item.tags
	if !s.mayManageTx(tx, who, col.ID) {
		tags = nil
	}
	row := Item{ID: id, Collection: col.ID, Title: item.title, Category: item.category, URLs: item.urls,
		Tags: tags, Level: item.level, State: item.state, Source: item.source,
		Created: now, CreatedBy: who.Name}
	reason := opts.Reason
	if reason == "" {
		reason = "import from " + batchSource(in.Source)
	}
	ck, err := s.collectionKeyByVersionTx(tx, col.ID, col.KeyVersion)
	if err != nil {
		return Item{}, nil, err
	}
	defer clear(ck)
	version := int64(0)
	write := func(values map[string]string, at time.Time, why string) error {
		version++
		details := Details{Values: values, Notes: item.notes, Fields: item.fields}
		plain, err := json.Marshal(details)
		if err != nil {
			return err
		}
		ver := ItemVersion{Item: id, Version: version, Fields: item.fields, KeyVersion: col.KeyVersion,
			Actor: who.Name, Via: who.Via, At: at, Reason: why}
		ver.CT, err = wrap(ck, plain, itemAAD(id, version, col.ID, col.KeyVersion))
		if err != nil {
			return err
		}
		return putVersionTx(tx, ver)
	}
	for _, h := range in.History {
		values := map[string]string{}
		for k, v := range item.values {
			values[k] = v
		}
		for _, f := range item.fields {
			if f.Purpose == PurposePassword {
				values[fieldKey(f.Section, f.Name)] = h.Value
			}
		}
		if err := write(values, h.At, "before the import: the password history"); err != nil {
			return Item{}, nil, err
		}
	}
	if err := write(item.values, now, reason); err != nil {
		return Item{}, nil, err
	}
	for _, a := range in.Attachments {
		if strings.TrimSpace(a.Name) == "" {
			continue
		}
		ct, err := wrap(ck, a.Data, attachmentAAD(id, version, col.ID, col.KeyVersion, a.Name))
		if err != nil {
			return Item{}, nil, err
		}
		att := Attachment{Item: id, Version: version, Name: a.Name, Size: int64(len(a.Data)), CT: ct}
		b, err := json.Marshal(att)
		if err != nil {
			return Item{}, nil, err
		}
		if err := tx.Put(bucketFiles, attachmentRowKey(id, version, a.Name), b); err != nil {
			return Item{}, nil, err
		}
	}
	if len(in.Attachments) > 0 {
		names := make([]string, 0, len(in.Attachments))
		for _, a := range in.Attachments {
			if strings.TrimSpace(a.Name) != "" {
				names = append(names, a.Name)
			}
		}
		plain, err := json.Marshal(Details{Values: item.values, Notes: item.notes, Attachments: names, Fields: item.fields})
		if err != nil {
			return Item{}, nil, err
		}
		ver, ok := getVersionTx(tx, id, version)
		if !ok {
			return Item{}, nil, fmt.Errorf("%w: the version just written is missing", ErrNotFound)
		}
		ver.CT, err = wrap(ck, plain, itemAAD(id, version, col.ID, col.KeyVersion))
		if err != nil {
			return Item{}, nil, err
		}
		if err := putVersionTx(tx, ver); err != nil {
			return Item{}, nil, err
		}
	}
	row.Version = version
	b, err := s.putItemBytesTx(tx, row)
	if err != nil {
		return Item{}, nil, err
	}
	s.pruneVersions(tx, id, version)
	return row, b, nil
}

// batchSource keeps the source marker short for the reason ("1password" out of "1password:UUID").
func batchSource(source string) string {
	if source == "" {
		return "an import"
	}
	if name, _, ok := strings.Cut(source, ":"); ok {
		return name
	}
	return source
}

// importNewVersionTx folds one imported item into an item that is already there, as its next
// version, inside the import's transaction: the places and the tags join together, an imported
// field's value replaces the field's own, fields the item never had join it, and the attachments
// ride with the new version. Field types come from the ciphertext, never the plaintext overview.
// The imported history stays out: the item keeps its own.
func (s *Service) importNewVersionTx(tx kv.Tx, who Actor, itemID string, in importer.ItemImport, opts ImportOptions) (Item, []byte, error) {
	item, err := checkImportedItem(in, importLevels{})
	if err != nil {
		return Item{}, nil, err
	}
	maxSize := s.attachmentMax()
	for _, a := range in.Attachments {
		if int64(len(a.Data)) > maxSize {
			return Item{}, nil, fmt.Errorf("%w: %s is %d bytes, the limit is %d", ErrAttachment, a.Name, len(a.Data), maxSize)
		}
	}
	now := s.now()
	reason := opts.Reason
	if reason == "" {
		reason = "import from " + batchSource(in.Source) + ": a newer value"
	}
	row, colRow, ok := s.itemTx(tx, itemID)
	if !ok {
		return Item{}, nil, fmt.Errorf("%w: no item %q", ErrNotFound, itemID)
	}
	cur, ok := getVersionTx(tx, row.ID, row.Version)
	if !ok {
		return Item{}, nil, fmt.Errorf("%w: the item's current version is missing", ErrNotFound)
	}
	d, err := s.decryptDetailsTx(tx, colRow, cur, row.ID, row.Version)
	if err != nil {
		return Item{}, nil, fmt.Errorf("the item's current version could not be opened: %w", err)
	}
	ck, err := s.collectionKeyByVersionTx(tx, colRow.ID, colRow.KeyVersion)
	if err != nil {
		return Item{}, nil, err
	}
	defer clear(ck)
	if s.rotatingNow(colRow.ID) {
		return Item{}, nil, fmt.Errorf("%w: the collection's key is being rotated; try again shortly", ErrConflict)
	}
	if row.State == StateTrash {
		return Item{}, nil, fmt.Errorf("%w: a trashed item is not updated by an import", ErrNotFound)
	}
	row.URLs = compact(append(row.URLs, item.urls...))
	if s.mayManageTx(tx, who, colRow.ID) {
		row.Tags = compactLower(append(row.Tags, item.tags...))
	}
	if row.Category == "" && item.category != "" {
		row.Category = item.category
	}
	if row.Source == "" && item.source != "" {
		row.Source = item.source
	}
	fields := d.Fields
	values := map[string]string{}
	for k, v := range d.Values {
		values[k] = v
	}
	for _, f := range item.fields {
		key := fieldKey(f.Section, f.Name)
		if item.values[key] == "" {
			continue // an empty import must not blank a value that is already there
		}
		values[key] = item.values[key]
		if !hasField(fields, f.Section, f.Name) {
			fields = append(fields, f)
		}
	}
	notes := d.Notes
	if item.notes != "" {
		if notes != "" {
			notes += "\n"
		}
		notes += item.notes
	}
	attachments := d.Attachments
	next := row.Version + 1
	ver := ItemVersion{Item: row.ID, Version: next, Fields: fields, KeyVersion: colRow.KeyVersion,
		Actor: who.Name, Via: who.Via, At: now, Reason: reason}
	plain, err := json.Marshal(Details{Values: values, Notes: notes, Attachments: attachments, Fields: fields})
	if err != nil {
		return Item{}, nil, err
	}
	ver.CT, err = wrap(ck, plain, itemAAD(row.ID, next, colRow.ID, colRow.KeyVersion))
	if err != nil {
		return Item{}, nil, err
	}
	if err := putVersionTx(tx, ver); err != nil {
		return Item{}, nil, err
	}
	names := attachments
	for _, a := range in.Attachments {
		if strings.TrimSpace(a.Name) == "" {
			continue
		}
		ct, err := wrap(ck, a.Data, attachmentAAD(row.ID, next, colRow.ID, colRow.KeyVersion, a.Name))
		if err != nil {
			return Item{}, nil, err
		}
		att := Attachment{Item: row.ID, Version: next, Name: a.Name, Size: int64(len(a.Data)), CT: ct}
		b, err := json.Marshal(att)
		if err != nil {
			return Item{}, nil, err
		}
		if err := tx.Put(bucketFiles, attachmentRowKey(row.ID, next, a.Name), b); err != nil {
			return Item{}, nil, err
		}
		if !containsString(names, a.Name) {
			names = append(names, a.Name)
		}
	}
	if len(names) != len(attachments) {
		plain, err := json.Marshal(Details{Values: values, Notes: notes, Attachments: names, Fields: fields})
		if err != nil {
			return Item{}, nil, err
		}
		ver.CT, err = wrap(ck, plain, itemAAD(row.ID, next, colRow.ID, colRow.KeyVersion))
		if err != nil {
			return Item{}, nil, err
		}
		if err := putVersionTx(tx, ver); err != nil {
			return Item{}, nil, err
		}
	}
	row.Version = next
	b, err := s.putItemBytesTx(tx, row)
	if err != nil {
		return Item{}, nil, err
	}
	s.pruneVersions(tx, row.ID, next)
	return row, b, nil
}
