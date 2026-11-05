// Package importer parses the exports other keepers make — 1Password's 1PUX and CSV, the op
// command line, Chrome's CSV, Bitwarden's JSON and a plain CSV with the columns named — into a
// batch the vault imports. Parsing is local: it happens in the command line's process or in the
// console's memory, so a plaintext export travels once, over the socket or TLS, and is never
// written anywhere by Beacon. Nothing here talks to an account: the op connector runs the op
// binary the caller has, and the tests stand a stub in for it.
//
// The batch carries values, and only the batch does: the dry-run strips them (ForDryRun keeps the
// user names, which the conflict rule needs, and drops everything else), and no report the package
// shapes ever echoes a value, a note or an attachment's bytes.
package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

// The sources, as the reports and the source tags name them. Source1Password covers 1PUX, the
// 1Password CSV and the op connector: they are one keeper, and its vaults arrive keyed for the
// import levels ("1password", with the personal vaults as "1password:personal" and
// "1password:private", which the default levels start at person).
const (
	Source1Password = "1password"
	SourceChrome    = "chrome"
	SourceBitwarden = "bitwarden"
	SourceCSV       = "csv"
)

// SourceTag is the tag every item of a source wears, so what came from where stays visible.
func SourceTag(source string) string { return "source/" + source }

// Field types, the vault's own vocabulary (the vault checks them again on the way in).
const (
	TypeConcealed = "concealed"
	TypeString    = "string"
	TypeEmail     = "email"
	TypeURL       = "url"
	TypeOTP       = "otp"
	TypeDate      = "date"
	TypeFile      = "file"
)

// Field purposes, as the vault names them.
const (
	PurposeUsername = "username"
	PurposePassword = "password"
	PurposeNotes    = "notes"
)

// Item states, as the vault names them.
const (
	StateNormal   = "normal"
	StateArchived = "archived"
)

// Categories, the vault's machine names. The identity and finance ones — CreditCard, BankAccount,
// Identity, Passport, DriverLicense, SocialSecurityNumber, MedicalRecord, Membership, Reward —
// are the categories the default import levels start at never; the rest take their collection's
// level. The names are the keys of import.levels.by_category.
const (
	CatLogin           = "login"
	CatPassword        = "password"
	CatAPICredential   = "api_credential" //nolint:gosec // G101: a category's name, not a credential
	CatDatabase        = "database"
	CatServer          = "server"
	CatSSHKey          = "ssh_key"
	CatSecureNote      = "secure_note"
	CatEmailAccount    = "email_account"
	CatWirelessRouter  = "wireless_router"
	CatSoftwareLicense = "software_license"

	CatCreditCard           = "credit_card"
	CatBankAccount          = "bank_account"
	CatIdentity             = "identity"
	CatPassport             = "passport"
	CatDriverLicense        = "driver_license"
	CatSocialSecurityNumber = "social_security_number"
	CatMedicalRecord        = "medical_record"
	CatMembership           = "membership"
	CatReward               = "reward_program"
)

// CategoryTitles names the categories for people (the reports and the console).
var CategoryTitles = map[string]string{ //nolint:gosec // G101: categories' names, not credentials
	CatLogin: "Login", CatPassword: "Password", CatAPICredential: "API Credential", CatDatabase: "Database",
	CatServer: "Server", CatSSHKey: "SSH Key", CatSecureNote: "Secure Note", CatEmailAccount: "Email Account",
	CatWirelessRouter: "Wireless Router", CatSoftwareLicense: "Software License", CatCreditCard: "Credit Card",
	CatBankAccount: "Bank Account", CatIdentity: "Identity", CatPassport: "Passport", CatDriverLicense: "Driver License",
	CatSocialSecurityNumber: "Social Security Number", CatMedicalRecord: "Medical Record", CatMembership: "Membership",
	CatReward: "Reward Program",
}

// Categories lists the category names, alphabetical.
func Categories() []string {
	out := make([]string, 0, len(CategoryTitles))
	for c := range CategoryTitles {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// FieldImport is one field as it is parsed: where it sits, its name, its type and purpose (the
// vault's vocabulary), and its value. A value here is a plaintext value; it travels with the batch
// and is never echoed by any report.
type FieldImport struct {
	Section string `json:"section,omitempty"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
	Value   string `json:"value"`
}

// AttachmentImport is one attachment: its name and its bytes.
type AttachmentImport struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// HistoryImport is one older value of the password (1Password's passwordHistory): the value and
// when it was last used. The oldest comes first; the vault writes each as an earlier version of
// the item, the current values as the newest one.
type HistoryImport struct {
	Value string    `json:"value"`
	At    time.Time `json:"at"`
}

// ItemImport is one item as it is parsed: the overview's parts, the fields with their values, the
// notes, the password history and the attachments. Source is the vault's own marker
// ("1password:<uuid>", "chrome", …); State is normal or archived (1Password's archived items).
type ItemImport struct {
	Title       string             `json:"title"`
	Category    string             `json:"category,omitempty"`
	URLs        []string           `json:"urls,omitempty"`
	Tags        []string           `json:"tags,omitempty"`
	State       string             `json:"state,omitempty"`
	Source      string             `json:"source,omitempty"`
	Fields      []FieldImport      `json:"fields,omitempty"`
	Notes       string             `json:"notes,omitempty"`
	History     []HistoryImport    `json:"history,omitempty"`
	Attachments []AttachmentImport `json:"attachments,omitempty"`
}

// CollectionImport is one target collection: what the source called it (a 1Password vault, the
// named collection of a CSV), and its items. LevelKey is the key the import levels are read by:
// "1password:personal" for a personal vault, "chrome" for Chrome, and so on.
type CollectionImport struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	LevelKey    string       `json:"level_key,omitempty"`
	Items       []ItemImport `json:"items"`
}

// Batch is a parsed import: the source it came from and the collections it holds. A collection a
// source does not separate (Chrome's rows, a plain CSV) comes as one, named by the caller.
type Batch struct {
	Source      string             `json:"source"`
	Collections []CollectionImport `json:"collections"`
	Merged      int                `json:"merged,omitempty"` // rows folded together (Chrome's repeated credentials)
}

// ForDryRun is the batch as a dry-run needs it: counts, shapes and the user names — which the
// conflict rule turns over — and no other value. Values, notes, history values and attachment
// bytes are dropped; the field list stays, so the report can count the one-time-passcode fields.
func (b Batch) ForDryRun() Batch {
	out := Batch{Source: b.Source, Merged: b.Merged}
	for _, col := range b.Collections {
		c := CollectionImport{Name: col.Name, Description: col.Description, LevelKey: col.LevelKey}
		for _, it := range col.Items {
			n := ItemImport{Title: it.Title, Category: it.Category, URLs: it.URLs, Tags: it.Tags,
				State: it.State, Source: it.Source}
			for _, f := range it.Fields {
				v := ""
				if f.Purpose == PurposeUsername {
					v = f.Value // the conflict rule needs it; no report echoes it
				}
				n.Fields = append(n.Fields, FieldImport{Section: f.Section, Name: f.Name, Type: f.Type, Purpose: f.Purpose, Value: v})
			}
			n.History = make([]HistoryImport, len(it.History)) // the count, not the values
			copy(n.History, it.History)
			for i := range n.History {
				n.History[i].Value = ""
			}
			for _, a := range it.Attachments {
				n.Attachments = append(n.Attachments, AttachmentImport{Name: a.Name}) // the name, never the bytes
			}
			c.Items = append(c.Items, n)
		}
		out.Collections = append(out.Collections, c)
	}
	return out
}

// IntoCollection folds the batch into one named collection (what Chrome, Bitwarden and a plain CSV
// import into). It keeps the source's level key when there was one.
func (b Batch) IntoCollection(name string) Batch {
	key := ""
	for _, col := range b.Collections {
		if col.LevelKey != "" {
			key = col.LevelKey
			break
		}
	}
	out := Batch{Source: b.Source, Merged: b.Merged, Collections: []CollectionImport{{Name: name, LevelKey: key}}}
	for _, col := range b.Collections {
		out.Collections[0].Items = append(out.Collections[0].Items, col.Items...)
	}
	return out
}

// NormalizeURL is the shape two URLs must share to count as the same place: the scheme and the
// host, lower-case, the default port dropped, the leading www. dropped, the user info dropped, no
// query and no fragment. A string that is not a URL falls back to its trimmed lower-case self, so
// odd inputs still compare consistently. Chrome's "android://<hash>@com.app" style URLs keep
// their app's host, which is the part that says where the credential lives.
func NormalizeURL(in string) string {
	s := strings.TrimSpace(in)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.ToLower(s)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host = strings.TrimPrefix(host, "www.")
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// hostOf a normalised URL, for the Chrome merge rule's "different domains".
func hostOf(normalized string) string {
	_, host, ok := strings.Cut(normalized, "://")
	if !ok {
		host = normalized
	}
	return host
}

// ---- CSV reading

// readCSV reads the rows of a CSV, tolerating a UTF-8 BOM and requiring a header. Every keeper
// writes its CSV differently enough (field counts, quoting) that the standard reader with fields
// as they come is the honest parser.
func readCSV(r io.Reader) ([][]string, error) {
	all, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	all = bytes.TrimPrefix(all, []byte{0xEF, 0xBB, 0xBF})
	rd := csv.NewReader(bytes.NewReader(all))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	rows, err := rd.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("the CSV could not be read: %w", err)
	}
	if len(rows) < 1 {
		return nil, fmt.Errorf("the CSV is empty")
	}
	return rows, nil
}

// headerIndex maps a header row to column numbers: each name is normalised (lower-case, spaces and
// underscores out) and looked up among the candidates. The first column a candidate set finds
// wins; columns nothing claims are ignored. The answer says which of the wanted names were found,
// so callers can require the few they cannot do without.
func headerIndex(header []string, wanted map[string][]string) map[string]int {
	out := map[string]int{}
	taken := map[int]bool{}
	for name, candidates := range wanted {
		for _, c := range candidates {
			needle := squash(c)
			for i, h := range header {
				if taken[i] || squash(h) != needle {
					continue
				}
				out[name] = i
				taken[i] = true
				break
			}
		}
		if _, ok := out[name]; !ok {
			for i, h := range header {
				if taken[i] {
					continue
				}
				for _, c := range candidates {
					if squash(h) == squash(c) {
						out[name] = i
						taken[i] = true
						break
					}
				}
				if _, ok := out[name]; ok {
					break
				}
			}
		}
	}
	return out
}

// squash makes a header name comparable: lower-case, spaces, underscores and hyphens out.
func squash(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

// cleanTags lower-cases, trims, sorts and drops the empties, keeping the source's own shape
// (1Password's tags are layered with / already).
func cleanTags(in []string, extra ...string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, t := range in {
		add(t)
	}
	for _, t := range extra {
		add(t)
	}
	sort.Strings(out)
	return out
}

// dedupeURLs keeps each URL once, in order, dropping the empties and the exact repeats.
func dedupeURLs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// titleOf makes a title an item can wear: trimmed, at most 128 characters, and never empty (the
// caller's fallback name comes in already). The reference syntax addresses items by ID when a
// title has other shapes, which a real export's titles often do.
func titleOf(in, fallback string) string {
	t := strings.TrimSpace(in)
	if t == "" {
		t = strings.TrimSpace(fallback)
	}
	if t == "" {
		return "Untitled"
	}
	if len(t) > 128 {
		t = t[:128]
	}
	return t
}
