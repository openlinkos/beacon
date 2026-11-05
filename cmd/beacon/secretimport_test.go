package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
)

// The import command's own parts: the column mapping and the report it prints — which, dry-run or
// not, never carries a value. The fixtures here are the made-up ones of the importer's tests.
func TestParseColumnMap(t *testing.T) {
	cols, err := parseColumnMap("title=Service,username=Who,password=Secret")
	if err != nil {
		t.Fatal(err)
	}
	if cols.Title != "Service" || cols.Username != "Who" || cols.Password != "Secret" {
		t.Fatalf("the mapping: %+v", cols)
	}
	if _, err := parseColumnMap("title"); err == nil {
		t.Fatal("a column is name=HEADER")
	}
	if _, err := parseColumnMap("nope=X"); err == nil || !strings.Contains(err.Error(), "one of") {
		t.Fatalf("the names are the vault's own: %v", err)
	}
}

func TestPrintImportReportCarriesNoValue(t *testing.T) {
	batch, err := importer.ParseChrome(strings.NewReader(
		"name,url,username,password,note\nPortal,https://www.example.com,fixture-import-chrome-user@example.com,fixture-import-chrome-value-plugh-quux9,hi\n"), "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	report := secrets.ImportReport{Source: batch.Source, DryRun: true, Collections: []secrets.CollectionReport{{
		Name: "Chrome", New: true, Level: "person", Items: 1, Fields: 2, OTPs: 0, Never: 0,
		Categories: []secrets.CategoryCount{{Name: "login", Count: 1}},
	}}}
	var out bytes.Buffer
	printImportReport(&out, report)
	printImportFollowups(&out, report)
	text := out.String()
	for _, marker := range []string{"fixture-import-chrome-value-plugh-quux9", "fixture-import-chrome-user@example.com"} {
		if strings.Contains(text, marker) {
			t.Fatalf("the report leaks a value: %s", marker)
		}
	}
	if !strings.Contains(text, "dry-run from chrome") || !strings.Contains(text, "Chrome (new)") ||
		!strings.Contains(text, "person") || !strings.Contains(text, "login 1") {
		t.Fatalf("the report says its counts: %s", text)
	}
	if !strings.Contains(text, "confirm the Chrome collection's level") {
		t.Fatalf("the follow-ups: %s", text)
	}
	// a report with conflicts names them without a value
	report.DryRun = false
	report.Conflicts = []secrets.ConflictReport{{Collection: "Chrome", Incoming: "Portal", Existing: "Portal", SharedURLs: 1}}
	report.Skipped = 1
	out.Reset()
	printImportReport(&out, report)
	if !strings.Contains(out.String(), "skipped 1") || !strings.Contains(out.String(), "ALREADY THERE") {
		t.Fatalf("the outcomes and the conflicts: %s", out.String())
	}
}
