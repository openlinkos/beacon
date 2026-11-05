package importer

import (
	"fmt"
	"io"
	"strings"
)

// A plain CSV with the columns named by the caller: every column of the import — the title, the
// URL, the user name, the password, the one-time-passcode seed, the notes, the tags, the
// category — is mapped to a header, so any export a keeper or a spreadsheet wrote can come in.
// What is not mapped is ignored; what the caller does not name is guessed from the header, the
// same way the 1Password CSV's columns are found. One import is one collection.
//
// The column names are the vault's own words: title, url, username, password, otp, notes, tags,
// category. Tags are comma-separated; the category is one of the vault's (login by default).

// Columns is the mapping of a plain CSV: each name is a header of the file. A name left empty is
// guessed from the header.
type Columns struct {
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	OTP      string `json:"otp,omitempty"`
	Notes    string `json:"notes,omitempty"`
	Tags     string `json:"tags,omitempty"`
	Category string `json:"category,omitempty"`
}

// guessColumns names the columns from the header alone, for the mapping the caller left empty.
var guessColumns = map[string][]string{
	"title":    {"title", "name", "account", "item"},
	"url":      {"url", "website", "site", "login url"},
	"username": {"username", "user name", "userid", "user id", "login", "email"},
	"password": {"password", "pwd"},
	"otp":      {"otp", "otpauth", "totp", "one time password"},
	"notes":    {"notes", "note", "comments"},
	"tags":     {"tags", "labels"},
	"category": {"category", "type"},
}

// ParseCSV reads a plain CSV into one collection the caller names, by the columns given.
func ParseCSV(r io.Reader, collection string, cols Columns) (Batch, error) {
	rows, err := readCSV(r)
	if err != nil {
		return Batch{}, err
	}
	wanted := map[string][]string{}
	for name, header := range map[string]string{
		"title": cols.Title, "url": cols.URL, "username": cols.Username, "password": cols.Password,
		"otp": cols.OTP, "notes": cols.Notes, "tags": cols.Tags, "category": cols.Category,
	} {
		if header != "" {
			wanted[name] = []string{header}
			continue
		}
		wanted[name] = guessColumns[name]
	}
	found := headerIndex(rows[0], wanted)
	if _, ok := found["title"]; !ok {
		return Batch{}, fmt.Errorf("the CSV has no title column; name it with -map title=HEADER")
	}
	if _, ok := found["username"]; !ok {
		return Batch{}, fmt.Errorf("the CSV has no username column; name it with -map username=HEADER")
	}
	cell := func(row []string, key string) string {
		i, ok := found[key]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	col := CollectionImport{Name: collection, LevelKey: SourceCSV}
	for _, row := range rows[1:] {
		title := cell(row, "title")
		user := cell(row, "username")
		if title == "" && user == "" {
			continue
		}
		item := ItemImport{Title: titleOf(title, user), Category: anyCategory(cell(row, "category")),
			Source: SourceCSV, Notes: cell(row, "notes"), Tags: cleanTags(splitList(cell(row, "tags")), SourceTag(SourceCSV))}
		if url := cell(row, "url"); url != "" {
			item.URLs = append(item.URLs, url)
		}
		if user != "" {
			item.Fields = append(item.Fields, FieldImport{Name: "username", Type: TypeString, Purpose: PurposeUsername, Value: user})
		}
		if pass := cell(row, "password"); pass != "" {
			item.Fields = append(item.Fields, FieldImport{Name: "password", Type: TypeConcealed, Purpose: PurposePassword, Value: pass})
		}
		if otp := cell(row, "otp"); otp != "" {
			item.Fields = append(item.Fields, FieldImport{Name: "one-time password", Type: TypeOTP, Value: otpSeedOf(otp)})
		}
		col.Items = append(col.Items, item)
	}
	return Batch{Source: SourceCSV, Collections: []CollectionImport{col}}, nil
}

// anyCategory takes the vault's own category name (or a title like "Credit Card"); anything it
// does not know is a secure note's, which loses nothing but the level a named category would set.
func anyCategory(in string) string {
	c := strings.ToLower(strings.TrimSpace(in))
	c = strings.ReplaceAll(c, " ", "_")
	c = strings.ReplaceAll(c, "-", "_")
	if _, ok := CategoryTitles[c]; ok {
		return c
	}
	for name, title := range CategoryTitles {
		if squash(title) == squash(in) {
			return name
		}
	}
	return CatSecureNote
}
