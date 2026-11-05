package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// The op command line's connector: it runs the op binary the caller has signed in with — `op vault
// list`, `op item list --vault`, `op item get --reveal --format json` — and maps what comes back
// as the 1PUX import does. Beacon never holds an account or a token of 1Password's: the connector
// only names the commands, and the tests stand a stub in for the binary. Each item costs two
// requests (a list and a get), so an account over about three hundred items is cheaper as a 1PUX
// export; the report says which it was.
//
// https://developer.1password.com/docs/cli/

// OpRunner runs one op command and answers its output. The real one runs the binary; a test's stub
// answers without touching anything.
type OpRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner runs the op binary on the caller's PATH.
type ExecRunner struct{}

// Run runs op with the arguments given.
func (ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "op", args...).Output() //nolint:gosec // G204: the arguments are the connector's own, never caller text
}

// OpConnector walks one account's vaults over the runner given.
type OpConnector struct {
	Run OpRunner
}

// opVault is `op vault list --format json`'s row.
type opVault struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // read/write share of the account; the personal vault is named, not typed
	Attr struct {
		Name string `json:"name"`
		Type string `json:"type"` // P, E or U where the build gives it
	} `json:"attributes"`
}

// opField is one field of op's item answer.
type opField struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"` // USERNAME or PASSWORD
	Section struct {
		ID string `json:"id"`
	} `json:"section"`
}

// opItem is `op item list --format json`'s row and, with the fields, `op item get --reveal`'s
// answer; the shapes are the same where it matters.
type opItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Vault    struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"vault"`
	State    string     `json:"state"` // ARCHIVED where the build gives it
	Sections []struct { //nolint:revive // op's own shape
		ID    string `json:"id"`
		Label string `json:"label"`
	} `json:"sections"`
	Fields []*opField `json:"fields"`
}

// ParseOp reads the account over the runner: every vault (or the ones named), every item of each,
// each item revealed. The mapping is the 1PUX import's: the designated fields by purpose, the
// sections' fields by type, the notes and the tags; op shows an item's files only as names, so
// attachments do not come along this way (a 1PUX export brings them).
func (c OpConnector) ParseOp(ctx context.Context, vaults []string) (Batch, error) {
	if c.Run == nil {
		c.Run = ExecRunner{}
	}
	out, err := c.Run.Run(ctx, "vault", "list", "--format", "json")
	if err != nil {
		return Batch{}, fmt.Errorf("op vault list failed (is op signed in?): %w", err)
	}
	var vl []opVault
	if err := json.Unmarshal(out, &vl); err != nil {
		return Batch{}, fmt.Errorf("op vault list did not answer a list: %w", err)
	}
	want := map[string]bool{}
	for _, v := range vaults {
		want[strings.ToLower(strings.TrimSpace(v))] = true
	}
	batch := Batch{Source: Source1Password}
	for _, v := range vl {
		name := v.Name
		if name == "" {
			name = v.Attr.Name
		}
		if len(want) > 0 && !want[strings.ToLower(name)] && !want[strings.ToLower(v.ID)] {
			continue
		}
		col, err := c.parseVault(ctx, v, name)
		if err != nil {
			return Batch{}, err
		}
		batch.Collections = append(batch.Collections, col)
	}
	sort.Slice(batch.Collections, func(i, j int) bool { return batch.Collections[i].Name < batch.Collections[j].Name })
	return batch, nil
}

// parseVault reads one vault: its items' list, then each item revealed.
func (c OpConnector) parseVault(ctx context.Context, v opVault, name string) (CollectionImport, error) {
	col := CollectionImport{Name: name, LevelKey: vaultLevelKey(name, v.Attr.Type)}
	args := []string{"item", "list", "--vault", v.ID, "--format", "json"}
	out, err := c.Run.Run(ctx, args...)
	if err != nil {
		return CollectionImport{}, fmt.Errorf("op item list failed for vault %s: %w", name, err)
	}
	var list []opItem
	if err := json.Unmarshal(out, &list); err != nil {
		return CollectionImport{}, fmt.Errorf("op item list did not answer a list for vault %s: %w", name, err)
	}
	for _, it := range list {
		full, err := c.Run.Run(ctx, "item", "get", it.ID, "--vault", v.ID, "--reveal", "--format", "json")
		if err != nil {
			return CollectionImport{}, fmt.Errorf("op item get failed for %s: %w", it.Title, err)
		}
		var item opItem
		if err := json.Unmarshal(full, &item); err != nil {
			return CollectionImport{}, fmt.Errorf("op item get did not answer an item for %s: %w", it.Title, err)
		}
		if item.Title == "" {
			item.Title = it.Title
		}
		if item.State == "" {
			item.State = it.State
		}
		col.Items = append(col.Items, parseOpItem(item))
	}
	return col, nil
}

// parseOpItem maps one revealed item, as the 1PUX import maps its own.
func parseOpItem(it opItem) ItemImport {
	out := ItemImport{Title: titleOf(it.Title, ""), Source: Source1Password + ":" + it.ID}
	out.Category = opCategory(it.Category)
	if strings.EqualFold(strings.TrimSpace(it.State), "ARCHIVED") {
		out.State = StateArchived
	}
	sections := map[string]string{} // id → label
	for _, s := range it.Sections {
		sections[s.ID] = s.Label
	}
	urls := []string{}
	for _, f := range it.Fields {
		if f == nil || strings.EqualFold(f.ID, "notes") {
			continue // op carries the notes as a field of their own; they are the item's notes
		}
		name := strings.TrimSpace(f.Label)
		if name == "" {
			name = strings.TrimSpace(f.ID)
		}
		value := f.Value
		section := sections[f.Section.ID]
		switch strings.ToUpper(f.Purpose) {
		case "USERNAME":
			out.Fields = append(out.Fields, FieldImport{Name: name, Type: TypeString, Purpose: PurposeUsername, Value: value})
			continue
		case "PASSWORD":
			out.Fields = append(out.Fields, FieldImport{Name: name, Type: TypeConcealed, Purpose: PurposePassword, Value: value})
			continue
		}
		if value == "" {
			continue
		}
		fi := FieldImport{Section: section, Name: name}
		switch strings.ToUpper(f.Type) {
		case "CONCEALED":
			fi.Type, fi.Value = TypeConcealed, value
		case "OTP":
			fi.Type, fi.Value = TypeOTP, value
		case "EMAIL":
			fi.Type, fi.Value = TypeEmail, value
		case "URL":
			fi.Type, fi.Value = TypeURL, value
			urls = append(urls, value)
		case "DATE", "MONTH_YEAR":
			fi.Type = TypeDate
			fi.Value = compactDate(value)
		case "SSH_KEY":
			// the key itself stays concealed; what a report shows is only that it is there
			fi.Type, fi.Value = TypeConcealed, value
		default: // STRING, PHONE, REFERENCES, MENU, GENDER and the rest
			fi.Type, fi.Value = TypeString, value
		}
		out.Fields = append(out.Fields, fi)
	}
	out.URLs = dedupeURLs(urls)
	out.Tags = cleanTags(nil, SourceTag(Source1Password))
	if n := notesOfOpFields(it.Fields); n != "" {
		out.Notes = n
	}
	return out
}

// notesOfOpFields gathers the notes field, which op carries as a field of its own.
func notesOfOpFields(fields []*opField) string {
	for _, f := range fields {
		if f != nil && strings.EqualFold(f.ID, "notes") {
			return f.Value
		}
	}
	return ""
}

// opCategory maps op's category name to the vault's: op answers lower-case, underscored names.
func opCategory(in string) string {
	c := strings.ToLower(strings.TrimSpace(in))
	c = strings.ReplaceAll(c, "-", "_")
	c = strings.ReplaceAll(c, " ", "_")
	switch c {
	case CatLogin:
		return CatLogin
	case "password":
		return CatPassword
	case "credit_card":
		return CatCreditCard
	case "secure_note":
		return CatSecureNote
	case "identity":
		return CatIdentity
	case "api_credential":
		return CatAPICredential
	case "database":
		return CatDatabase
	case "driver_license", "driving_licence":
		return CatDriverLicense
	case "membership":
		return CatMembership
	case "outdoor_license", "outdoor_licence":
		return CatSecureNote
	case "passport":
		return CatPassport
	case "rewards", "reward_program":
		return CatReward
	case "social_security_number":
		return CatSocialSecurityNumber
	case "software_license":
		return CatSoftwareLicense
	case "bank_account":
		return CatBankAccount
	case "email_account":
		return CatEmailAccount
	case "medical_record":
		return CatMedicalRecord
	case "wireless_router":
		return CatWirelessRouter
	case "server":
		return CatServer
	case "ssh_key":
		return CatSSHKey
	case "document":
		return CatSecureNote
	default:
		return CatSecureNote
	}
}

// compactDate keeps a date-shaped value's date: the first ten characters when it looks like ISO,
// else the value as it came.
func compactDate(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 10 && v[4] == '-' && v[7] == '-' {
		return v[:10]
	}
	return v
}
