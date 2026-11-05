package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The fixtures are made up, every one of them: no real export, no real account, no real value.
// The values carry markers, so the gate's leak check (scripts/check-test-leaks.sh) can grep the
// tests' output for them: a marker that reaches a log line, an error or a test's own output is a
// leak.
const (
	fx1PUXPassword   = "fixture-import-1pux-value-xyzzy-frobnicate"
	fx1PUXUsername   = "fixture-import-1pux-user@example.com"
	fx1PUXOTPSeed    = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	fx1PUXNotes      = "fixture-import-1pux-notes-tumbler"
	fx1PUXAttach     = "fixture-import-1pux-attachment-quartz"
	fx1PUXHistory    = "fixture-import-1pux-old-value-grault"
	fxChromePassword = "fixture-import-chrome-value-plugh-quux9"
	fxChromeUsername = "fixture-import-chrome-user@example.com"
	fxBitwardenPass  = "fixture-import-bitwarden-value-cleesh3"
	fxOpPassword     = "fixture-import-op-value-frotz5blorb"
	fxCSVPassword    = "fixture-import-csv-value-waldo-fred7"
)

// fxMarkers are the strings the leak check greps for (scripts/check-test-leaks.sh keeps its own
// copy of this list; both move together).
var fxMarkers = []string{fx1PUXPassword, fx1PUXNotes, fx1PUXAttach, fx1PUXHistory, fxChromePassword,
	fxBitwardenPass, fxOpPassword, fxCSVPassword}

// ---- the 1PUX fixture: a zip with export.attributes, export.data and files/, self-made

// fixture1PUX builds a 1PUX export: two vaults (a personal one and a team one), a login with a
// one-time-passcode seed and a password history, an SSH key, a credit card, an archived passport,
// and an attachment in files/.
func fixture1PUX(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	attrs, _ := json.Marshal(map[string]any{"version": 4, "description": "1Password Unencrypted Export", "createdAt": 1760000000})
	w, _ := zw.Create("export.attributes")
	_, _ = w.Write(attrs)

	docID := "b4q7v8xk2m"
	fileName := "Recovery words.txt"
	data := map[string]any{
		"accounts": []any{map[string]any{
			"attrs": map[string]any{"accountName": "Example", "uuid": "acc1", "domain": "example.1password.com"},
			"vaults": []any{
				map[string]any{
					"attrs": map[string]any{"uuid": "v1", "name": "Private", "desc": "the personal vault", "type": "P"},
					"items": []any{
						map[string]any{
							"uuid": "i1", "state": "active", "categoryUuid": "001",
							"overview": map[string]any{
								"title": "Example Login", "url": "https://www.example.com/login",
								"urls": []any{map[string]any{"label": "Portal", "url": "https://portal.example.com"}},
								"tags": []any{"Work", "env/prod"},
							},
							"details": map[string]any{
								"loginFields": []any{
									map[string]any{"value": fx1PUXUsername, "name": "email", "id": "username", "fieldType": "E", "designation": "username"},
									map[string]any{"value": fx1PUXPassword, "name": "password", "id": "password", "fieldType": "P", "designation": "password"},
									map[string]any{"value": "fixture-import-1pux-remember-me", "name": "remember", "id": "rm", "fieldType": "C"},
								},
								"notesPlain": fx1PUXNotes,
								"sections": []any{
									map[string]any{"title": "Security", "name": "security", "fields": []any{
										map[string]any{"title": "one-time password", "id": "totp", "value": map[string]any{"totp": fx1PUXOTPSeed}},
										map[string]any{"title": "Backup date", "id": "bkdate", "value": map[string]any{"date": 1735689600}},
									}},
								},
								"passwordHistory": []any{
									map[string]any{"value": fx1PUXHistory, "time": 1700000000},
									map[string]any{"value": fx1PUXPassword, "time": 1750000000},
								},
								"documentAttributes": map[string]any{"fileName": fileName, "documentId": docID, "decryptedSize": len(fx1PUXAttach)},
							},
						},
						map[string]any{
							"uuid": "i2", "state": "active", "categoryUuid": "005",
							"overview": map[string]any{"title": "Router admin", "url": "https://192.168.1.1"},
							"details": map[string]any{
								"password": fx1PUXPassword,
								"sections": []any{map[string]any{"title": "WLAN", "fields": []any{
									map[string]any{"title": "SSID", "id": "ssid", "value": map[string]any{"string": "fixture-import-ssid"}},
								}}},
							},
						},
					},
				},
				map[string]any{
					"attrs": map[string]any{"uuid": "v2", "name": "Engineering", "desc": "the team vault", "type": "E"},
					"items": []any{
						map[string]any{
							"uuid": "i3", "state": "active", "categoryUuid": "114",
							"overview": map[string]any{"title": "Deploy key"},
							"details": map[string]any{
								"sections": []any{map[string]any{"title": "Key", "fields": []any{
									map[string]any{"title": "private key", "id": "pk", "value": map[string]any{"sshKey": map[string]any{
										"privateKey": "fixture-import-ssh-private-key",
										"metadata":   map[string]any{"publicKey": "fixture-import-ssh-public", "fingerprint": "SHA256:fixture", "keyType": "ed25519"},
									}}},
								}}},
							},
						},
						map[string]any{
							"uuid": "i4", "state": "active", "categoryUuid": "002",
							"overview": map[string]any{"title": "Company card"},
							"details": map[string]any{
								"sections": []any{map[string]any{"title": "Card", "fields": []any{
									map[string]any{"title": "card number", "id": "ccnum", "value": map[string]any{"creditCardNumber": "fixture-import-card-number"}},
									map[string]any{"title": "valid from", "id": "ccfrom", "value": map[string]any{"monthYear": 202501}},
								}}},
							},
						},
						map[string]any{
							"uuid": "i5", "state": "archived", "categoryUuid": "106",
							"overview": map[string]any{"title": "Old passport"},
							"details": map[string]any{"sections": []any{map[string]any{"title": "Passport", "fields": []any{
								map[string]any{"title": "number", "id": "pn", "value": map[string]any{"concealed": "fixture-import-passport-number"}},
							}}}},
						},
					},
				},
			},
		}},
	}
	raw, _ := json.Marshal(data)
	w, _ = zw.Create("export.data")
	_, _ = w.Write(raw)
	w, _ = zw.Create("files/" + docID + "___" + fileName)
	_, _ = w.Write([]byte(fx1PUXAttach))
	w, _ = zw.Create("files/icon.png")
	_, _ = w.Write([]byte("not an attachment the import reads"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParse1PUX(t *testing.T) {
	batch, err := Parse1PUX(bytes.NewReader(fixture1PUX(t)))
	if err != nil {
		t.Fatal(err)
	}
	if batch.Source != Source1Password {
		t.Fatalf("source %q", batch.Source)
	}
	if len(batch.Collections) != 2 {
		t.Fatalf("two vaults, got %d collections", len(batch.Collections))
	}
	priv, eng := batch.Collections[0], batch.Collections[1]
	if priv.Name != "Private" || priv.LevelKey != "1password:private" {
		t.Fatalf("the personal vault keyed by name: %q %q", priv.Name, priv.LevelKey)
	}
	if eng.Name != "Engineering" || eng.LevelKey != Source1Password {
		t.Fatalf("the team vault keyed as the source: %q %q", eng.Name, eng.LevelKey)
	}
	if len(priv.Items) != 2 || len(eng.Items) != 3 {
		t.Fatalf("items: private %d, engineering %d", len(priv.Items), len(eng.Items))
	}
	login := priv.Items[0]
	if login.Title != "Example Login" || login.Category != CatLogin || login.Source != "1password:i1" {
		t.Fatalf("the login: %+v", login)
	}
	if len(login.URLs) != 2 {
		t.Fatalf("the login's URLs: %v", login.URLs)
	}
	// the tags: lower-cased as they came, plus the source tag
	want := []string{"env/prod", "source/1password", "work"}
	if strings.Join(login.Tags, ",") != strings.Join(want, ",") {
		t.Fatalf("tags: %v", login.Tags)
	}
	var user, pass, otp, date, remember *FieldImport
	for i, f := range login.Fields {
		switch {
		case f.Purpose == PurposeUsername:
			user = &login.Fields[i]
		case f.Purpose == PurposePassword:
			pass = &login.Fields[i]
		case f.Type == TypeOTP:
			otp = &login.Fields[i]
		case f.Type == TypeDate:
			date = &login.Fields[i]
		case f.Name == "remember":
			remember = &login.Fields[i]
		}
	}
	if user == nil || user.Value != fx1PUXUsername || user.Type != TypeString {
		t.Fatalf("the designated username: %+v", user)
	}
	if pass == nil || pass.Value != fx1PUXPassword || pass.Type != TypeConcealed {
		t.Fatalf("the designated password: %+v", pass)
	}
	if otp == nil || otp.Section != "Security" || otp.Name != "one-time password" || otp.Value != fx1PUXOTPSeed {
		t.Fatalf("the one-time-passcode seed: %+v", otp)
	}
	if date == nil || date.Value != "2025-01-01" {
		t.Fatalf("the date value: %+v", date)
	}
	if remember == nil || remember.Type != TypeString {
		t.Fatalf("the saved form field: %+v", remember)
	}
	if login.Notes != fx1PUXNotes {
		t.Fatalf("notes: %q", login.Notes)
	}
	if len(login.History) != 2 || !login.History[0].At.Before(login.History[1].At) {
		t.Fatalf("the history, oldest first: %+v", login.History)
	}
	if login.History[0].Value != fx1PUXHistory {
		t.Fatalf("the older value: %+v", login.History[0])
	}
	if len(login.Attachments) != 1 || login.Attachments[0].Name != "Recovery words.txt" || string(login.Attachments[0].Data) != fx1PUXAttach {
		t.Fatalf("the attachment: %+v", login.Attachments)
	}
	// the password category keeps its password where 1Password keeps it
	pw := priv.Items[1]
	if pw.Category != CatPassword {
		t.Fatalf("the router's category: %q", pw.Category)
	}
	found := false
	for _, f := range pw.Fields {
		if f.Purpose == PurposePassword && f.Value == fx1PUXPassword {
			found = true
		}
	}
	if !found {
		t.Fatalf("the password on its own: %+v", pw.Fields)
	}
	// the SSH key: the private key concealed, the metadata beside it
	key := eng.Items[0]
	if key.Category != CatSSHKey {
		t.Fatalf("the key's category: %q", key.Category)
	}
	if len(key.Fields) != 4 || key.Fields[0].Type != TypeConcealed || key.Fields[0].Value != "fixture-import-ssh-private-key" {
		t.Fatalf("the key's fields: %+v", key.Fields)
	}
	// the credit card and the archived passport
	if eng.Items[1].Category != CatCreditCard {
		t.Fatalf("the card's category: %q", eng.Items[1].Category)
	}
	for _, f := range eng.Items[1].Fields {
		if f.Name == "card number" && (f.Type != TypeConcealed || f.Value != "fixture-import-card-number") {
			t.Fatalf("the card number: %+v", f)
		}
		if f.Name == "valid from" && f.Value != "2025-01" {
			t.Fatalf("the month-year: %+v", f)
		}
	}
	if eng.Items[2].State != StateArchived || eng.Items[2].Category != CatPassport {
		t.Fatalf("the archived passport: %+v", eng.Items[2])
	}
}

func TestParse1PUXRejectsAZiplessFile(t *testing.T) {
	if _, err := Parse1PUX(strings.NewReader("not a zip")); err == nil {
		t.Fatal("a file that is not a zip is refused")
	}
}

func TestParse1PUXRefusesAnOversizedEntry(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("export.data")
	_, _ = w.Write([]byte(`{"accounts":[{"vaults":[{"attrs":{"name":"Private","type":"P"},"items":[
	  {"uuid":"i1","categoryUuid":"006","overview":{"title":"Doc"},
	   "details":{"documentAttributes":{"fileName":"big.bin","documentId":"bomb"}}}]}]}]}`))
	w, _ = zw.Create("files/bomb___big.bin")
	_, _ = w.Write(bytes.Repeat([]byte("0"), 65<<20))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse1PUX(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("an oversized zip entry must be refused")
	}
}

func TestTitleOfCutsOnRuneBoundary(t *testing.T) {
	han := strings.Repeat("密", 50)
	got := titleOf(han, "")
	if got != han {
		t.Fatalf("fifty han characters fit: %q", got)
	}
	got = titleOf(han+"\xff", "")
	if got != han {
		t.Fatalf("invalid bytes are dropped: %q", got)
	}
}

// ---- Chrome

const chromeFixture = `name,url,username,password,note
Portal,https://www.example.com/signin,` + fxChromeUsername + `,` + fxChromePassword + `,the main one
Portal,https://portal.example.com,` + fxChromeUsername + `,` + fxChromePassword + `,same credential
Shop,https://shop.example.net,` + fxChromeUsername + `,` + fxChromePassword + `,
Shop,https://shop.example.net/account,` + fxChromeUsername + `,` + fxChromePassword + `,
Mail,https://mail.example.org,` + fxChromeUsername + `,fixture-import-chrome-other-value-2,
`

func TestParseChromeMergesTheSameCredential(t *testing.T) {
	batch, err := ParseChrome(strings.NewReader(chromeFixture), "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Collections) != 1 || batch.Collections[0].Name != "Chrome" || batch.Collections[0].LevelKey != "chrome" {
		t.Fatalf("one named collection: %+v", batch.Collections)
	}
	items := batch.Collections[0].Items
	// five rows, two credentials: one across three sites (one row the same site again, which the
	// merge counts), one with another password of its own
	if len(items) != 2 {
		t.Fatalf("two items after the merge, got %d: %+v", len(items), items)
	}
	if batch.Merged != 1 {
		t.Fatalf("the same site saved twice counts as merged: %d", batch.Merged)
	}
	// every item: the username, the password, the source tag, the login category
	for _, it := range items {
		if it.Category != CatLogin || it.Source != "chrome" {
			t.Fatalf("chrome's items are logins: %+v", it)
		}
		var user, pass *FieldImport
		for i, f := range it.Fields {
			if f.Purpose == PurposeUsername {
				user = &it.Fields[i]
			}
			if f.Purpose == PurposePassword {
				pass = &it.Fields[i]
			}
		}
		if user == nil || user.Value != fxChromeUsername {
			t.Fatalf("the username: %+v", user)
		}
		if pass == nil {
			t.Fatal("the password")
		}
		if !contains(it.Tags, "source/chrome") {
			t.Fatalf("the source tag: %v", it.Tags)
		}
	}
	// the shared credential holds all three sites, and only those
	var shared *ItemImport
	for i := range items {
		if len(items[i].URLs) == 3 {
			shared = &items[i]
		}
	}
	if shared == nil {
		t.Fatalf("one item holds all three sites: %+v", items)
	}
	if shared.Fields[1].Value != fxChromePassword {
		t.Fatalf("the shared password: %+v", shared.Fields)
	}
	// the other password is an item of its own
	var other *ItemImport
	for i := range items {
		if items[i].URLs[0] == "https://mail.example.org" {
			other = &items[i]
		}
	}
	if other == nil || other.Fields[1].Value == fxChromePassword {
		t.Fatalf("another password is another item: %+v", other)
	}
}

// ---- 1Password CSV

func TestParse1PasswordCSV(t *testing.T) {
	// the columns in another order and under other names than the export's usual header, to check
	// the header is read, not assumed
	const csv = `Account,Login URL,Password,Notes,Type,Tags,OTP Auth
Example Service,https://api.example.com,` + fxCSVPassword + `,the notes,Login,work; prod,` + "otpauth://totp/Example:user?secret=" + fx1PUXOTPSeed + "&issuer=Example" + `
`
	batch, err := Parse1PasswordCSV(strings.NewReader(csv), "Imports")
	if err != nil {
		t.Fatal(err)
	}
	items := batch.Collections[0].Items
	if len(items) != 1 {
		t.Fatalf("one item: %+v", items)
	}
	it := items[0]
	if it.Title != "Example Service" || it.Category != CatLogin || it.URLs[0] != "https://api.example.com" {
		t.Fatalf("the item: %+v", it)
	}
	var pass, otp *FieldImport
	for i, f := range it.Fields {
		if f.Purpose == PurposePassword {
			pass = &it.Fields[i]
		}
		if f.Type == TypeOTP {
			otp = &it.Fields[i]
		}
	}
	if pass == nil || pass.Value != fxCSVPassword {
		t.Fatalf("the password: %+v", pass)
	}
	if otp == nil || otp.Value != fx1PUXOTPSeed {
		t.Fatalf("the seed out of the otpauth URI: %+v", otp)
	}
	if !contains(it.Tags, "prod") || !contains(it.Tags, "source/1password") {
		t.Fatalf("the tags: %v", it.Tags)
	}
	if it.Notes != "the notes" {
		t.Fatalf("the notes: %q", it.Notes)
	}
}

// ---- Bitwarden

func TestParseBitwarden(t *testing.T) {
	const doc = `{"encrypted":false,
	 "folders":[{"id":"f1","name":"Servers"}],
	 "items":[
	  {"id":"b1","folderId":"f1","type":1,"name":"Edge node","notes":"the notes",
	   "login":{"username":"fixture-import-bw-user","password":"` + fxBitwardenPass + `","totp":"` + fx1PUXOTPSeed + `",
	            "uris":[{"uri":"https://edge.example.com"}]},
	   "fields":[{"name":"region","value":"eu-1","type":0},{"name":"token","value":"fixture-import-bw-hidden","type":1}]},
	  {"id":"b2","type":3,"name":"Team card","card":{"cardholderName":"Example Person","brand":"Visa","number":"fixture-import-bw-card","expMonth":"3","expYear":"27","code":"fixture-import-bw-cvc"}},
	  {"id":"b3","type":2,"name":"Runbook","notes":"how it runs"}
	 ]}`
	batch, err := ParseBitwarden(strings.NewReader(doc), "Bitwarden")
	if err != nil {
		t.Fatal(err)
	}
	items := batch.Collections[0].Items
	if len(items) != 3 {
		t.Fatalf("three items: %+v", items)
	}
	login := items[0]
	if login.Category != CatLogin || login.Title != "Edge node" {
		t.Fatalf("the login: %+v", login)
	}
	if !contains(login.Tags, "servers") || !contains(login.Tags, "source/bitwarden") {
		t.Fatalf("the folder as a tag, plus the source: %v", login.Tags)
	}
	if len(login.URLs) != 1 || login.URLs[0] != "https://edge.example.com" {
		t.Fatalf("the URIs: %v", login.URLs)
	}
	var pass, otp, hidden *FieldImport
	for i, f := range login.Fields {
		switch {
		case f.Purpose == PurposePassword:
			pass = &login.Fields[i]
		case f.Type == TypeOTP:
			otp = &login.Fields[i]
		case f.Name == "token":
			hidden = &login.Fields[i]
		}
	}
	if pass == nil || pass.Value != fxBitwardenPass {
		t.Fatalf("the password: %+v", pass)
	}
	if otp == nil || otp.Value != fx1PUXOTPSeed {
		t.Fatalf("the seed: %+v", otp)
	}
	if hidden == nil || hidden.Type != TypeConcealed {
		t.Fatalf("a hidden custom field is concealed: %+v", hidden)
	}
	card := items[1]
	if card.Category != CatCreditCard {
		t.Fatalf("the card's category: %q", card.Category)
	}
	for _, f := range card.Fields {
		if f.Name == "number" && f.Value != "fixture-import-bw-card" {
			t.Fatalf("the card number: %+v", f)
		}
		if f.Name == "expiry" && f.Value != "2027-03" {
			t.Fatalf("the expiry: %+v", f)
		}
	}
	if items[2].Category != CatSecureNote || items[2].Notes != "how it runs" {
		t.Fatalf("the note: %+v", items[2])
	}
}

func TestParseBitwardenRefusesTheEncryptedExport(t *testing.T) {
	if _, err := ParseBitwarden(strings.NewReader(`{"encrypted":true,"items":[]}`), "Bitwarden"); err == nil {
		t.Fatal("the encrypted export is refused with a plain error")
	}
}

// ---- a plain CSV with the columns named

func TestParseCSVWithColumns(t *testing.T) {
	const csv = `service,who,secret,extra,kind
Example API,fixture-import-csv-user,` + fxCSVPassword + `,ignored,api credential
`
	batch, err := ParseCSV(strings.NewReader(csv), "Sheet", Columns{Title: "service", Username: "who", Password: "secret", Category: "kind"})
	if err != nil {
		t.Fatal(err)
	}
	items := batch.Collections[0].Items
	if len(items) != 1 {
		t.Fatalf("one item: %+v", items)
	}
	it := items[0]
	if it.Title != "Example API" || it.Category != CatAPICredential || it.Source != "csv" {
		t.Fatalf("the item: %+v", it)
	}
	if it.Fields[0].Value != "fixture-import-csv-user" || it.Fields[1].Value != fxCSVPassword {
		t.Fatalf("the fields: %+v", it.Fields)
	}
	// a column nothing claims is ignored
	for _, f := range it.Fields {
		if f.Name == "extra" {
			t.Fatalf("the unmapped column came along: %+v", it.Fields)
		}
	}
}

func TestParseCSVGuessesTheHeader(t *testing.T) {
	const csv = `name,url,username,password
Example,https://example.com,user@example.com,fixture-import-csv-guess-1
`
	batch, err := ParseCSV(strings.NewReader(csv), "Sheet", Columns{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Collections[0].Items) != 1 || batch.Collections[0].Items[0].Title != "Example" {
		t.Fatalf("the guessed item: %+v", batch.Collections[0].Items)
	}
}

// ---- the op connector, over a stub

// opStub answers the connector's three commands with a small account: two vaults, three items.
type opStub struct{ calls int }

func (s *opStub) Run(_ context.Context, args ...string) ([]byte, error) {
	s.calls++
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "vault list"):
		return []byte(`[{"id":"v1","name":"Private","attributes":{"name":"Private","type":"P"}},{"id":"v2","name":"Engineering","attributes":{"name":"Engineering","type":"E"}}]`), nil
	case strings.HasPrefix(strings.Join(args, " "), "item list --vault v1"):
		return []byte(`[{"id":"i1","title":"Example Login","category":"login","vault":{"id":"v1","name":"Private"}}]`), nil
	case strings.HasPrefix(strings.Join(args, " "), "item list --vault v2"):
		return []byte(`[{"id":"i2","title":"Example Database","category":"database","vault":{"id":"v2","name":"Engineering"}},{"id":"i3","title":"Archived note","category":"secure_note","vault":{"id":"v2","name":"Engineering"},"state":"ARCHIVED"}]`), nil
	case strings.HasPrefix(strings.Join(args, " "), "item get i1"):
		return []byte(fmt.Sprintf(`{"id":"i1","title":"Example Login","category":"LOGIN","state":"ACTIVE",
		 "sections":[{"id":"sec1","label":"Security"}],
		 "fields":[
		  {"id":"username","label":"username","value":"fixture-import-op-user","type":"STRING","purpose":"USERNAME"},
		  {"id":"password","label":"password","value":"%s","type":"CONCEALED","purpose":"PASSWORD"},
		  {"id":"totp","label":"one-time password","value":"%s","type":"OTP","section":{"id":"sec1"}},
		  {"id":"notes","label":"notes","value":"the op notes","type":"STRING"},
		  {"id":"url","label":"website","value":"https://op.example.com","type":"URL"}]}`, fxOpPassword, fx1PUXOTPSeed)), nil
	case strings.HasPrefix(strings.Join(args, " "), "item get i2"):
		return []byte(`{"id":"i2","title":"Example Database","category":"DATABASE","fields":[
		  {"id":"username","label":"username","value":"dbuser","type":"STRING","purpose":"USERNAME"},
		  {"id":"password","label":"password","value":"fixture-import-op-db-pass","type":"CONCEALED","purpose":"PASSWORD"}]}`), nil
	default:
		return []byte(`{"id":"i3","title":"Archived note","category":"SECURE_NOTE","state":"ARCHIVED","fields":[{"id":"notes","label":"notes","value":"an archived note","type":"STRING"}]}`), nil
	}
}

func TestParseOp(t *testing.T) {
	stub := &opStub{}
	batch, err := (OpConnector{Run: stub}).ParseOp(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Collections) != 2 {
		t.Fatalf("two vaults: %+v", batch.Collections)
	}
	var priv, eng *CollectionImport
	for i := range batch.Collections {
		switch batch.Collections[i].Name {
		case "Private":
			priv = &batch.Collections[i]
		case "Engineering":
			eng = &batch.Collections[i]
		}
	}
	if priv == nil || eng == nil {
		t.Fatalf("both vaults: %+v", batch.Collections)
	}
	if priv.LevelKey != "1password:private" {
		t.Fatalf("the personal vault: %+v", priv)
	}
	if len(priv.Items) != 1 || len(eng.Items) != 2 {
		t.Fatalf("the items: %d %d", len(priv.Items), len(eng.Items))
	}
	login := priv.Items[0]
	if login.Category != CatLogin || login.Source != "1password:i1" {
		t.Fatalf("the login: %+v", login)
	}
	var pass, otp, notes *FieldImport
	for i, f := range login.Fields {
		switch {
		case f.Purpose == PurposePassword:
			pass = &login.Fields[i]
		case f.Type == TypeOTP:
			otp = &login.Fields[i]
		case f.Name == "notes":
			notes = &login.Fields[i]
		}
	}
	if pass == nil || pass.Value != fxOpPassword {
		t.Fatalf("the password: %+v", pass)
	}
	if otp == nil || otp.Section != "Security" || otp.Value != fx1PUXOTPSeed {
		t.Fatalf("the seed in its section: %+v", otp)
	}
	if notes != nil {
		t.Fatalf("the notes field is the item's notes, not a field: %+v", notes)
	}
	if login.Notes != "the op notes" {
		t.Fatalf("the notes: %q", login.Notes)
	}
	if len(login.URLs) != 1 || login.URLs[0] != "https://op.example.com" {
		t.Fatalf("the URL fields are the item's URLs: %v", login.URLs)
	}
	if eng.Items[1].State != StateArchived {
		t.Fatalf("the archived item: %+v", eng.Items[1])
	}
	// one vault list, one list a vault, one get an item
	if stub.calls != 1+2+3 {
		t.Fatalf("the connector asks op for each piece: %d calls", stub.calls)
	}
}

func TestParseOpPicksTheVaultsNamed(t *testing.T) {
	stub := &opStub{}
	batch, err := (OpConnector{Run: stub}).ParseOp(context.Background(), []string{"engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Collections) != 1 || batch.Collections[0].Name != "Engineering" {
		t.Fatalf("the vault named: %+v", batch.Collections)
	}
}

// ---- the shared shapes

func TestNormalizeURL(t *testing.T) {
	same := [][2]string{
		{"https://www.Example.com/login?a=1#top", "https://example.com"},
		{"HTTPS://Example.com:443/", "https://example.com"},
		{"http://example.com:80/x", "http://example.com"},
		{"https://example.com", "https://example.com"},
	}
	for _, pair := range same {
		if got := NormalizeURL(pair[0]); got != pair[1] {
			t.Fatalf("NormalizeURL(%q) = %q, want %q", pair[0], got, pair[1])
		}
	}
	if got := NormalizeURL("android://x@com.example.app"); got != "android://com.example.app" {
		t.Fatalf("the app's host is the part that counts: %q", got)
	}
}

func TestForDryRunStripsTheValues(t *testing.T) {
	batch, err := Parse1PUX(bytes.NewReader(fixture1PUX(t)))
	if err != nil {
		t.Fatal(err)
	}
	dry := batch.ForDryRun()
	raw, err := json.Marshal(dry)
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, marker := range fxMarkers {
		if strings.Contains(out, marker) {
			t.Fatalf("the dry-run batch leaks a value: %s appears in its JSON", marker)
		}
	}
	if strings.Contains(out, fx1PUXOTPSeed) {
		t.Fatal("the dry-run batch leaks a one-time-passcode seed")
	}
	// the user names stay: the conflict rule turns them over, and no report echoes them
	if !strings.Contains(out, fx1PUXUsername) {
		t.Fatal("the dry-run batch keeps the user names, which the conflict rule needs")
	}
	// the shapes stay, so the report can count
	login := dry.Collections[0].Items[0]
	if len(login.Fields) == 0 || len(login.Attachments) != 1 || login.Attachments[0].Data != nil {
		t.Fatalf("the dry-run keeps the shapes and drops the bytes: %+v", login)
	}
	if len(login.History) != 2 || login.History[0].Value != "" {
		t.Fatalf("the dry-run keeps the history's count, not its values: %+v", login.History)
	}
}

func TestIntoCollectionFolds(t *testing.T) {
	batch := Batch{Source: SourceChrome, Collections: []CollectionImport{
		{Name: "A", LevelKey: "chrome", Items: []ItemImport{{Title: "One"}}},
		{Name: "B", Items: []ItemImport{{Title: "Two"}}},
	}}
	out := batch.IntoCollection("Chrome")
	if len(out.Collections) != 1 || out.Collections[0].Name != "Chrome" || len(out.Collections[0].Items) != 2 {
		t.Fatalf("folded: %+v", out)
	}
	if out.Collections[0].LevelKey != "chrome" {
		t.Fatalf("the source's level key survives the fold: %q", out.Collections[0].LevelKey)
	}
}

func TestHistoryTimesComeSorted(t *testing.T) {
	// the parser sorts what the export gives; a later value before an older one is put right
	batch, err := Parse1PUX(bytes.NewReader(fixture1PUX(t)))
	if err != nil {
		t.Fatal(err)
	}
	h := batch.Collections[0].Items[0].History
	if !h[0].At.Before(h[1].At) || time.Unix(1700000000, 0).UTC() != h[0].At.UTC() {
		t.Fatalf("oldest first with its own time: %+v", h)
	}
}
