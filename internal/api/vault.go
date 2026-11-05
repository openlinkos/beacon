package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

// The vault's routes: the collections with their items and versions, the tags, the people
// permissions, the reveal with its password re-check, the key events (seal, unseal, rotate) and
// the search. People work the vault here and in the console; agents reach items through the grants
// a later package brings, so every route answers an agent with a plain refusal until then.

func vaultRoutes() []string {
	return []string{
		"GET /api/v1/vault", "POST /api/v1/vault/init", "POST /api/v1/vault/unseal", "POST /api/v1/vault/seal", "POST /api/v1/vault/rotate-key",
		"POST /api/v1/vault/rotate-collection-key",
		"GET /api/v1/vault/collections", "POST /api/v1/vault/collections",
		"GET /api/v1/vault/collections/{id}", "PUT /api/v1/vault/collections/{id}",
		"GET /api/v1/vault/collections/{id}/items", "POST /api/v1/vault/collections/{id}/items",
		"GET /api/v1/vault/collections/{id}/perms", "POST /api/v1/vault/collections/{id}/perms",
		"GET /api/v1/vault/items/{id}", "PUT /api/v1/vault/items/{id}", "DELETE /api/v1/vault/items/{id}",
		"POST /api/v1/vault/items/{id}/archive", "POST /api/v1/vault/items/{id}/restore",
		"POST /api/v1/vault/items/{id}/rollback", "POST /api/v1/vault/items/{id}/generate",
		"POST /api/v1/vault/items/{id}/reveal",
		"GET /api/v1/vault/items/{id}/versions/{version}/attachments/{name}",
		"GET /api/v1/vault/search", "GET /api/v1/vault/tags", "POST /api/v1/vault/tags", "DELETE /api/v1/vault/tags/{name}",
	}
}

func init() { Routes = append(Routes, vaultRoutes()...) }

// vault is the interface the server holds (nil: no vault), so tests can stand in.
type vault interface {
	Sealed() bool
	SealModeName() string
	Bad() []string
	Unseal(passphrase string) error
	InitPassphrase(passphrase string) error
	Seal() error
	RotateRootKey(newPassphrase string) error
	RotateCollectionKey(collection string) error
	OnKeyChange(func([]byte))

	Collections() []secrets.Collection
	Collection(nameOrID string) (secrets.Collection, bool)
	CreateCollection(who secrets.Actor, name, description, level string) (secrets.Collection, error)
	UpdateCollection(who secrets.Actor, id, description, level string) (secrets.Collection, error)
	Permissions(collection string) []secrets.Permission
	SetPermission(who secrets.Actor, collection, subject, perm, reason string) error

	Items(collection, state string) []secrets.Item
	Item(ref string) (secrets.Item, secrets.Collection, bool)
	Versions(item string) []secrets.ItemVersion
	SetItem(who secrets.Actor, in secrets.ItemIn) (secrets.Item, error)
	ArchiveItem(who secrets.Actor, item string) (secrets.Item, error)
	TrashItem(who secrets.Actor, item string) (secrets.Item, error)
	RestoreItem(who secrets.Actor, item string) (secrets.Item, error)
	RollbackItem(who secrets.Actor, item string, version int64, reason string) (secrets.Item, error)
	Generate(who secrets.Actor, item, section, field, reason string, spec secrets.GenSpec) (secrets.Item, error)
	Reveal(who secrets.Actor, item string, fields []string, password string) (map[string]string, []secrets.Field, error)
	Attachment(who secrets.Actor, item string, version int64, name string) ([]byte, int64, error)

	Tags() []secrets.Tag
	SetTag(who secrets.Actor, name, level, description string) (secrets.Tag, error)
	DeleteTag(who secrets.Actor, name string) error
	EffectiveLevel(item secrets.Item) string
	WhoCanGet(collection string) []string
	Search(who secrets.Actor, query, collection string, tags []string) []secrets.SearchResult
	May(who secrets.Actor, collection, perm string) bool
}

func (s *Server) registerVault(mux *http.ServeMux) {
	if s.Vault == nil {
		return
	}
	mux.Handle("GET /api/v1/vault", s.with("", s.vaultState))
	mux.Handle("POST /api/v1/vault/init", s.with("secrets:admin", s.vaultInit))
	mux.Handle("POST /api/v1/vault/unseal", s.with("secrets:admin", s.vaultUnseal))
	mux.Handle("POST /api/v1/vault/seal", s.with("secrets:admin", s.vaultSeal))
	mux.Handle("POST /api/v1/vault/rotate-key", s.with("secrets:admin", s.vaultRotateKey))
	mux.Handle("POST /api/v1/vault/rotate-collection-key", s.with("secrets:admin", s.vaultRotateCollectionKey))
	mux.Handle("GET /api/v1/vault/collections", s.withPrincipal("", s.listCollections))
	mux.Handle("POST /api/v1/vault/collections", s.with("secrets:admin", s.createCollection))
	mux.Handle("GET /api/v1/vault/collections/{id}", s.withPrincipal("", s.getCollection))
	mux.Handle("PUT /api/v1/vault/collections/{id}", s.with("secrets:admin", s.putCollection))
	mux.Handle("GET /api/v1/vault/collections/{id}/items", s.withPrincipal("", s.listItems))
	mux.Handle("POST /api/v1/vault/collections/{id}/items", s.withPrincipal("", s.postItem))
	mux.Handle("GET /api/v1/vault/collections/{id}/perms", s.withPrincipal("", s.listPerms))
	mux.Handle("POST /api/v1/vault/collections/{id}/perms", s.withPrincipal("", s.postPerm))
	mux.Handle("GET /api/v1/vault/items/{id}", s.withPrincipal("", s.getItem))
	mux.Handle("PUT /api/v1/vault/items/{id}", s.withPrincipal("", s.putItem))
	mux.Handle("DELETE /api/v1/vault/items/{id}", s.withPrincipal("", s.trashItem))
	mux.Handle("POST /api/v1/vault/items/{id}/archive", s.withPrincipal("", s.archiveItem))
	mux.Handle("POST /api/v1/vault/items/{id}/restore", s.withPrincipal("", s.restoreItem))
	mux.Handle("POST /api/v1/vault/items/{id}/rollback", s.withPrincipal("", s.rollbackItem))
	mux.Handle("POST /api/v1/vault/items/{id}/generate", s.withPrincipal("", s.generateItem))
	mux.Handle("POST /api/v1/vault/items/{id}/reveal", s.withPrincipal("", s.revealItem))
	mux.Handle("GET /api/v1/vault/items/{id}/versions/{version}/attachments/{name}", s.withPrincipal("", s.getAttachment))
	mux.Handle("GET /api/v1/vault/search", s.withPrincipal("", s.searchVault))
	mux.Handle("GET /api/v1/vault/tags", s.withPrincipal("", s.listTags))
	mux.Handle("POST /api/v1/vault/tags", s.withPrincipal("", s.postTag))
	mux.Handle("DELETE /api/v1/vault/tags/{name}", s.withPrincipal("", s.deleteTag))
}

// vaultActor maps a principal to the vault's actor: a person (with their role) or nothing — an
// agent is refused, and a machine's token is not a person either.
func (s *Server) vaultActor(p store.Principal) (secrets.Actor, bool) {
	if !p.Person() {
		return secrets.Actor{}, false
	}
	acc, ok := s.Auth.Store.Account(p.User)
	if !ok || acc.Disabled {
		return secrets.Actor{}, false
	}
	return secrets.Actor{Name: p.User, Via: "api", Role: acc.Role}, true
}

// needPerson answers the agent refusal when the caller is not a person.
func (s *Server) needPerson(w http.ResponseWriter, p store.Principal) (secrets.Actor, bool) {
	who, ok := s.vaultActor(p)
	if !ok {
		if p.Agent != "" {
			writeError(w, http.StatusForbidden, "secrets.agent",
				"agents reach items through grants; until those arrive, the vault answers an agent with nothing")
			return secrets.Actor{}, false
		}
		writeError(w, http.StatusForbidden, "secrets.person", "the vault is for people; sign in with an account")
		return secrets.Actor{}, false
	}
	return who, true
}

// vaultError maps the vault's errors to HTTP answers.
func (s *Server) vaultError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, secrets.ErrSealed):
		writeError(w, http.StatusServiceUnavailable, "secrets.sealed", "the vault is sealed; enter the passphrase to unseal it")
	case errors.Is(err, secrets.ErrWrongPassphrase):
		writeError(w, http.StatusForbidden, "secrets.wrong_passphrase", "the passphrase does not unseal this vault")
	case errors.Is(err, secrets.ErrNotInitialised):
		writeError(w, http.StatusConflict, "secrets.not_initialised", "this vault has no passphrase yet; POST /api/v1/vault/init first")
	case errors.Is(err, secrets.ErrReauth):
		writeError(w, http.StatusUnauthorized, "secrets.reauth", "enter your password to view values")
	case errors.Is(err, secrets.ErrAgent):
		writeError(w, http.StatusForbidden, "secrets.agent", "agents reach items through grants; until those arrive, the vault answers an agent with nothing")
	case errors.Is(err, secrets.ErrForbidden):
		writeError(w, http.StatusForbidden, "secrets.permission", "you may not do this here")
	case errors.Is(err, secrets.ErrNotFound):
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such collection, item, tag or field")
	case errors.Is(err, secrets.ErrExists):
		writeError(w, http.StatusConflict, "secrets.exists", "a collection or tag of this name exists")
	case errors.Is(err, secrets.ErrConflict):
		writeError(w, http.StatusConflict, "secrets.conflict", "it changed since you saw it")
	case errors.Is(err, secrets.ErrAttachment):
		writeError(w, http.StatusRequestEntityTooLarge, "secrets.attachment_too_large", "the attachment is too large")
	case errors.Is(err, secrets.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "secrets.invalid", err.Error())
	case errors.Is(err, secrets.ErrBadInput):
		writeError(w, http.StatusBadRequest, "secrets.bad_input", err.Error())
	case errors.Is(err, secrets.ErrCrypto):
		s.Log.Error("the vault's cryptography failed", "err", err)
		writeError(w, http.StatusInternalServerError, "secrets.crypto", "the ciphertext does not authenticate under this key")
	default:
		s.Log.Error("vault operation failed", "err", err)
		writeError(w, http.StatusBadGateway, "upstream", err.Error())
	}
}

func (s *Server) vaultState(w http.ResponseWriter, _ *http.Request, _ bridge.Actor) {
	writeJSON(w, http.StatusOK, struct {
		Sealed bool     `json:"sealed"`
		Mode   string   `json:"mode"`
		Bad    []string `json:"rows_failed_signature,omitempty"`
	}{
		Sealed: s.Vault.Sealed(), Mode: s.Vault.SealModeName(), Bad: s.Vault.Bad(),
	})
}

func (s *Server) vaultInit(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	var in struct {
		Passphrase string `json:"passphrase"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	if err := s.Vault.InitPassphrase(in.Passphrase); err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sealed bool `json:"sealed"`
	}{Sealed: s.Vault.Sealed()})
}

func (s *Server) vaultUnseal(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	var in struct {
		Passphrase string `json:"passphrase"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	if err := s.Vault.Unseal(in.Passphrase); err != nil {
		if errors.Is(err, secrets.ErrWrongPassphrase) {
			s.Auth.Fail(r)
		}
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sealed bool `json:"sealed"`
	}{Sealed: s.Vault.Sealed()})
}

func (s *Server) vaultSeal(w http.ResponseWriter, _ *http.Request, _ bridge.Actor) {
	if err := s.Vault.Seal(); err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sealed bool `json:"sealed"`
	}{Sealed: true})
}

func (s *Server) vaultRotateKey(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	var in struct {
		Passphrase string `json:"passphrase"` // the new one, in the passphrase mode
	}
	if r.ContentLength != 0 && !readBody(w, r, &in, 1<<16) {
		return
	}
	if err := s.Vault.RotateRootKey(in.Passphrase); err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Rotated bool `json:"rotated"`
	}{Rotated: true})
}

func (s *Server) vaultRotateCollectionKey(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	var in struct {
		Collection string `json:"collection"`
	}
	if !readBody(w, r, &in, 1<<10) {
		return
	}
	if err := s.Vault.RotateCollectionKey(in.Collection); err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Started bool `json:"started"`
	}{Started: true})
}

func (s *Server) listCollections(w http.ResponseWriter, _ *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var out []secrets.Collection
	for _, col := range s.Vault.Collections() {
		if s.Vault.May(who, col.ID, secrets.PermDescribe) {
			out = append(out, col)
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Collections []secrets.Collection `json:"collections"`
	}{Collections: out})
}

type collectionIn struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Level       string `json:"level"`
}

func (s *Server) createCollection(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	var in collectionIn
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	col, err := s.Vault.CreateCollection(secrets.Actor{Via: "api"}, in.Name, in.Description, in.Level)
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, col)
}

func (s *Server) getCollection(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	col, found := s.Vault.Collection(r.PathValue("id"))
	if !found || !s.Vault.May(who, col.ID, secrets.PermDescribe) {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such collection")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Collection secrets.Collection `json:"collection"`
		MayRead    bool               `json:"may_read"`
	}{Collection: col, MayRead: s.Vault.May(who, col.ID, secrets.PermRead)})
}

func (s *Server) putCollection(w http.ResponseWriter, r *http.Request, _ bridge.Actor) {
	var in struct {
		Description string `json:"description"`
		Level       string `json:"level"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	col, err := s.Vault.UpdateCollection(secrets.Actor{Via: "api"}, r.PathValue("id"), in.Description, in.Level)
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, col)
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	col, found := s.Vault.Collection(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such collection")
		return
	}
	if !s.Vault.May(who, col.ID, secrets.PermDescribe) {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such collection")
		return
	}
	items := s.Vault.Items(col.Name, r.URL.Query().Get("state"))
	if items == nil {
		items = []secrets.Item{}
	}
	writeJSON(w, http.StatusOK, struct {
		Items []secrets.Item `json:"items"`
	}{Items: items})
}

func (s *Server) postItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in itemIn
	if !readBody(w, r, &in, 4<<20) {
		return
	}
	col, found := s.Vault.Collection(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such collection")
		return
	}
	in.Collection = col.Name
	item, err := s.Vault.SetItem(who, in.itemIn())
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// itemIn is an item as the API takes it; the fields carry their values, which never appear in any
// log, error or audit text.
type itemIn struct {
	ID          string            `json:"id,omitempty"`
	Collection  string            `json:"collection"`
	Title       string            `json:"title,omitempty"`
	Category    string            `json:"category,omitempty"`
	URLs        []string          `json:"urls,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Level       string            `json:"level,omitempty"`
	Actions     []string          `json:"actions,omitempty"`
	Source      string            `json:"source,omitempty"`
	RotateDays  int               `json:"rotate_days,omitempty"`
	Machines    []string          `json:"machines,omitempty"`
	Hosts       []string          `json:"hosts,omitempty"`
	Fields      []secrets.FieldIn `json:"fields,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	HasNotes    bool              `json:"has_notes,omitempty"`
	Attachments []attachmentIn    `json:"attachments,omitempty"`
	Reason      string            `json:"reason,omitempty"`
}

type attachmentIn struct {
	Name string `json:"name"`
	Data []byte `json:"data"` // base64 in JSON, as encoding/json does for []byte
}

func (in itemIn) itemIn() secrets.ItemIn {
	out := secrets.ItemIn{Collection: in.Collection, Title: in.Title, Category: in.Category, URLs: in.URLs,
		Tags: in.Tags, Level: in.Level, Actions: in.Actions, Source: in.Source, RotateDays: in.RotateDays,
		Machines: in.Machines, Hosts: in.Hosts, Fields: in.Fields, Notes: in.Notes, HasNotes: in.HasNotes, Reason: in.Reason}
	for _, a := range in.Attachments {
		out.Attachments = append(out.Attachments, secrets.AttachmentIn{Name: a.Name, Data: a.Data})
	}
	return out
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	item, col, found := s.Vault.Item(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such item")
		return
	}
	if !s.Vault.May(who, col.ID, secrets.PermDescribe) {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such item")
		return
	}
	versions := s.Vault.Versions(item.ID)
	if versions == nil {
		versions = []secrets.ItemVersion{}
	}
	for i := range versions {
		versions[i].CT = nil // the ciphertext never leaves
	}
	writeJSON(w, http.StatusOK, struct {
		Item       secrets.Item          `json:"item"`
		Collection secrets.Collection    `json:"collection"`
		Level      string                `json:"level"`
		Versions   []secrets.ItemVersion `json:"versions"`
		WhoCanGet  []string              `json:"who_can_get"`
		Ref        string                `json:"ref"`
	}{
		Item: item, Collection: col, Level: s.Vault.EffectiveLevel(item), Versions: versions,
		WhoCanGet: s.Vault.WhoCanGet(col.ID), Ref: secrets.RefOfItem(col.Name, itemRefName(col, item)),
	})
}

func itemRefName(col secrets.Collection, item secrets.Item) string {
	if secrets.RefNameOK(item.Title) {
		return item.Title
	}
	return item.ID
}

func (s *Server) putItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in itemIn
	if !readBody(w, r, &in, 4<<20) {
		return
	}
	in.ID = r.PathValue("id")
	item, err := s.Vault.SetItem(who, in.itemIn())
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) trashItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	s.stateChange(w, r, p, s.Vault.TrashItem)
}

func (s *Server) archiveItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	s.stateChange(w, r, p, s.Vault.ArchiveItem)
}

func (s *Server) restoreItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	s.stateChange(w, r, p, s.Vault.RestoreItem)
}

func (s *Server) stateChange(w http.ResponseWriter, r *http.Request, p store.Principal, op func(secrets.Actor, string) (secrets.Item, error)) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	item, err := op(who, r.PathValue("id"))
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) rollbackItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in struct {
		Version int64  `json:"version"`
		Reason  string `json:"reason"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	item, err := s.Vault.RollbackItem(who, r.PathValue("id"), in.Version, in.Reason)
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) generateItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in struct {
		Section string `json:"section,omitempty"`
		Field   string `json:"field"`
		Reason  string `json:"reason,omitempty"`
		Length  int    `json:"length,omitempty"`
		Charset string `json:"charset,omitempty"` // letters, alnum or all
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	item, err := s.Vault.Generate(who, r.PathValue("id"), in.Section, in.Field, in.Reason, secrets.GenSpec{Length: in.Length, Charset: in.Charset})
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item) // the version number, never the value
}

func (s *Server) revealItem(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in struct {
		Fields   []string `json:"fields,omitempty"`
		Password string   `json:"password"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	values, fields, err := s.Vault.Reveal(who, r.PathValue("id"), in.Fields, in.Password)
	if err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Values map[string]string `json:"values"`
		Fields []secrets.Field   `json:"fields"`
	}{Values: values, Fields: fields})
}

func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 1 {
		writeError(w, http.StatusBadRequest, "secrets.bad_input", "the version is a whole number")
		return
	}
	item, col, found := s.Vault.Item(r.PathValue("id"))
	if !found || !s.Vault.May(who, col.ID, secrets.PermRead) {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such item")
		return
	}
	data, _, err := s.Vault.Attachment(who, item.ID, version, r.PathValue("name"))
	if err != nil {
		s.vaultError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data) //nolint:gosec // G705: the attachment's bytes, not HTML
}

func (s *Server) listPerms(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	col, found := s.Vault.Collection(r.PathValue("id"))
	if !found {
		writeError(w, http.StatusNotFound, "secrets.not_found", "no such collection")
		return
	}
	if !s.Vault.May(who, col.ID, secrets.PermManage) {
		writeError(w, http.StatusForbidden, "secrets.permission", "managing a collection's people needs manage")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Permissions []secrets.Permission `json:"permissions"`
	}{Permissions: s.Vault.Permissions(col.ID)})
}

func (s *Server) postPerm(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in struct {
		Subject string `json:"subject"`
		Perm    string `json:"perm"`
		Reason  string `json:"reason"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	if err := s.Vault.SetPermission(who, r.PathValue("id"), in.Subject, in.Perm, in.Reason); err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Set bool `json:"set"`
	}{Set: true})
}

func (s *Server) searchVault(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	out := s.Vault.Search(who, r.URL.Query().Get("q"), r.URL.Query().Get("collection"), r.URL.Query()["tag"])
	if out == nil {
		out = []secrets.SearchResult{}
	}
	writeJSON(w, http.StatusOK, struct {
		Results []secrets.SearchResult `json:"results"`
	}{Results: out})
}

func (s *Server) listTags(w http.ResponseWriter, _ *http.Request, p store.Principal) {
	if _, ok := s.needPerson(w, p); !ok {
		return
	}
	tags := s.Vault.Tags()
	if tags == nil {
		tags = []secrets.Tag{}
	}
	writeJSON(w, http.StatusOK, struct {
		Tags []secrets.Tag `json:"tags"`
	}{Tags: tags})
}

func (s *Server) postTag(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Level       string `json:"level,omitempty"`
		Description string `json:"description,omitempty"`
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	if _, err := s.Vault.SetTag(who, in.Name, in.Level, in.Description); err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Set bool `json:"set"`
	}{Set: true})
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	if err := s.Vault.DeleteTag(who, r.PathValue("name")); err != nil {
		s.vaultError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Deleted bool `json:"deleted"`
	}{Deleted: true})
}
