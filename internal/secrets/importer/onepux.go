package importer

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// The 1PUX export: a zip holding export.attributes, export.data (accounts → vaults → items) and a
// files/ folder with the attachments, named by their document id. The format is 1Password's own
// (1Password Unencrypted Export); the field shapes here are the export's, as the format's
// documentation and Bitwarden's importer types give them. Every vault — personal (P), shared (E)
// and user-created (U) — becomes a collection; an item's categoryUuid says its category, its
// overview carries the title, URLs and tags, its details the fields: loginFields by their
// designation (username, password), sections' fields by their value's type, notesPlain the notes,
// passwordHistory the older values, documentAttributes the attachment in files/.
//
// https://support.1password.com/1pux-format/

// categoryUUIDs are 1Password's category codes, to the vault's category names.
var categoryUUIDs = map[string]string{
	"001": CatLogin, "002": CatCreditCard, "003": CatSecureNote, "004": CatIdentity, "005": CatPassword,
	"006": CatSecureNote, // a document: kept as a secure note with its file attached
	"100": CatSoftwareLicense, "101": CatBankAccount, "102": CatDatabase, "103": CatDriverLicense,
	"104": CatSecureNote, // an outdoor licence, for which the vault has no category
	"105": CatMembership, "106": CatPassport, "107": CatReward, "108": CatSocialSecurityNumber,
	"109": CatWirelessRouter, "110": CatServer, "111": CatEmailAccount, "112": CatAPICredential,
	"113": CatMedicalRecord, "114": CatSSHKey,
}

// onepux is export.data's shape.
type onepux struct {
	Accounts []*struct {
		Vaults []*struct {
			Attrs struct {
				UUID string `json:"uuid"`
				Name string `json:"name"`
				Desc string `json:"desc"`
				Type string `json:"type"` // P personal, E everyone, U user-created
			} `json:"attrs"`
			Items []*onepuxItem `json:"items"`
		} `json:"vaults"`
	} `json:"accounts"`
}

// onepuxItem is one item: the fields the import reads.
type onepuxItem struct {
	UUID         string `json:"uuid"`
	State        string `json:"state"` // active or archived
	CategoryUUID string `json:"categoryUuid"`
	Overview     struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		URLs  []struct {
			Label string `json:"label"`
			URL   string `json:"url"`
		} `json:"urls"`
		Tags []string `json:"tags"`
	} `json:"overview"`
	Details struct {
		LoginFields []struct {
			Value       string `json:"value"`
			ID          string `json:"id"`
			Name        string `json:"name"`
			FieldType   string `json:"fieldType"`
			Designation string `json:"designation"` // username or password
		} `json:"loginFields"`
		NotesPlain   string `json:"notesPlain"`
		Password     string `json:"password"`
		DocumentAttr *struct {
			FileName string `json:"fileName"`
			ID       string `json:"documentId"`
		} `json:"documentAttributes"`
		Sections []struct {
			Title  string `json:"title"`
			Name   string `json:"name"`
			Fields []struct {
				Title string           `json:"title"`
				ID    string           `json:"id"`
				Value onepuxFieldValue `json:"value"`
			} `json:"fields"`
		} `json:"sections"`
		PasswordHistory []struct {
			Value string `json:"value"`
			Time  int64  `json:"time"` // seconds
		} `json:"passwordHistory"`
	} `json:"details"`
}

// onepuxFieldValue is a section field's value: exactly one key is set, saying the value's type.
type onepuxFieldValue struct {
	Concealed        *string        `json:"concealed,omitempty"`
	String           *string        `json:"string,omitempty"`
	Email            *string        `json:"email,omitempty"` // the address itself in 1PUX; a provider may ride along
	Phone            *string        `json:"phone,omitempty"`
	Menu             *string        `json:"menu,omitempty"`
	Gender           *string        `json:"gender,omitempty"`
	URL              *string        `json:"url,omitempty"`
	TOTP             *string        `json:"totp,omitempty"`
	Date             *int64         `json:"date,omitempty"`      // seconds
	MonthYear        *int64         `json:"monthYear,omitempty"` // YYYYMM as a number
	CreditCardType   *string        `json:"creditCardType,omitempty"`
	CreditCardNumber *string        `json:"creditCardNumber,omitempty"`
	Reference        *string        `json:"reference,omitempty"`
	Address          *onepuxAddress `json:"address,omitempty"`
	SSHKey           *onepuxSSHKey  `json:"sshKey,omitempty"`
}

// onepuxAddress is a postal address.
type onepuxAddress struct {
	Street string `json:"street"`
	City   string `json:"city"`
	State  string `json:"state"`
	ZIP    string `json:"zip"`
	// 1Password writes the country under a key its own page does not name; both spellings are read
	Country  string `json:"country"`
	Country2 string `json:"country_2"`
}

// onepuxSSHKey is an SSH key with its metadata.
type onepuxSSHKey struct {
	PrivateKey string `json:"privateKey"`
	Metadata   struct {
		PublicKey   string `json:"publicKey"`
		Fingerprint string `json:"fingerprint"`
		KeyType     string `json:"keyType"`
	} `json:"metadata"`
}

// Parse1PUX reads a 1PUX export. It never writes anything: the attachments' bytes come straight
// out of the zip into the batch. The export is read whole into memory, as a plaintext export
// must be, and nothing is kept once the batch is made.
func Parse1PUX(r io.Reader) (Batch, error) {
	all, err := io.ReadAll(io.LimitReader(r, 512<<20))
	if err != nil {
		return Batch{}, err
	}
	zr, err := zip.NewReader(&bytesReader{b: all}, int64(len(all)))
	if err != nil {
		return Batch{}, fmt.Errorf("the 1PUX file is not a zip: %w", err)
	}
	var data []byte
	files := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := f.Name
		if !strings.HasPrefix(name, "files/") && !strings.Contains(name, "/files/") && name != "export.data" && name != "export.attributes" {
			continue // custom icons and anything else: not the import's business
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		if name == "export.data" {
			data = b
			continue
		}
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:] // files/<id>___<name>, by the file's own name
		}
		files[name] = b
	}
	if len(data) == 0 {
		return Batch{}, fmt.Errorf("the 1PUX file has no export.data")
	}
	var export onepux
	if err := json.Unmarshal(data, &export); err != nil {
		return Batch{}, fmt.Errorf("the 1PUX export.data could not be read: %w", err)
	}
	out := Batch{Source: Source1Password}
	for _, acc := range export.Accounts {
		for _, v := range acc.Vaults {
			col := CollectionImport{Name: v.Attrs.Name, LevelKey: vaultLevelKey(v.Attrs.Name, v.Attrs.Type)}
			if v.Attrs.Desc != "" {
				col.Description = v.Attrs.Desc
			}
			for _, it := range v.Items {
				item := parse1PUXItem(it, files)
				if len(item.Fields) == 0 && item.Notes == "" && len(item.Attachments) == 0 {
					continue // an empty row: nothing to keep
				}
				col.Items = append(col.Items, item)
			}
			if len(col.Items) > 0 {
				out.Collections = append(out.Collections, col)
			}
		}
	}
	return out, nil
}

// vaultLevelKey is the key the import levels are read by for one vault: the personal vaults
// (1Password's Private and Personal, the type P) start at person by the default levels, every
// other vault at the source's own level.
func vaultLevelKey(name, typ string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "private", "personal":
		return Source1Password + ":private"
	}
	if strings.EqualFold(strings.TrimSpace(typ), "P") {
		return Source1Password + ":personal"
	}
	return Source1Password
}

// parse1PUXItem maps one item: the category from its UUID, the overview's title, URLs and tags,
// the loginFields by designation, the sections' fields by their value's type, the notes, the
// password history and the document attachment. The source tag marks where it came from.
func parse1PUXItem(it *onepuxItem, files map[string][]byte) ItemImport {
	out := ItemImport{Title: titleOf(it.Overview.Title, ""), Source: Source1Password + ":" + it.UUID}
	if cat, ok := categoryUUIDs[it.CategoryUUID]; ok {
		out.Category = cat
	} else {
		out.Category = CatSecureNote
	}
	if strings.EqualFold(it.State, "archived") {
		out.State = StateArchived
	}
	urls := []string{}
	if it.Overview.URL != "" {
		urls = append(urls, it.Overview.URL)
	}
	for _, u := range it.Overview.URLs {
		if u.URL != "" {
			urls = append(urls, u.URL)
		}
	}
	out.URLs = dedupeURLs(urls)
	out.Tags = cleanTags(it.Overview.Tags, SourceTag(Source1Password))

	// the login fields: the designated username and password first, then the saved form fields
	hasPassword := false
	for _, lf := range it.Details.LoginFields {
		name := strings.TrimSpace(lf.Name)
		if name == "" {
			name = strings.TrimSpace(lf.ID)
		}
		value := lf.Value
		switch strings.ToLower(lf.Designation) {
		case PurposeUsername:
			out.Fields = append(out.Fields, FieldImport{Name: "username", Type: TypeString, Purpose: PurposeUsername, Value: value})
		case PurposePassword:
			out.Fields = append(out.Fields, FieldImport{Name: "password", Type: TypeConcealed, Purpose: PurposePassword, Value: value})
			hasPassword = true
		default:
			if value == "" || name == "" {
				continue // a saved form field with nothing in it
			}
			typ := TypeString
			if lf.FieldType == "P" {
				typ = TypeConcealed
			}
			out.Fields = append(out.Fields, FieldImport{Name: name, Type: typ, Value: value})
		}
	}
	// the password on its own (the Password category keeps it there)
	if it.Details.Password != "" && !hasPassword {
		out.Fields = append(out.Fields, FieldImport{Name: "password", Type: TypeConcealed, Purpose: PurposePassword, Value: it.Details.Password})
	}
	// the sections: each field by its value's type
	for _, sec := range it.Details.Sections {
		name := strings.TrimSpace(sec.Title)
		if name == "" {
			name = strings.TrimSpace(sec.Name)
		}
		for _, f := range sec.Fields {
			label := strings.TrimSpace(f.Title)
			if label == "" {
				label = strings.TrimSpace(f.ID)
			}
			if label == "" {
				continue
			}
			for _, fi := range onepuxSectionFields(label, f.Value) {
				fi.Section = name
				out.Fields = append(out.Fields, fi)
			}
		}
	}
	out.Notes = it.Details.NotesPlain
	for _, h := range it.Details.PasswordHistory {
		if h.Value == "" {
			continue
		}
		out.History = append(out.History, HistoryImport{Value: h.Value, At: time.Unix(h.Time, 0).UTC()})
	}
	sort.SliceStable(out.History, func(i, j int) bool { return out.History[i].At.Before(out.History[j].At) })
	if da := it.Details.DocumentAttr; da != nil && da.ID != "" {
		if data, ok := attachmentBytes(files, da.ID, da.FileName); ok {
			name := da.FileName
			if name == "" {
				name = da.ID
			}
			out.Attachments = append(out.Attachments, AttachmentImport{Name: name, Data: data})
		}
	}
	return out
}

// onepuxSectionFields maps one section field by its value's key: the vault's own types where they
// meet (concealed, string, email, url, otp, date), and the rest on the types that hold them
// (phone, menu and gender as strings, month-year as a date, a credit card number concealed, an
// address one string, an SSH key the private key concealed with its metadata beside it).
func onepuxSectionFields(label string, v onepuxFieldValue) []FieldImport {
	switch {
	case v.Concealed != nil:
		return []FieldImport{{Name: label, Type: TypeConcealed, Value: *v.Concealed}}
	case v.String != nil:
		return []FieldImport{{Name: label, Type: TypeString, Value: *v.String}}
	case v.Email != nil:
		return []FieldImport{{Name: label, Type: TypeEmail, Value: *v.Email}}
	case v.URL != nil:
		return []FieldImport{{Name: label, Type: TypeURL, Value: *v.URL}}
	case v.TOTP != nil:
		return []FieldImport{{Name: label, Type: TypeOTP, Value: *v.TOTP}}
	case v.Date != nil && *v.Date > 0:
		return []FieldImport{{Name: label, Type: TypeDate, Value: time.Unix(*v.Date, 0).UTC().Format("2006-01-02")}}
	case v.MonthYear != nil && *v.MonthYear > 0:
		y, m := *v.MonthYear/100, *v.MonthYear%100
		if m >= 1 && m <= 12 {
			return []FieldImport{{Name: label, Type: TypeDate, Value: fmt.Sprintf("%04d-%02d", y, m)}}
		}
		return nil
	case v.Phone != nil:
		return []FieldImport{{Name: label, Type: TypeString, Value: *v.Phone}}
	case v.Menu != nil:
		return []FieldImport{{Name: label, Type: TypeString, Value: *v.Menu}}
	case v.Gender != nil:
		return []FieldImport{{Name: label, Type: TypeString, Value: *v.Gender}}
	case v.CreditCardType != nil:
		return []FieldImport{{Name: label, Type: TypeString, Value: *v.CreditCardType}}
	case v.CreditCardNumber != nil:
		return []FieldImport{{Name: label, Type: TypeConcealed, Value: *v.CreditCardNumber}}
	case v.Reference != nil:
		return []FieldImport{{Name: label, Type: TypeString, Value: *v.Reference}}
	case v.Address != nil:
		parts := []string{v.Address.Street, v.Address.City, v.Address.State, v.Address.ZIP, v.Address.Country, v.Address.Country2}
		var keep []string
		for _, p := range parts {
			if strings.TrimSpace(p) != "" {
				keep = append(keep, strings.TrimSpace(p))
			}
		}
		if len(keep) == 0 {
			return nil
		}
		return []FieldImport{{Name: label, Type: TypeString, Value: strings.Join(keep, ", ")}}
	case v.SSHKey != nil:
		out := []FieldImport{{Name: label, Type: TypeConcealed, Value: v.SSHKey.PrivateKey}}
		if v.SSHKey.Metadata.PublicKey != "" {
			out = append(out, FieldImport{Name: label + " public key", Type: TypeString, Value: v.SSHKey.Metadata.PublicKey})
		}
		if v.SSHKey.Metadata.Fingerprint != "" {
			out = append(out, FieldImport{Name: label + " fingerprint", Type: TypeString, Value: v.SSHKey.Metadata.Fingerprint})
		}
		if v.SSHKey.Metadata.KeyType != "" {
			out = append(out, FieldImport{Name: label + " type", Type: TypeString, Value: v.SSHKey.Metadata.KeyType})
		}
		return out
	default:
		return nil
	}
}

// attachmentBytes finds one attachment in the zip's files/ folder: the entry is named after the
// document id, with the file's own name riding along ("id___name").
func attachmentBytes(files map[string][]byte, id, name string) ([]byte, bool) {
	for _, want := range []string{id, id + "___" + name} {
		if b, ok := files[want]; ok {
			return b, true
		}
	}
	for key, b := range files {
		if strings.HasPrefix(key, id+"___") {
			return b, true
		}
	}
	return nil, false
}

// bytesReader is the whole export, as zip.NewReader wants it.
type bytesReader struct{ b []byte }

func (r *bytesReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
