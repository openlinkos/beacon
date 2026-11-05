package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Bitwarden's JSON export, the unencrypted one ("file format: JSON, unencrypted"), which is what
// an import into another keeper takes: {"encrypted": false, "folders": […], "items": […]}. The
// encrypted export is refused with a plain error — it carries the vault's own key derivation and
// Beacon takes a plaintext export by design, so the honest answer is to say export it unencrypted.
// Every item lands in the one collection the caller names (a folder is a grouping inside one
// vault, not a vault of its own); folders ride along as tags.
//
// https://bitwarden.com/help/export-your-data/

// bitwardenExport is the file's top level.
type bitwardenExport struct {
	Encrypted bool `json:"encrypted"`
	Folders   []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"folders"`
	Items []bitwardenItem `json:"items"`
}

// bitwardenItem is one row, with only the parts the import maps.
type bitwardenItem struct {
	ID       string `json:"id"`
	FolderID string `json:"folderId"`
	Type     int    `json:"type"` // 1 login, 2 secure note, 3 card, 4 identity
	Name     string `json:"name"`
	Notes    string `json:"notes"`
	Fields   []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Type  int    `json:"type"` // 0 text, 1 hidden, 2 boolean
	} `json:"fields"`
	Login *struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTP     string `json:"totp"`
		URIs     []struct {
			URI string `json:"uri"`
		} `json:"uris"`
	} `json:"login"`
	Card *struct {
		CardholderName string `json:"cardholderName"`
		Brand          string `json:"brand"`
		Number         string `json:"number"`
		ExpMonth       string `json:"expMonth"`
		ExpYear        string `json:"expYear"`
		Code           string `json:"code"`
	} `json:"card"`
	Identity *struct {
		Title          string `json:"title"`
		FirstName      string `json:"firstName"`
		LastName       string `json:"lastName"`
		Email          string `json:"email"`
		Phone          string `json:"phone"`
		Company        string `json:"company"`
		PassportNumber string `json:"passportNumber"`
		SSN            string `json:"ssn"`
	} `json:"identity"`
}

// ParseBitwarden reads the unencrypted JSON export into one collection the caller names.
func ParseBitwarden(r io.Reader, collection string) (Batch, error) {
	b, err := io.ReadAll(io.LimitReader(r, 256<<20))
	if err != nil {
		return Batch{}, err
	}
	var export bitwardenExport
	if err := json.Unmarshal(b, &export); err != nil {
		return Batch{}, fmt.Errorf("the Bitwarden JSON could not be read: %w", err)
	}
	if export.Encrypted {
		return Batch{}, fmt.Errorf("the Bitwarden export is encrypted; export it unencrypted to import it (delete the file afterwards, either way)")
	}
	folders := map[string]string{}
	for _, f := range export.Folders {
		folders[f.ID] = f.Name
	}
	col := CollectionImport{Name: collection, LevelKey: SourceBitwarden}
	for _, it := range export.Items {
		col.Items = append(col.Items, parseBitwardenItem(it, folders))
	}
	return Batch{Source: SourceBitwarden, Collections: []CollectionImport{col}}, nil
}

// parseBitwardenItem maps one item by its type: a login's username, password, one-time-passcode
// seed and URIs; a secure note's notes; a card's number and the rest; an identity's parts. The
// custom fields come along, hidden ones concealed.
func parseBitwardenItem(it bitwardenItem, folders map[string]string) ItemImport {
	out := ItemImport{Title: titleOf(it.Name, ""), Source: SourceBitwarden + ":" + it.ID,
		Notes: it.Notes, Tags: cleanTags([]string{folders[it.FolderID]}, SourceTag(SourceBitwarden))}
	switch it.Type {
	case 2:
		out.Category = CatSecureNote
	case 3:
		out.Category = CatCreditCard
	case 4:
		out.Category = CatIdentity
	default:
		out.Category = CatLogin
	}
	if it.Login != nil {
		if it.Login.Username != "" {
			out.Fields = append(out.Fields, FieldImport{Name: "username", Type: TypeString, Purpose: PurposeUsername, Value: it.Login.Username})
		}
		if it.Login.Password != "" {
			out.Fields = append(out.Fields, FieldImport{Name: "password", Type: TypeConcealed, Purpose: PurposePassword, Value: it.Login.Password})
		}
		if it.Login.TOTP != "" {
			out.Fields = append(out.Fields, FieldImport{Name: "one-time password", Type: TypeOTP, Value: otpSeedOf(it.Login.TOTP)})
		}
		for _, u := range it.Login.URIs {
			if u.URI != "" {
				out.URLs = append(out.URLs, u.URI)
			}
		}
	}
	if it.Card != nil {
		section := "card"
		add := func(name, typ, value string) {
			if value != "" {
				out.Fields = append(out.Fields, FieldImport{Section: section, Name: name, Type: typ, Value: value})
			}
		}
		add("cardholder name", TypeString, it.Card.CardholderName)
		add("brand", TypeString, it.Card.Brand)
		add("number", TypeConcealed, it.Card.Number)
		add("expiry", TypeDate, expiryOf(it.Card.ExpMonth, it.Card.ExpYear))
		add("security code", TypeConcealed, it.Card.Code)
	}
	if it.Identity != nil {
		section := "identity"
		add := func(name, value string) {
			if value != "" {
				out.Fields = append(out.Fields, FieldImport{Section: section, Name: name, Type: TypeString, Value: value})
			}
		}
		add("name", strings.TrimSpace(it.Identity.Title+" "+it.Identity.FirstName+" "+it.Identity.LastName))
		add("email", it.Identity.Email)
		add("phone", it.Identity.Phone)
		add("company", it.Identity.Company)
		add("passport number", it.Identity.PassportNumber)
		add("social security number", it.Identity.SSN)
	}
	for _, f := range it.Fields {
		if f.Name == "" || f.Value == "" {
			continue
		}
		typ := TypeString
		if f.Type == 1 {
			typ = TypeConcealed
		}
		out.Fields = append(out.Fields, FieldImport{Name: f.Name, Type: typ, Value: f.Value})
	}
	out.URLs = dedupeURLs(out.URLs)
	return out
}

// expiryOf puts a card's month and year together as a date value.
func expiryOf(month, year string) string {
	month, year = strings.TrimSpace(month), strings.TrimSpace(year)
	if month == "" && year == "" {
		return ""
	}
	if len(year) == 2 { // Bitwarden writes two digits
		year = "20" + year
	}
	if month == "" {
		return year
	}
	if len(month) == 1 {
		month = "0" + month
	}
	return year + "-" + month
}
