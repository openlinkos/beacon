package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/secrets/importer"
)

// The console's import wizard: the export file is uploaded, parsed in memory — never written to
// Beacon's disk — dry-run reported, and imported on the person's confirmation. The parsed batch
// waits for that confirmation in memory, under a one-time token; nothing else ever holds it. The
// report and the pages show counts, categories, levels and conflicts, never a value.

// importLabels is the wizard's own text; what it shares with the vault's pages it takes from
// vaultLabels' strings, so the catalogue holds one entry for each.
type importLabels struct {
	Title, Choose, Source, SourceHint, File, FileHint, Collection, CollectionHint string
	DryRun, Report, New, OTPs, Attachments, AtNever, Categories                   string
	Conflicts, Incoming, AlreadyThere, SharedURLs, ConflictsHint                  string
	OnConflict, Reason, Do                                                        string
	Done, DeleteExport, Followups, FollowupLevels                                 string
	FollowupNever, FollowupEntitlements                                           string
}

func (s *Server) importLabels() importLabels {
	return importLabels{
		Title: s.t("Import secrets"), Choose: s.t("Bring an export in"),
		Source: s.t("Source"), SourceHint: s.t("The op connector runs where you are; use the command line for it."),
		File:                 s.t("Export file"),
		FileHint:             s.t("Parsed in memory, never written to Beacon's disk; delete the export when the import is done."),
		Collection:           s.t("Collection (for chrome, bitwarden and csv)"),
		CollectionHint:       s.t("1PUX brings its own vaults, one a collection."),
		DryRun:               s.t("Look first (dry-run)"),
		Report:               s.t("The report"),
		New:                  s.t("new collection"),
		OTPs:                 s.t("One-time passcodes"),
		Attachments:          s.t("Attachments"),
		AtNever:              s.t("At never"),
		Categories:           s.t("Categories"),
		Conflicts:            s.t("Conflicts"),
		ConflictsHint:        s.t("The same place and the same user name; no value is shown."),
		Incoming:             s.t("Incoming"),
		AlreadyThere:         s.t("Already there"),
		SharedURLs:           s.t("Shared URLs"),
		OnConflict:           s.t("On conflict"),
		Reason:               s.t("Why"),
		Do:                   s.t("Import these items"),
		Done:                 s.t("Imported"),
		DeleteExport:         s.t("Delete the export file now that its contents are in the vault."),
		Followups:            s.t("Follow-ups"),
		FollowupLevels:       s.t("Confirm each collection's level."),
		FollowupNever:        s.t("Check the items started at never."),
		FollowupEntitlements: s.t("Give the agents their entitlements."),
	}
}

// importRow is one collection's line of the report.
type importRow struct {
	Name, Level, Categories string
	New                     bool
	Items, Fields, OTPs     int
	Attachments, Never      int
	Archived, History       int
}

// importConflictRow is one conflict's line.
type importConflictRow struct {
	Collection, Incoming, Existing string
	SharedURLs                     int
}

type vaultImportPage struct {
	shell
	L                importLabels
	VL               vaultLabels
	CanManage        bool
	Report           []importRow
	Conflicts        []importConflictRow
	Token            string
	DryRunDone, Done bool
	CreatedLine      string
	Problem          string
}

var vaultImportTmpl = parse("vaultimport.html")

// vaultImportPage renders the wizard: the upload, or the report of a dry-run, or what an import
// did.
func (s *Server) vaultImportPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	if s.Vault == nil {
		http.Redirect(w, r, consolePath(""), http.StatusSeeOther)
		return
	}
	p := s.importShell(w, r, v)
	if msg := r.URL.Query().Get("problem"); msg != "" {
		p.Problem = msg
	}
	s.sweepImports()
	s.render(w, vaultImportTmpl, p)
}

// importShell is the page's frame.
func (s *Server) importShell(w http.ResponseWriter, r *http.Request, v *viewer) vaultImportPage {
	return vaultImportPage{shell: s.shellFor(r, v, "secrets", s.t("Secrets")+" · "+s.t("Import secrets"),
		crumb{Label: s.t("Secrets"), Href: consolePath("/secrets")}),
		L: s.importLabels(), VL: s.vaultLabels(), CanManage: v.has("secrets:admin")}
}

// vaultImportDryRun takes the upload, parses it in memory, answers the report and holds the batch
// for the confirmation, in memory, under a one-time token.
func (s *Server) vaultImportDryRun(w http.ResponseWriter, r *http.Request, v *viewer) {
	if s.Vault == nil {
		http.Redirect(w, r, consolePath(""), http.StatusSeeOther)
		return
	}
	source := strings.TrimSpace(r.PostForm.Get("source"))
	collection := strings.TrimSpace(r.PostForm.Get("collection"))
	file, _, err := r.FormFile("file")
	if err != nil {
		s.redirectProblem(w, r, s.t("Choose the export file to import."))
		return
	}
	defer file.Close() //nolint:errcheck // the upload's own file, read by the parser
	var batch importer.Batch
	switch source {
	case "1pux":
		batch, err = importer.Parse1PUX(file)
	case "1password-csv":
		batch, err = importer.Parse1PasswordCSV(file, orDefault(collection, "Imports"))
	case "chrome":
		batch, err = importer.ParseChrome(file, orDefault(collection, "Chrome"))
	case "bitwarden":
		batch, err = importer.ParseBitwarden(file, orDefault(collection, "Bitwarden"))
	case "csv":
		if collection == "" {
			s.redirectProblem(w, r, s.t("Name the collection the CSV goes into."))
			return
		}
		batch, err = importer.ParseCSV(file, collection, importer.Columns{})
	default:
		s.redirectProblem(w, r, s.t("Choose where the export comes from."))
		return
	}
	if err != nil {
		s.redirectProblem(w, r, err.Error())
		return
	}
	if len(batch.Collections) == 0 {
		s.redirectProblem(w, r, s.t("The export holds nothing to import."))
		return
	}
	who, ok := vaultActorOf(v)
	if !ok {
		s.forbidden(w, r, v, "secrets:admin")
		return
	}
	report, err := s.Vault.Import(who, batch.ForDryRun(), secrets.ImportOptions{DryRun: true})
	if err != nil {
		s.redirectProblem(w, r, err.Error())
		return
	}
	p := s.importShell(w, r, v)
	p.Report, p.Conflicts = importRows(report), importConflictRows(report)
	p.Token = s.putImport(batch, v.ID)
	p.DryRunDone = true
	s.render(w, vaultImportTmpl, p)
}

// vaultImportCommit runs the import the report showed, by the token the dry-run gave; the batch
// leaves memory with it.
func (s *Server) vaultImportCommit(w http.ResponseWriter, r *http.Request, v *viewer) {
	if s.Vault == nil {
		http.Redirect(w, r, consolePath(""), http.StatusSeeOther)
		return
	}
	batch, ok := s.takeImport(r.PostForm.Get("token"), v.ID)
	if !ok {
		s.redirectProblem(w, r, s.t("The upload is no longer there; start again with the file."))
		return
	}
	who, ok := vaultActorOf(v)
	if !ok {
		s.forbidden(w, r, v, "secrets:admin")
		return
	}
	opts := secrets.ImportOptions{OnConflict: r.PostForm.Get("on_conflict"), Reason: r.PostForm.Get("reason")}
	for key, values := range r.PostForm {
		name, found := strings.CutPrefix(key, "level-")
		if !found || len(values) == 0 || !secrets.ValidLevel(values[0]) {
			continue
		}
		if opts.Levels == nil {
			opts.Levels = map[string]string{}
		}
		opts.Levels[name] = values[0]
	}
	report, err := s.Vault.Import(who, batch, opts)
	if err != nil {
		s.redirectProblem(w, r, err.Error())
		return
	}
	p := s.importShell(w, r, v)
	p.Done = true
	p.CreatedLine = s.t("%d created, %d new versions, %d skipped, %d held back, %d kept both",
		report.Created, report.Versions, report.Skipped, report.HeldBack, report.KeptBoth)
	s.render(w, vaultImportTmpl, p)
}

// redirectProblem sends the wizard back with the problem said.
func (s *Server) redirectProblem(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, consolePath("/secrets/import?problem="+urlQueryEscape(msg)), http.StatusSeeOther) //nolint:gosec // G710: within the console's own paths
}

// importSlot is one parsed batch waiting for its person's confirmation, bound to the session
// that uploaded it.
type importSlot struct {
	batch     importer.Batch
	sessionID string
	at        time.Time
}

const importSlotTTL = 10 * time.Minute

func (s *Server) sweepImports() {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	for k, slot := range s.imports {
		if time.Since(slot.at) > importSlotTTL {
			delete(s.imports, k)
		}
	}
}

// putImport holds a parsed batch under a one-time token for this session, dropping anything
// older than ten minutes so nothing waits long in memory.
func (s *Server) putImport(batch importer.Batch, sessionID string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := hex.EncodeToString(b)
	s.importMu.Lock()
	if s.imports == nil {
		s.imports = map[string]importSlot{}
	}
	for k, slot := range s.imports {
		if time.Since(slot.at) > importSlotTTL {
			delete(s.imports, k)
		}
	}
	s.imports[token] = importSlot{batch: batch, sessionID: sessionID, at: time.Now()}
	s.importMu.Unlock()
	return token
}

// takeImport answers the token for this session and forgets it; a token older than ten minutes,
// or one another session uploaded, is refused.
func (s *Server) takeImport(token, sessionID string) (importer.Batch, bool) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	slot, ok := s.imports[token]
	if !ok || slot.sessionID != sessionID {
		return importer.Batch{}, false
	}
	delete(s.imports, token)
	if time.Since(slot.at) > importSlotTTL {
		return importer.Batch{}, false
	}
	return slot.batch, true
}

// importRows shapes the report for the page.
func importRows(report secrets.ImportReport) []importRow {
	var out []importRow
	for _, col := range report.Collections {
		var cats []string
		for _, c := range col.Categories {
			cats = append(cats, strconv.Itoa(c.Count)+" "+c.Name)
		}
		out = append(out, importRow{Name: col.Name, Level: col.Level, New: col.New, Items: col.Items,
			Fields: col.Fields, OTPs: col.OTPs, Attachments: col.Attachments, Never: col.Never,
			Archived: col.Archived, History: col.History, Categories: strings.Join(cats, ", ")})
	}
	return out
}

func importConflictRows(report secrets.ImportReport) []importConflictRow {
	var out []importConflictRow
	for _, c := range report.Conflicts {
		out = append(out, importConflictRow{Collection: c.Collection, Incoming: c.Incoming,
			Existing: c.Existing, SharedURLs: c.SharedURLs})
	}
	return out
}

// orDefault falls back to the default name.
func orDefault(in, def string) string {
	if strings.TrimSpace(in) != "" {
		return strings.TrimSpace(in)
	}
	return def
}

// importAction guards the wizard's own forms: an upload is multipart, so it takes the CSRF secret
// from the multipart body and reads what an export weighs, where the console's other forms read
// their small bodies.
func (s *Server) importAction(h func(http.ResponseWriter, *http.Request, *viewer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		v := s.viewer(r)
		if v == nil {
			http.Redirect(w, r, consolePath("/sign-in"), http.StatusSeeOther)
			return
		}
		if !v.has("secrets:admin") {
			s.forbidden(w, r, v, "secrets:admin")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
		if err := r.ParseMultipartForm(256 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) { //nolint:gosec // G120: MaxBytesReader already caps the body at 256 MiB, so this stays in memory
			s.message(w, r, v, http.StatusForbidden, s.t("The form has expired"), s.t("Reload the page and try again."))
			return
		}
		if err := r.ParseForm(); err != nil || !sameOrigin(r) || !equal(r.PostForm.Get(csrfField), v.Session.CSRF) {
			s.message(w, r, v, http.StatusForbidden, s.t("The form has expired"), s.t("Reload the page and try again."))
			return
		}
		h(w, r, v)
	}
}
