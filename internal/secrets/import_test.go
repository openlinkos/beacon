package secrets

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/secrets/importer"
	"github.com/openlinkos/beacon/internal/store"
)

// The import's tests, on fixtures made up for them (never a real export): a 1PUX export and a
// Chrome export, imported and counted out; the one-time passcode computed from its seed; the four
// ways a conflict can go; the levels the settings give. The values carry the same markers as the
// vault's own fixtures, so the gate's leak check (scripts/check-test-leaks.sh) greps the tests'
// output for them.

// importFixture1PUX builds a small 1PUX export: a personal vault with a login (a user name, a
// password, a one-time-passcode seed, a password history, an attachment) and a team vault with a
// credit card and an archived passport.
func importFixture1PUX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("export.attributes")
	_, _ = w.Write([]byte(`{"version":4,"description":"1Password Unencrypted Export","createdAt":1760000000}`))
	w, _ = zw.Create("files/doc1___Recovery words.txt")
	_, _ = w.Write([]byte("fixture-import-1pux-attachment-quartz"))
	w, _ = zw.Create("export.data")
	_, _ = w.Write([]byte(`{"accounts":[{"attrs":{"accountName":"Example","uuid":"a1"},"vaults":[
	 {"attrs":{"uuid":"v1","name":"Private","type":"P"},"items":[
	  {"uuid":"i1","state":"active","categoryUuid":"001",
	   "overview":{"title":"Example Login","url":"https://www.example.com/login","tags":["Work"]},
	   "details":{
	    "loginFields":[
	     {"value":"fixture-import-1pux-user@example.com","name":"email","id":"u","fieldType":"E","designation":"username"},
	     {"value":"fixture-import-1pux-value-xyzzy-frobnicate","name":"password","id":"p","fieldType":"P","designation":"password"}],
	    "notesPlain":"fixture-import-1pux-notes-tumbler",
	    "sections":[{"title":"Security","fields":[
	     {"title":"one-time password","id":"totp","value":{"totp":"GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}}]}],
	    "passwordHistory":[{"value":"fixture-import-1pux-old-value-grault","time":1700000000}],
	    "documentAttributes":{"fileName":"Recovery words.txt","documentId":"doc1","decryptedSize":40}}}]},
	 {"attrs":{"uuid":"v2","name":"Engineering","type":"E"},"items":[
	  {"uuid":"i2","state":"active","categoryUuid":"002","overview":{"title":"Company card"},
	   "details":{"sections":[{"title":"Card","fields":[
	    {"title":"card number","id":"n","value":{"creditCardNumber":"fixture-import-card-number"}}]}]}},
	  {"uuid":"i3","state":"archived","categoryUuid":"106","overview":{"title":"Old passport"},
	   "details":{"sections":[{"title":"Passport","fields":[
	    {"title":"number","id":"n","value":{"concealed":"fixture-import-passport-number"}}]}]}}]}]}]}`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const importFixtureChrome = `name,url,username,password,note
Portal,https://www.example.com/signin,fixture-import-chrome-user@example.com,fixture-import-chrome-value-plugh-quux9,the main one
Portal,https://portal.example.com,fixture-import-chrome-user@example.com,fixture-import-chrome-value-plugh-quux9,same credential
Mail,https://mail.example.org,fixture-import-chrome-user@example.com,fixture-import-chrome-other-value-2,
`

// importFixtureMarkers are the strings the leak check greps this package's output for.
var importFixtureMarkers = []string{
	"fixture-import-1pux-value-xyzzy-frobnicate", "fixture-import-1pux-notes-tumbler",
	"fixture-import-1pux-attachment-quartz", "fixture-import-1pux-old-value-grault",
	"fixture-import-chrome-value-plugh-quux9",
}

// import1PUX parses the fixture.
func import1PUX(t *testing.T) importer.Batch {
	t.Helper()
	batch, err := importer.Parse1PUX(bytes.NewReader(importFixture1PUX(t)))
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

// importChrome parses the fixture into a named collection.
func importChrome(t *testing.T, name string) importer.Batch {
	t.Helper()
	batch, err := importer.ParseChrome(strings.NewReader(importFixtureChrome), name)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

// TestImport1PUXAndChromeCountOut is the acceptance test: a 1PUX export and a Chrome export
// imported, every item, field, attachment and one-time passcode accounted for, the states and the
// levels where the settings put them, and every row signed.
func TestImport1PUXAndChromeCountOut(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	report, err := s.Import(owner, import1PUX(t), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Collections) != 2 {
		t.Fatalf("two collections: %+v", report.Collections)
	}
	priv, eng := report.Collections[0], report.Collections[1]
	if priv.Name != "Private" || priv.Items != 1 || priv.Fields != 3 || priv.Attachments != 1 || priv.OTPs != 1 || priv.History != 1 {
		t.Fatalf("the personal vault's counts: %+v", priv)
	}
	if priv.Level != LevelPerson || !priv.New {
		t.Fatalf("the personal vault starts at person: %+v", priv)
	}
	if eng.Name != "Engineering" || eng.Items != 2 || eng.Level != LevelReview || eng.Never != 2 {
		t.Fatalf("the team vault's counts: %+v", eng)
	}
	if eng.Archived != 1 {
		t.Fatalf("the archived passport counts as archived: %+v", eng)
	}
	if report.Created != 3 {
		t.Fatalf("three items written: %+v", report)
	}
	// what the report says is what the vault holds
	cols := s.Collections()
	if len(cols) != 2 {
		t.Fatalf("two collections in the vault: %+v", cols)
	}
	items := s.Items("Private", "")
	if len(items) != 1 {
		t.Fatalf("one item in Private: %+v", items)
	}
	login := items[0]
	if login.Title != "Example Login" || login.Source != "1password:i1" || login.Version != 2 {
		t.Fatalf("the login's overview: %+v", login)
	}
	ver, ok := s.Version(login.ID, login.Version)
	if !ok || len(ver.Fields) != 3 {
		t.Fatalf("the current version's fields: %+v", ver)
	}
	if login.Category != "login" || s.EffectiveLevel(login) != LevelPerson {
		t.Fatalf("the login inherits the personal vault's person: %q %q", login.Category, s.EffectiveLevel(login))
	}
	card := s.Items("Engineering", "")[0]
	if card.Category != "credit_card" || card.Level != LevelNever || s.EffectiveLevel(card) != LevelNever {
		t.Fatalf("the card starts at never, whatever its collection says: %+v", card)
	}
	archived := s.Items("Engineering", "archived")
	if len(archived) != 1 || archived[0].Title != "Old passport" {
		t.Fatalf("the passport came archived: %+v", archived)
	}
	// the history is the item's first version, the current values the second
	first, _ := s.Version(login.ID, 1)
	if !strings.Contains(first.Reason, "history") {
		t.Fatalf("the history's version says what it is: %+v", first)
	}
	// every imported row carries its signature
	if bad := s.CheckRows(); len(bad) != 0 {
		t.Fatalf("the imported rows verify: %v", bad)
	}

	// the Chrome export, into one collection of its own
	creport, err := s.Import(owner, importChrome(t, "Chrome Imports"), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(creport.Collections) != 1 || creport.Collections[0].Level != LevelPerson || creport.Collections[0].Items != 2 || creport.Merged != 0 {
		t.Fatalf("the Chrome report: %+v", creport)
	}
	if creport.Created != 2 {
		t.Fatalf("two Chrome items: %+v", creport)
	}
	chrome := s.Items("Chrome Imports", "")
	if len(chrome) != 2 {
		t.Fatalf("two items in the Chrome collection: %+v", chrome)
	}
	for _, it := range chrome {
		if it.Source != "chrome" || it.Category != "login" {
			t.Fatalf("Chrome's items: %+v", it)
		}
		if !containsString(it.Tags, "source/chrome") {
			t.Fatalf("the source tag: %v", it.Tags)
		}
		if s.EffectiveLevel(it) != LevelPerson {
			t.Fatalf("Chrome's items start at person: %q", s.EffectiveLevel(it))
		}
	}
}

// TestImportTOTPIsComputedFromTheSeed is the acceptance test for the one-time passcodes: the seed
// imports as an otp field, and the reveal answers the code the known seed gives at the vault's own
// clock — never the seed itself.
func TestImportTOTPIsComputedFromTheSeed(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if _, err := s.Import(owner, import1PUX(t), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	login := s.Items("Private", "")[0]
	values, fields, err := s.Reveal(owner, login.ID, nil, "fixture-password-entry")
	if err != nil {
		t.Fatal(err)
	}
	var otpField *Field
	for i, f := range fields {
		if f.Type == FieldOTP {
			otpField = &fields[i]
		}
	}
	if otpField == nil {
		t.Fatalf("the otp field is there: %+v", fields)
	}
	code, left, err := TOTPNow("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", s.now())
	if err != nil {
		t.Fatal(err)
	}
	key := otpField.Name
	if otpField.Section != "" {
		key = otpField.Section + "\x00" + otpField.Name
	}
	if values[key] != code {
		t.Fatalf("the code the seed gives at the vault's clock: %q (want %q)", values[key], code)
	}
	if values[key+"\x00expiry"] != i64(int64(left.Seconds())) {
		t.Fatalf("the seconds it has left: %q", values[key+"\x00expiry"])
	}
	if strings.Contains(values[key], "GEZD") {
		t.Fatal("the answer is the code, not the seed")
	}
}

// TestImportDryRunWritesNothingAndPrintsNoValues is the acceptance test for the dry-run: the same
// report as a real import would answer, nothing written, and not a value in it.
func TestImportDryRunWritesNothingAndPrintsNoValues(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	report, err := s.Import(owner, import1PUX(t), ImportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.Created != 0 {
		t.Fatalf("a dry-run writes nothing: %+v", report)
	}
	if len(s.Collections()) != 0 {
		t.Fatalf("a dry-run makes no collections: %+v", s.Collections())
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range importFixtureMarkers {
		if strings.Contains(string(raw), marker) {
			t.Fatalf("the dry-run report leaks a value: %s", marker)
		}
	}
	// the counts it answers are the counts a real import would write
	real, err := s.Import(owner, import1PUX(t), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Collections) != len(real.Collections) {
		t.Fatalf("the dry-run and the import agree: %+v %+v", report, real)
	}
	for i := range report.Collections {
		if report.Collections[i].Items != real.Collections[i].Items || report.Collections[i].Never != real.Collections[i].Never {
			t.Fatalf("the dry-run and the import agree: %+v %+v", report.Collections[i], real.Collections[i])
		}
	}
}

// importConflictBatch is one item that meets the item the test makes first: the same place, by the
// normalised URL, with the same user name.
func importConflictBatch(url, username string) importer.Batch {
	item := importer.ItemImport{Title: "Example Login", Category: importer.CatLogin,
		URLs: []string{url}, Source: importer.Source1Password,
		Fields: []importer.FieldImport{
			{Name: "username", Type: importer.TypeString, Purpose: importer.PurposeUsername, Value: username},
			{Name: "password", Type: importer.TypeConcealed, Purpose: importer.PurposePassword, Value: "fixture-import-conflict-new-value"},
		}}
	return importer.Batch{Source: importer.Source1Password, Collections: []importer.CollectionImport{
		{Name: "Infra", LevelKey: "1password", Items: []importer.ItemImport{item}}}}
}

// makeConflicting makes the item that is already there: www.example.com and the user name.
func makeConflicting(t *testing.T, s *Service) Item {
	t.Helper()
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Example Login", URLs: []string{"https://www.example.com/login"},
		Fields: []FieldIn{
			{Name: "username", Type: FieldString, Purpose: PurposeUsername, Value: "fixture-import-conflict-user@example.com"},
			{Name: "password", Type: FieldConcealed, Purpose: PurposePassword, Value: "fixture-import-conflict-old-value"},
		}})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

// TestImportConflictAskHoldsThemBack: the default holds a conflicting item for a person to decide
// — the item that is there stays as it was, and the report says what waited.
func TestImportConflictAskHoldsThemBack(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	item := makeConflicting(t, s)
	report, err := s.Import(owner, importConflictBatch("https://example.com/other", "fixture-import-conflict-user@example.com"), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.HeldBack != 1 || report.Created != 0 || len(report.Conflicts) != 1 {
		t.Fatalf("ask holds it back and says so: %+v", report)
	}
	c := report.Conflicts[0]
	if c.Incoming != "Example Login" || c.Existing != "Example Login" || c.SharedURLs != 1 {
		t.Fatalf("the conflict is named, without a value: %+v", c)
	}
	again := s.Items("Infra", "")
	if len(again) != 1 || again[0].Version != item.Version {
		t.Fatalf("nothing was written: %+v", again)
	}
	values, _, err := s.Reveal(owner, item.ID, []string{"password"}, "fixture-password-entry")
	if err != nil {
		t.Fatal(err)
	}
	if values["password"] != "fixture-import-conflict-old-value" {
		t.Fatal("the item that was there keeps its value")
	}
}

// TestImportConflictNewVersionFoldsTheValues: the item that is there gains a version with the
// imported values, the places join, and nothing is lost.
func TestImportConflictNewVersionFoldsTheValues(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	item := makeConflicting(t, s)
	report, err := s.Import(owner, importConflictBatch("https://example.com/other", "fixture-import-conflict-user@example.com"), ImportOptions{OnConflict: "new_version"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Versions != 1 || report.Created != 0 {
		t.Fatalf("one new version: %+v", report)
	}
	got := s.Items("Infra", "")
	if len(got) != 1 {
		t.Fatalf("still one item: %+v", got)
	}
	if got[0].Version != item.Version+1 {
		t.Fatalf("the version moved: %d → %d", item.Version, got[0].Version)
	}
	if len(got[0].URLs) != 2 {
		t.Fatalf("the places join: %v", got[0].URLs)
	}
	values, _, err := s.Reveal(owner, item.ID, nil, "fixture-password-entry")
	if err != nil {
		t.Fatal(err)
	}
	if values["password"] != "fixture-import-conflict-new-value" {
		t.Fatal("the imported value took the field")
	}
	if values["username"] != "fixture-import-conflict-user@example.com" {
		t.Fatal("the user name stayed")
	}
}

// TestImportConflictSkipKeepsWhatIsThere: the incoming item is passed over, counted as skipped.
func TestImportConflictSkipKeepsWhatIsThere(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	item := makeConflicting(t, s)
	report, err := s.Import(owner, importConflictBatch("https://example.com/other", "fixture-import-conflict-user@example.com"), ImportOptions{OnConflict: "skip"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != 1 || report.Created != 0 {
		t.Fatalf("skipped: %+v", report)
	}
	if got := s.Items("Infra", ""); len(got) != 1 || got[0].Version != item.Version {
		t.Fatalf("the item that was there stays: %+v", got)
	}
}

// TestImportConflictKeepBothLeavesTwo: the incoming item is made as an item of its own beside the
// one that is there.
func TestImportConflictKeepBothLeavesTwo(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	makeConflicting(t, s)
	report, err := s.Import(owner, importConflictBatch("https://example.com/other", "fixture-import-conflict-user@example.com"), ImportOptions{OnConflict: "keep_both"})
	if err != nil {
		t.Fatal(err)
	}
	if report.KeptBoth != 1 || report.Created != 0 {
		t.Fatalf("kept both: %+v", report)
	}
	if got := s.Items("Infra", ""); len(got) != 2 {
		t.Fatalf("two items now: %+v", got)
	}
}

// TestImportNoConflictWithoutTheUserName: the same place with another user name is another
// credential, and a user name with no place in common is another too.
func TestImportNoConflictWithoutTheUserName(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	makeConflicting(t, s)
	report, err := s.Import(owner, importConflictBatch("https://example.com/other", "fixture-import-another-user@example.com"), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.HeldBack != 0 || report.Created != 1 {
		t.Fatalf("another user name is no conflict: %+v", report)
	}
	report, err = s.Import(owner, importConflictBatch("https://other.example.net", "fixture-import-conflict-user@example.com"), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.HeldBack != 0 || report.Created != 1 {
		t.Fatalf("another place is no conflict: %+v", report)
	}
}

// TestImportLevelsFollowTheSettings: the settings' maps decide the levels — by source, by
// category, and the call's own choice over both.
func TestImportLevelsFollowTheSettings(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	// chrome's collection starts at person by the default levels; the call lifts it to auto
	report, err := s.Import(owner, importChrome(t, "Chrome Imports"), ImportOptions{Levels: map[string]string{"Chrome Imports": LevelAuto}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Collections[0].Level != LevelAuto {
		t.Fatalf("the call's own level wins: %+v", report.Collections[0])
	}
	if col, ok := s.Collection("Chrome Imports"); !ok || col.Level != LevelAuto {
		t.Fatalf("the collection was made at auto: %+v", col)
	}
	// an override on a dry-run changes only the report; a bad level is refused
	dry, err := s.Import(owner, importChrome(t, "Chrome Imports"), ImportOptions{DryRun: true, Levels: map[string]string{"Chrome Imports": LevelNever}})
	if err != nil {
		t.Fatal(err)
	}
	if dry.Collections[0].Level != LevelNever {
		t.Fatalf("the dry-run answers the level it was given: %+v", dry.Collections[0])
	}
	if _, err := s.Import(owner, importChrome(t, "Chrome Imports"), ImportOptions{Levels: map[string]string{"Chrome Imports": "sometimes"}}); !errors.Is(err, ErrBadInput) {
		t.Fatalf("a level is a level, got %v", err)
	}
}

// TestImportRefusesAnAgentAndASealedVault: an agent imports nothing (the vault answers an agent
// with nothing until the grants arrive), and a sealed vault writes nothing.
func TestImportRefusesAnAgentAndASealedVault(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if _, err := s.Import(agent, importChrome(t, "Chrome Imports"), ImportOptions{}); !errors.Is(err, ErrAgent) {
		t.Fatalf("an agent imports nothing, got %v", err)
	}
	if _, err := s.Import(Actor{}, importChrome(t, "Chrome Imports"), ImportOptions{}); !errors.Is(err, ErrAgent) {
		t.Fatalf("the system itself is not a person here, got %v", err)
	}
	ss, _ := newVault(t, SealPassphrase, Deps{Lang: i18n.EN})
	if err := ss.Unseal("fixture-passphrase-hush-puppy"); err != nil {
		t.Fatal(err)
	}
	if err := ss.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.Import(owner, importChrome(t, "Chrome Imports"), ImportOptions{}); !errors.Is(err, ErrSealed) {
		t.Fatalf("a sealed vault imports nothing, got %v", err)
	}
}

// TestImportNeverStaysNeverForAgents: an item a category starts at never keeps it, and the vault
// answers an agent's read with nothing — the level's own meaning, which the import does not
// soften.
func TestImportNeverStaysNeverForAgents(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if _, err := s.Import(owner, import1PUX(t), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	card := s.Items("Engineering", "")[0]
	if s.EffectiveLevel(card) != LevelNever {
		t.Fatalf("the card is never: %q", s.EffectiveLevel(card))
	}
	if _, _, err := s.Reveal(agent, card.ID, nil, ""); !errors.Is(err, ErrAgent) {
		t.Fatalf("an agent reads nothing, never or not, got %v", err)
	}
}

// TestImportWritesItsAuditEntry: the import is one audited thing — who, from where, what came in
// and how much, never a value.
func TestImportWritesItsAuditEntry(t *testing.T) {
	var entries []store.AuditEntry
	s, _ := newVault(t, SealFile, Deps{Audit: func(e store.AuditEntry) error {
		entries = append(entries, e)
		return nil
	}, Lang: i18n.EN})
	if _, err := s.Import(owner, importChrome(t, "Chrome Imports"), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[len(entries)-1].Action != "secrets_import" {
		t.Fatalf("the import is audited: %+v", entries)
	}
	detail := entries[len(entries)-1].Detail
	if !strings.Contains(detail, "chrome") || strings.Contains(detail, "fixture-import") {
		t.Fatalf("the audit says the source and the counts, never a value: %q", detail)
	}
}
