// Package secrets is Beacon's vault: collections of items whose values it keeps for the machines
// and agents that run servers, so that nobody has to hold a key. Every item's details — field
// values, user names, notes, one-time-passcode seeds, attachments — are encrypted under that
// collection's key; the overview (title, category, tags, URLs, field names) stays readable, for
// the policy evaluation and the search that need it. The root key (RK) wraps each collection key
// (CK); HKDF of RK gives the signature key (SK) that signs every policy row, so someone holding
// the database file alone can neither read a value nor widen a permission (a row that fails its
// signature is treated as absent and reported — the design's third invariant).
//
// People hold four permissions per collection — describe, read, edit, manage — and an owner holds
// manage on every collection. Reading a value needs the account password re-entered, within a
// window the settings give, and every view is recorded before the value is returned. Agents reach
// items only through the grants a later package brings; until then the vault answers an agent with
// nothing.
package secrets

import (
	"time"
)

// Approval levels, weakest first; Never is the strongest and is never given to an agent — that is
// what the level means, and the vault refuses an agent a value outright until grants arrive.
const (
	LevelAuto   = "auto"
	LevelReview = "review"
	LevelPerson = "person"
	LevelNever  = "never"
)

// Levels lists the approval levels, weakest first.
var Levels = []string{LevelAuto, LevelReview, LevelPerson, LevelNever}

// LevelStrictness numbers the levels for taking the stricter of two.
var LevelStrictness = map[string]int{LevelAuto: 0, LevelReview: 1, LevelPerson: 2, LevelNever: 3}

// StrictestLevel is the stricter of two levels.
func StrictestLevel(a, b string) string {
	if LevelStrictness[a] >= LevelStrictness[b] {
		return a
	}
	return b
}

// ValidLevel reports whether s is an approval level.
func ValidLevel(s string) bool { _, ok := LevelStrictness[s]; return ok }

// Item states: normal, archived (out of the search), trash (kept for the retention, restorable).
const (
	StateNormal   = "normal"
	StateArchived = "archived"
	StateTrash    = "trash"
)

// Field types, as the importers and the console show them; Concealed is a value the vault hides
// until it is revealed. Purpose says what a field is for: a user name, a password, notes.
const (
	FieldConcealed = "concealed"
	FieldString    = "string"
	FieldEmail     = "email"
	FieldURL       = "url"
	FieldOTP       = "otp"
	FieldDate      = "date"
	FieldFile      = "file"

	PurposeUsername = "username"
	PurposePassword = "password"
	PurposeNotes    = "notes"
)

// FieldTypes and Purposes list what a field may be.
var (
	FieldTypes = []string{FieldConcealed, FieldString, FieldEmail, FieldURL, FieldOTP, FieldDate, FieldFile}
	Purposes   = []string{PurposeUsername, PurposePassword, PurposeNotes}
)

// Collection is a library of items (1Password's vault): its default approval level, the people and
// roles allowed in, and which version of its key is in force. KeyVersion only moves when the
// collection key is rotated; the wrapped key of every version is kept, so old item versions stay
// readable.
type Collection struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Level       string    `json:"level"` // the default approval level of the collection
	KeyVersion  int64     `json:"key_version"`
	Created     time.Time `json:"created"`
	CreatedBy   string    `json:"created_by,omitempty"`
}

// Item is one secret as the overview shows it: everything here is readable by whoever may describe
// the collection. The values live in the version's ciphertext. Level empty inherits: the strictest
// of the collection's default and every tag's level. ActionsAllowed, when set, narrows what may be
// done with the item to the actions named (use only, most often).
type Item struct {
	ID         string    `json:"id"`
	Collection string    `json:"collection"` // the collection's ID
	Title      string    `json:"title"`
	Category   string    `json:"category,omitempty"`
	URLs       []string  `json:"urls,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	Level      string    `json:"level,omitempty"` // empty: inherit
	Actions    []string  `json:"actions,omitempty"`
	State      string    `json:"state"`
	Version    int64     `json:"version"`          // the current version
	Source     string    `json:"source,omitempty"` // 1password:<uuid>, chrome
	RotateDays int       `json:"rotate_days,omitempty"`
	Machines   []string  `json:"machines,omitempty"` // bound machines, for use
	Hosts      []string  `json:"hosts,omitempty"`    // bound https hosts, for use
	Created    time.Time `json:"created"`
	CreatedBy  string    `json:"created_by,omitempty"`
	TrashedAt  time.Time `json:"trashed_at,omitzero"`
}

// Field is one field of an item as the version lists it: where it sits (the section), its name,
// its type and its purpose. The value is not here — it is in the version's ciphertext, keyed by
// section and name.
type Field struct {
	Section string `json:"section,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
}

// Details is what a version's ciphertext holds: every field value (keyed "section\x00name", or
// "name" with no section), the notes, and the attachments' names (their bytes are encrypted each
// on its own, so a large one does not make the ciphertext unwieldy).
type Details struct {
	Values      map[string]string `json:"values,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Attachments []string          `json:"attachments,omitempty"`
	Fields      []Field           `json:"fields,omitempty"` // inside the ciphertext, so a type cannot be swapped in the clear
}

// ItemVersion is one version of an item: the ciphertext of its details under the collection key
// of KeyVersion, the fields in the clear (names, types, purposes — what the search and the policy
// need), and who made it, when and why.
type ItemVersion struct {
	Item       string    `json:"item"`
	Version    int64     `json:"version"`
	Fields     []Field   `json:"fields,omitempty"`
	CT         []byte    `json:"ct"` // AES-256-GCM: nonce ‖ ciphertext ‖ tag, AAD binds it to this row
	KeyVersion int64     `json:"key_version"`
	Actor      string    `json:"actor"`
	Via        string    `json:"via"`
	At         time.Time `json:"at"`
	Reason     string    `json:"reason,omitempty"`
}

// Attachment is one file kept with an item version: its name and size in the clear, its bytes
// encrypted under the same collection key.
type Attachment struct {
	Item    string `json:"item"`
	Version int64  `json:"version"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	CT      []byte `json:"ct"`
}

// Tag is a layered, lower-case label that items and agents carry ("env/prod", "service/dns"); its
// level, when set, makes every item wearing it at least that strict.
type Tag struct {
	Name        string    `json:"name"` // lower-case, layered with /
	Level       string    `json:"level,omitempty"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
}

// Permission levels for people over a collection, weakest first. Describe reads the overview; read
// reveals values (with the password re-entered); edit adds and changes items; manage changes
// policy — the collection's level, its permissions, items' tags and levels.
const (
	PermDescribe = "describe"
	PermRead     = "read"
	PermEdit     = "edit"
	PermManage   = "manage"
)

// Perms lists the permission levels, weakest first.
var Perms = []string{PermDescribe, PermRead, PermEdit, PermManage}

// permRank numbers the permissions for comparing them.
var permRank = map[string]int{PermDescribe: 0, PermRead: 1, PermEdit: 2, PermManage: 3}

// ValidPerm reports whether s is a permission level.
func ValidPerm(s string) bool { _, ok := permRank[s]; return ok }

// PermAtLeast reports whether have is at least want.
func PermAtLeast(have, want string) bool { return permRank[have] >= permRank[want] }

// Subject names who a permission row is for: "user:NAME" or "role:viewer|oncall|operator|owner".
// An owner holds manage on every collection without a row.
const permSubjectPrefix = "user:"

// Permission is one row of a collection's people permissions, signed like every policy row.
type Permission struct {
	Collection string    `json:"collection"` // the collection's ID
	Subject    string    `json:"subject"`    // user:NAME or role:NAME
	Perm       string    `json:"perm"`
	Actor      string    `json:"actor"` // who set it
	Via        string    `json:"via"`
	At         time.Time `json:"at"`
	Reason     string    `json:"reason,omitempty"`
	Sig        []byte    `json:"sig"`
}
