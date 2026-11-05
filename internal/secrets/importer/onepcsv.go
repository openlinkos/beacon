package importer

import (
	"fmt"
	"io"
	"strings"
)

// The 1Password CSV export: what 1Password writes for an account's Login and Password items (it
// exports nothing else to CSV — a whole account is better as a 1PUX export). The columns are
// recognised by their header, not fixed to one spelling: the export's own header has changed
// between versions, so every column is matched by the names it has carried.
//
// https://support.1password.com/export/

// onepasswordColumns are the columns the CSV may carry, by the names they have had. The Fields
// column holds "name: value; name: value" pairs, which become fields of their own.
var onepasswordColumns = map[string][]string{
	"title":    {"title", "account", "name", "item"},
	"url":      {"url", "urls", "website", "login url", "website url", "site"},
	"username": {"username", "user name", "userid", "user id", "login"},
	"password": {"password", "pwd"},
	"otp":      {"otp auth", "otpauth", "otp", "one time password", "totp"},
	"type":     {"type", "category"},
	"tags":     {"tags", "labels"},
	"fields":   {"fields"},
	"notes":    {"notes", "note", "comments"},
}

// Parse1PasswordCSV reads the 1Password CSV into one collection the caller names. collection may
// be empty: the caller then folds the batch itself.
func Parse1PasswordCSV(r io.Reader, collection string) (Batch, error) {
	rows, err := readCSV(r)
	if err != nil {
		return Batch{}, err
	}
	cols := headerIndex(rows[0], onepasswordColumns)
	if _, ok := cols["title"]; !ok {
		return Batch{}, fmt.Errorf("the CSV has no title column")
	}
	cell := func(row []string, key string) string {
		i, ok := cols[key]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	col := CollectionImport{Name: collection, LevelKey: Source1Password}
	for _, row := range rows[1:] {
		title := cell(row, "title")
		user := cell(row, "username")
		pass := cell(row, "password")
		if title == "" && user == "" && pass == "" {
			continue
		}
		item := ItemImport{Title: titleOf(title, user), Category: csvCategory(cell(row, "type")),
			Source: Source1Password, Tags: cleanTags(splitList(cell(row, "tags")), SourceTag(Source1Password))}
		if url := cell(row, "url"); url != "" {
			item.URLs = append(item.URLs, url)
		}
		if user != "" {
			item.Fields = append(item.Fields, FieldImport{Name: "username", Type: TypeString, Purpose: PurposeUsername, Value: user})
		}
		if pass != "" {
			item.Fields = append(item.Fields, FieldImport{Name: "password", Type: TypeConcealed, Purpose: PurposePassword, Value: pass})
		}
		if otp := cell(row, "otp"); otp != "" {
			seed := otpSeedOf(otp)
			if seed != "" {
				item.Fields = append(item.Fields, FieldImport{Name: "one-time password", Type: TypeOTP, Value: seed})
			}
		}
		for _, pair := range splitList(cell(row, "fields")) {
			name, value, ok := strings.Cut(pair, ":")
			if !ok || strings.TrimSpace(name) == "" {
				continue
			}
			item.Fields = append(item.Fields, FieldImport{Name: strings.TrimSpace(name), Type: TypeString, Value: strings.TrimSpace(value)})
		}
		item.Notes = cell(row, "notes")
		col.Items = append(col.Items, item)
	}
	return Batch{Source: Source1Password, Collections: []CollectionImport{col}}, nil
}

// csvCategory maps the CSV's type column to a category; the export holds Login and Password items.
func csvCategory(in string) string {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "password":
		return CatPassword
	default:
		return CatLogin
	}
}

// otpSeedOf takes the seed out of an otpauth:// URI (the shape the CSVs carry), leaving a seed
// that already is one as it came.
func otpSeedOf(in string) string {
	in = strings.TrimSpace(in)
	if !strings.HasPrefix(strings.ToLower(in), "otpauth://") {
		return in
	}
	_, rest, _ := strings.Cut(in, "?")
	for _, part := range strings.Split(rest, "&") {
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(k, "secret") {
			return v
		}
	}
	return ""
}

// splitList splits a list the exports write comma- or semicolon-separated.
func splitList(in string) []string {
	if strings.TrimSpace(in) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(in, func(r rune) bool { return r == ',' || r == ';' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
