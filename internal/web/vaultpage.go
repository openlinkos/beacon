package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/secrets"
)

// The console's vault pages: the collections with a search, one collection's items, one item with
// its versions, who can get it, the reveal (the account password re-entered, every view recorded)
// and the state changes. The page never shows a value by itself: values come out only through the
// reveal, with its password and its record, in the answer of the form just sent.

type vaultLabels struct {
	Sealed, SealedHint, Unseal, Passphrase, Collection, Collections, Items, Item, NewCollection     string
	Level, State, Version, Versions, Fields, Field, Type, Purpose, Notes                            string
	WhoCanGet, WhoCanGetHint, Reveal, RevealPassword, RevealFields, RevealWhy, RevealWhat           string
	Tags, AddTag, SetLevel, Archive, Remove, Restore, Rollback, RollbackTo, Generate, GenerateField string
	Perms, PermsSubject, PermsLevel, PermsSet, Search, SearchPlaceholder, None, KeyVersion          string
	Normal, Archived, Trash, CreatedBy, Reason, Ref, Concealed                                      string
}

func (s *Server) vaultLabels() vaultLabels {
	return vaultLabels{
		Sealed: s.t("The vault is sealed"), SealedHint: s.t("Enter the passphrase to unseal it. Monitoring and the SSH channel carry on while it waits."),
		Unseal: s.t("Unseal"), Passphrase: s.t("Passphrase"),
		Collection: s.t("Collection"), Collections: s.t("Collections"), Items: s.t("Items"), Item: s.t("Item"),
		NewCollection: s.t("New collection"), Level: s.t("Level"), State: s.t("State"), Version: s.t("Version"),
		Versions: s.t("History"), Fields: s.t("Fields"), Field: s.t("Field"), Type: s.t("Type"), Purpose: s.t("Purpose"),
		Notes: s.t("Notes"), WhoCanGet: s.t("Who can get it"),
		WhoCanGetHint: s.t("The people and roles that hold read or more over the collection; the owner holds manage everywhere."),
		Reveal:        s.t("View values"), RevealPassword: s.t("Your password"), RevealFields: s.t("Fields (empty: all)"),
		RevealWhy: s.t("Every view is recorded before the values are shown."), RevealWhat: s.t("Values"),
		Tags: s.t("Tags"), AddTag: s.t("Tags (comma-separated; needs manage)"), SetLevel: s.t("Level (needs manage)"),
		Archive: s.t("Archive"), Remove: s.t("Remove"), Restore: s.t("Restore"), Rollback: s.t("Roll back"),
		RollbackTo: s.t("Roll back to version"), Generate: s.t("Generate a value"), GenerateField: s.t("Field"),
		Perms: s.t("People"), PermsSubject: s.t("user:NAME or role:viewer|oncall|operator|owner"), PermsLevel: s.t("describe, read, edit or manage"),
		PermsSet: s.t("Set"), Search: s.t("Search"), SearchPlaceholder: s.t("Search the vault"),
		None: s.t("None"), KeyVersion: s.t("Key version"), Normal: s.t("Normal"), Archived: s.t("Archived"),
		Trash: s.t("Trash"), CreatedBy: s.t("By"), Reason: s.t("Why"), Ref: s.t("Reference"), Concealed: s.t("hidden until revealed"),
	}
}

// vaultActor maps the signed-in viewer to the vault's actor.
func vaultActorOf(v *viewer) (secrets.Actor, bool) {
	if v == nil || v.Account == nil || !v.Principal.Person() {
		return secrets.Actor{}, false
	}
	return secrets.Actor{Name: v.Account.Username, Via: "console", Role: v.Account.Role}, true
}

type vaultCollectionRow struct {
	Name, Level, Href string
	Items             int
	MayRead           bool
}

type vaultItemRow struct {
	Title, Level, State, Href string
	Version                   int64
	Tags                      string
}

type vaultVersionRow struct {
	Version int64
	Who     string
	When    string
	Why     string
	Fields  string
	Current bool
}

type vaultPage struct {
	shell
	L           vaultLabels
	Sealed      bool
	Mode        string
	BadRows     []string
	CanUnseal   bool
	CanManage   bool
	Collections []vaultCollectionRow
	Tags        []secrets.Tag
	Results     []secrets.SearchResult
	Query       string
	Pages       []vaultLink // the pages beside the vault: approvals, grants, policies, hygiene
}

// vaultLink is one page link under the vault's index.
type vaultLink struct {
	Label string
	Href  string
}

var vaultTmpl = parse("vault.html")

func (s *Server) vaultPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	p := vaultPage{shell: s.shellFor(r, v, "secrets", s.t("Secrets")), L: s.vaultLabels(), Query: r.URL.Query().Get("q")}
	if s.Vault == nil {
		s.render(w, vaultTmpl, p)
		return
	}
	who, _ := vaultActorOf(v)
	p.Sealed, p.Mode, p.BadRows = s.Vault.Sealed(), s.Vault.SealModeName(), s.Vault.Bad()
	p.CanUnseal = p.Sealed && p.Mode == secrets.SealPassphrase && v.has("secrets:admin")
	p.CanManage = v.has("secrets:admin")
	// the pages the secrets work keeps beside the vault, whatever of them is wired
	if s.Secrets != nil {
		p.Pages = append(p.Pages, vaultLink{Label: s.t("Secret approvals"), Href: consolePath("/secrets/approvals")})
		p.Pages = append(p.Pages, vaultLink{Label: s.t("Grants"), Href: consolePath("/secrets/grants")})
		if p.CanManage {
			p.Pages = append(p.Pages, vaultLink{Label: s.t("Entitlements"), Href: consolePath("/secrets/policies")})
		}
	}
	if s.Hygiene != nil {
		p.Pages = append(p.Pages, vaultLink{Label: s.t("Hygiene"), Href: consolePath("/secrets/hygiene")})
	}
	for _, col := range s.Vault.CollectionsFor(who) {
		p.Collections = append(p.Collections, vaultCollectionRow{Name: col.Name, Level: col.Level,
			Href: consolePath("/secrets/" + col.ID), Items: len(s.Vault.Items(col.Name, "")),
			MayRead: s.Vault.May(who, col.ID, secrets.PermRead)})
	}
	p.Tags = s.Vault.Tags()
	if p.Query != "" {
		p.Results = s.Vault.Search(who, p.Query, "", nil)
	}
	s.render(w, vaultTmpl, p)
}

type vaultCollectionPage struct {
	shell
	L                           vaultLabels
	Col                         secrets.Collection
	Items                       []vaultItemRow
	Perms                       []secrets.Permission
	WhoCanGet                   []string
	CanRead, CanEdit, CanManage bool
}

var vaultCollectionTmpl = parse("vaultcollection.html")

func (s *Server) vaultCollectionPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	col, found := s.Vault.Collection(r.PathValue("id"))
	who, _ := vaultActorOf(v)
	if !found || !s.Vault.May(who, col.ID, secrets.PermDescribe) {
		http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
		return
	}
	p := vaultCollectionPage{shell: s.shellFor(r, v, "secrets", s.t("Secrets")+" · "+col.Name, crumb{Label: s.t("Secrets"), Href: consolePath("/secrets")}),
		L: s.vaultLabels(), Col: col}
	p.CanRead = s.Vault.May(who, col.ID, secrets.PermRead)
	p.CanEdit = s.Vault.May(who, col.ID, secrets.PermEdit)
	p.CanManage = s.Vault.May(who, col.ID, secrets.PermManage)
	if p.CanManage {
		p.Perms = s.Vault.Permissions(col.ID)
		p.WhoCanGet = s.Vault.WhoCanGet(col.ID)
	}
	for _, it := range s.Vault.Items(col.Name, r.URL.Query().Get("state")) {
		p.Items = append(p.Items, vaultItemRow{Title: it.Title, Level: s.Vault.EffectiveLevel(it), State: it.State,
			Version: it.Version, Href: consolePath("/secrets/item/" + it.ID), Tags: strings.Join(it.Tags, ", ")})
	}
	s.render(w, vaultCollectionTmpl, p)
}

type vaultFieldRow struct {
	Section, Name, Type, Purpose string
	Value                        string // only after a reveal
}

type vaultItemPage struct {
	shell
	L                           vaultLabels
	Item                        secrets.Item
	Col                         secrets.Collection
	Level                       string
	Ref                         string
	Fields                      []vaultFieldRow
	Versions                    []vaultVersionRow
	WhoCanGet                   []string
	Perms                       []secrets.Permission
	CanRead, CanEdit, CanManage bool
	Values                      map[string]string
	Problem                     string
}

var vaultItemTmpl = parse("vaultitem.html")

func (s *Server) vaultItemPage(w http.ResponseWriter, r *http.Request, v *viewer) {
	item, col, found := s.Vault.Item(r.PathValue("id"))
	if !found {
		http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
		return
	}
	who, _ := vaultActorOf(v)
	if !s.Vault.May(who, col.ID, secrets.PermDescribe) {
		http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
		return
	}
	p := vaultItemPage{shell: s.shellFor(r, v, "secrets", s.t("Secrets")+" · "+item.Title,
		crumb{Label: s.t("Secrets"), Href: consolePath("/secrets")},
		crumb{Label: col.Name, Href: consolePath("/secrets/" + col.ID)}),
		L: s.vaultLabels(), Item: item, Col: col, Level: s.Vault.EffectiveLevel(item),
		WhoCanGet: s.Vault.WhoCanGet(col.ID), Perms: s.Vault.Permissions(col.ID)}
	name := item.Title
	if !secrets.RefNameOK(name) {
		name = item.ID
	}
	p.Ref = secrets.RefOfItem(col.Name, name)
	p.CanRead = s.Vault.May(who, col.ID, secrets.PermRead)
	p.CanEdit = s.Vault.May(who, col.ID, secrets.PermEdit)
	p.CanManage = s.Vault.May(who, col.ID, secrets.PermManage)
	for _, ver := range s.Vault.Versions(item.ID) {
		row := vaultVersionRow{Version: ver.Version, Who: ver.Actor, When: s.clock(ver.At), Why: ver.Reason, Current: ver.Version == item.Version}
		names := []string{}
		for _, f := range ver.Fields {
			names = append(names, f.Name)
		}
		row.Fields = strings.Join(names, ", ")
		p.Versions = append(p.Versions, row)
		if ver.Version == item.Version {
			for _, f := range ver.Fields {
				p.Fields = append(p.Fields, vaultFieldRow{Section: f.Section, Name: f.Name, Type: f.Type, Purpose: f.Purpose})
			}
		}
	}
	q := r.URL.Query()
	if token := q.Get("reveal"); token != "" {
		if _, values, ok := s.takeReveal(token); ok {
			p.Values = values
		} else {
			p.Problem = s.t("The values are no longer there; view them again")
		}
	}
	if msg := q.Get("problem"); msg != "" {
		p.Problem = msg
	}
	s.render(w, vaultItemTmpl, p)
}

// ---- the forms

// vaultFail sends the item page back with the problem said.
func (s *Server) vaultFail(w http.ResponseWriter, r *http.Request, v *viewer, itemID string, err error) {
	msg := err.Error()
	if errors.Is(err, secrets.ErrSealed) {
		msg = s.t("The vault is sealed")
	} else if errors.Is(err, secrets.ErrReauth) {
		msg = s.t("Enter your password to view values")
	} else if errors.Is(err, secrets.ErrForbidden) {
		msg = s.t("You may not do this here")
	}
	to := consolePath("/secrets/item/" + url.PathEscape(itemID))
	if itemID == "" {
		to = consolePath("/secrets")
	}
	http.Redirect(w, r, to+"?problem="+urlQueryEscape(msg), http.StatusSeeOther) //nolint:gosec // G710: within the console's own paths
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

func (s *Server) vaultUnsealForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	pass := r.PostForm.Get("passphrase")
	err := s.Vault.Unseal(pass)
	if errors.Is(err, secrets.ErrNotInitialised) {
		err = s.Vault.InitPassphrase(pass)
	}
	if err != nil {
		s.vaultFail(w, r, v, "", err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
}

func (s *Server) vaultCollectionForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	who, ok := vaultActorOf(v)
	if !ok {
		s.forbidden(w, r, v, "secrets:admin")
		return
	}
	col, err := s.Vault.CreateCollection(who, r.PostForm.Get("name"), r.PostForm.Get("description"), r.PostForm.Get("level"), nil)
	if err != nil {
		s.vaultFail(w, r, v, "", err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/"+col.ID), http.StatusSeeOther)
}

func (s *Server) vaultItemForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	who, _ := vaultActorOf(v)
	item, _, ok := s.Vault.Item(r.PathValue("id"))
	if !ok {
		http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
		return
	}
	in := secrets.ItemIn{ID: item.ID, Collection: item.Collection, Reason: r.PostForm.Get("reason")}
	switch r.PostForm.Get("do") {
	case "tags":
		in.Tags = strings.Split(r.PostForm.Get("tags"), ",")
	case "level":
		in.Level = r.PostForm.Get("level")
	case "field":
		in.Fields = []secrets.FieldIn{{Section: r.PostForm.Get("section"), Name: r.PostForm.Get("name"),
			Type: r.PostForm.Get("type"), Purpose: r.PostForm.Get("purpose"), Value: r.PostForm.Get("value"),
			Clear: r.PostForm.Get("value") == "" && r.PostForm.Get("clear") == "1"}}
	case "notes":
		in.HasNotes, in.Notes = true, r.PostForm.Get("notes")
	default:
		s.vaultFail(w, r, v, item.ID, secrets.ErrBadInput)
		return
	}
	if _, err := s.Vault.SetItem(who, in); err != nil {
		s.vaultFail(w, r, v, item.ID, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/item/"+item.ID), http.StatusSeeOther)
}

func (s *Server) vaultStateForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	who, _ := vaultActorOf(v)
	item, _, ok := s.Vault.Item(r.PathValue("id"))
	if !ok {
		http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
		return
	}
	var err error
	switch r.PostForm.Get("do") {
	case "archive":
		_, err = s.Vault.ArchiveItem(who, item.ID)
	case "rm":
		_, err = s.Vault.TrashItem(who, item.ID)
	case "restore":
		_, err = s.Vault.RestoreItem(who, item.ID)
	case "rollback":
		var version int64
		version, err = strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
		if err == nil {
			_, err = s.Vault.RollbackItem(who, item.ID, version, r.PostForm.Get("reason"))
		}
	case "generate":
		_, err = s.Vault.Generate(who, item.ID, r.PostForm.Get("section"), r.PostForm.Get("field"),
			r.PostForm.Get("reason"), secrets.GenSpec{Charset: r.PostForm.Get("charset")})
	default:
		err = secrets.ErrBadInput
	}
	if err != nil {
		s.vaultFail(w, r, v, item.ID, err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/item/"+item.ID), http.StatusSeeOther)
}

// revealSlot is one reveal's answer, held for the page the redirect opens, by a one-time token:
// values never travel in a URL (a URL lands in histories, logs and Referer headers).
type revealSlot struct {
	item   string
	values map[string]string
	at     time.Time
}

// takeReveal answers the token and forgets it; a token older than a minute is refused.
func (s *Server) takeReveal(token string) (string, map[string]string, bool) {
	s.revealMu.Lock()
	defer s.revealMu.Unlock()
	slot, ok := s.reveals[token]
	if !ok {
		return "", nil, false
	}
	delete(s.reveals, token)
	if time.Since(slot.at) > time.Minute {
		return "", nil, false
	}
	return slot.item, slot.values, true
}

func (s *Server) putReveal(item string, values map[string]string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := hex.EncodeToString(b)
	s.revealMu.Lock()
	if s.reveals == nil {
		s.reveals = map[string]revealSlot{}
	}
	// keep the map small: anything older than a minute goes
	for k, slot := range s.reveals {
		if time.Since(slot.at) > time.Minute {
			delete(s.reveals, k)
		}
	}
	s.reveals[token] = revealSlot{item: item, values: values, at: time.Now()}
	s.revealMu.Unlock()
	return token
}

// vaultRevealForm is the console's door to values: the password goes in, the values are held by a
// one-time token the redirect carries (never the values themselves), and the record was written
// before them. The page says so.
func (s *Server) vaultRevealForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	who, _ := vaultActorOf(v)
	item, _, ok := s.Vault.Item(r.PathValue("id"))
	if !ok {
		http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
		return
	}
	var fields []string
	if f := r.PostForm.Get("fields"); f != "" {
		for _, name := range strings.Split(f, ",") {
			fields = append(fields, strings.TrimSpace(name))
		}
	}
	values, _, err := s.Vault.Reveal(who, item.ID, fields, r.PostForm.Get("password"))
	if err != nil {
		s.vaultFail(w, r, v, item.ID, err)
		return
	}
	token := s.putReveal(item.ID, values)
	http.Redirect(w, r, consolePath("/secrets/item/"+item.ID+"?reveal="+token), http.StatusSeeOther)
}

func (s *Server) vaultPermForm(w http.ResponseWriter, r *http.Request, v *viewer) {
	who, _ := vaultActorOf(v)
	col, ok := s.Vault.Collection(r.PathValue("id"))
	if !ok {
		http.Redirect(w, r, consolePath("/secrets"), http.StatusSeeOther)
		return
	}
	if err := s.Vault.SetPermission(who, col.ID, r.PostForm.Get("subject"), r.PostForm.Get("perm"), r.PostForm.Get("reason")); err != nil {
		s.vaultFail(w, r, v, "", err)
		return
	}
	http.Redirect(w, r, consolePath("/secrets/"+col.ID), http.StatusSeeOther)
}
