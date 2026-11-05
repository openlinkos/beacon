package importer

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Chrome's password export: a CSV with the header name,url,username,password,note (Chromium's
// password_csv_writer), one row a saved credential, multi-site credentials one row a site. Rows
// with the same username and password and different domains are one credential shown several
// times — they merge into one item with every URL, which is what the merge rule keeps. Chrome
// does not export passkeys; nothing here pretends it does.
//
// https://support.google.com/chrome/answer/13068232 and Chromium's password_csv_writer.cc.

// chromeColumns are Chrome's five, recognised by their header so an export with the columns in
// another order, or renamed, still reads.
var chromeColumns = map[string][]string{
	"title":    {"name", "title"},
	"url":      {"url", "website"},
	"username": {"username", "user name"},
	"password": {"password"},
	"note":     {"note", "notes"},
}

// ParseChrome reads Chrome's CSV into one collection the caller names. collection may be empty:
// the caller then folds the batch itself.
func ParseChrome(r io.Reader, collection string) (Batch, error) {
	rows, err := readCSV(r)
	if err != nil {
		return Batch{}, err
	}
	cols := headerIndex(rows[0], chromeColumns)
	if _, ok := cols["username"]; !ok {
		return Batch{}, fmt.Errorf("the CSV has no username column")
	}
	if _, ok := cols["password"]; !ok {
		return Batch{}, fmt.Errorf("the CSV has no password column")
	}
	cell := func(row []string, key string) string {
		i, ok := cols[key]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	// group by username+password: the same pair across domains is one credential
	type group struct {
		title   string
		urls    []string
		domains map[string]bool
		notes   []string
	}
	groups := map[string]*group{}
	var order []string
	merged := 0
	for _, row := range rows[1:] {
		user, pass := cell(row, "username"), cell(row, "password")
		if user == "" && pass == "" {
			continue // a row with nothing in it
		}
		url := cell(row, "url")
		key := user + "\x00" + pass
		g, ok := groups[key]
		if !ok {
			g = &group{title: cell(row, "title"), domains: map[string]bool{}}
			groups[key] = g
			order = append(order, key)
		}
		if url != "" {
			host := hostOf(NormalizeURL(url))
			if !g.domains[host] {
				g.domains[host] = true
				g.urls = append(g.urls, url)
			} else {
				merged++ // the same credential saved for the same site twice
			}
		}
		if note := cell(row, "note"); note != "" && !contains(g.notes, note) {
			g.notes = append(g.notes, note)
		}
	}
	sort.Strings(order)
	col := CollectionImport{Name: collection, LevelKey: SourceChrome}
	for _, key := range order {
		g := groups[key]
		user, _, _ := strings.Cut(key, "\x00")
		item := ItemImport{Title: titleOf(g.title, hostOf(NormalizeURL(first(g.urls)))), Category: CatLogin,
			Source: SourceChrome, URLs: dedupeURLs(g.urls), Tags: cleanTags(nil, SourceTag(SourceChrome))}
		item.Fields = append(item.Fields, FieldImport{Name: "username", Type: TypeString, Purpose: PurposeUsername, Value: user})
		if _, pass, _ := strings.Cut(key, "\x00"); pass != "" {
			item.Fields = append(item.Fields, FieldImport{Name: "password", Type: TypeConcealed, Purpose: PurposePassword, Value: pass})
		}
		item.Notes = strings.Join(g.notes, "\n")
		col.Items = append(col.Items, item)
	}
	return Batch{Source: SourceChrome, Collections: []CollectionImport{col}, Merged: merged}, nil
}

func first(in []string) string {
	if len(in) == 0 {
		return ""
	}
	return in[0]
}

func contains(in []string, s string) bool {
	for _, x := range in {
		if x == s {
			return true
		}
	}
	return false
}
