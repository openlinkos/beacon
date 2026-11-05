package ctl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/openlinkos/beacon/internal/secrets"
)

// Vault is the service the socket serves for `beacon secret …` and `beacon key …`. The command line
// is a person's hands: every change is made as a named account (User), and viewing values needs
// that account's password, as the console does.
type Vault interface {
	Sealed() bool
	SealModeName() string
	Bad() []string
	Unseal(passphrase string) error
	InitPassphrase(passphrase string) error
	Seal() error
	RotateRootKey(newPassphrase string) error
	RotateCollectionKey(collection string) error

	ActorForUser(name string) (secrets.Actor, bool)
	Collections() []secrets.Collection
	Collection(nameOrID string) (secrets.Collection, bool)
	CreateCollection(who secrets.Actor, name, description, level string, actions []string) (secrets.Collection, error)
	UpdateCollection(who secrets.Actor, id, description, level string, actions []string) (secrets.Collection, error)
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

	Tags() []secrets.Tag
	SetTag(who secrets.Actor, name, level, description string, actions []string) (secrets.Tag, error)
	DeleteTag(who secrets.Actor, name string) error
	Search(who secrets.Actor, query, collection string, tags []string) []secrets.SearchResult
	EffectiveLevel(item secrets.Item) string
}

// VaultHandler serves the vault's routes under /v1/secrets/, beside Handler.
func VaultHandler(v Vault) http.Handler {
	mux := http.NewServeMux()
	failVault := func(w http.ResponseWriter, err error) {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, secrets.ErrSealed):
			code = http.StatusServiceUnavailable
		case errors.Is(err, secrets.ErrWrongPassphrase), errors.Is(err, secrets.ErrReauth), errors.Is(err, secrets.ErrForbidden):
			code = http.StatusForbidden
		case errors.Is(err, secrets.ErrNotInitialised):
			code = http.StatusConflict
		case errors.Is(err, secrets.ErrNotFound):
			code = http.StatusNotFound
		case errors.Is(err, secrets.ErrExists), errors.Is(err, secrets.ErrConflict):
			code = http.StatusConflict
		case errors.Is(err, secrets.ErrInvalid), errors.Is(err, secrets.ErrBadInput):
			code = http.StatusBadRequest
		}
		fail(w, code, err)
	}
	readBody := func(w http.ResponseWriter, r *http.Request, into any) bool {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(into); err != nil {
			fail(w, http.StatusBadRequest, err)
			return false
		}
		return true
	}
	// actor resolves the named account, refusing anything else: the command line works the vault as
	// a person, never as the service itself.
	actor := func(w http.ResponseWriter, r *http.Request) (secrets.Actor, bool) {
		who, ok := v.ActorForUser(r.URL.Query().Get("user"))
		if !ok {
			fail(w, http.StatusForbidden, errors.New("name the account whose hands these are: -user NAME (it exists, as `beacon user ls` lists)"))
			return secrets.Actor{}, false
		}
		return who, true
	}

	mux.HandleFunc("GET /v1/secrets/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Sealed bool     `json:"sealed"`
			Mode   string   `json:"mode"`
			Bad    []string `json:"rows_failed_signature,omitempty"`
		}{Sealed: v.Sealed(), Mode: v.SealModeName(), Bad: v.Bad()})
	})
	mux.HandleFunc("POST /v1/secrets/init", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Passphrase string `json:"passphrase"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if err := v.InitPassphrase(in.Passphrase); err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct{ Sealed bool }{v.Sealed()})
	})
	mux.HandleFunc("POST /v1/secrets/unseal", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Passphrase string `json:"passphrase"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if err := v.Unseal(in.Passphrase); err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct{ Sealed bool }{v.Sealed()})
	})
	mux.HandleFunc("POST /v1/secrets/seal", func(w http.ResponseWriter, _ *http.Request) {
		if err := v.Seal(); err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct{ Sealed bool }{true})
	})
	mux.HandleFunc("POST /v1/secrets/rotate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Passphrase string `json:"passphrase,omitempty"`
			Collection string `json:"collection,omitempty"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if in.Collection != "" {
			if err := v.RotateCollectionKey(in.Collection); err != nil {
				failVault(w, err)
				return
			}
			writeJSON(w, http.StatusOK, struct{ Started bool }{true})
			return
		}
		if err := v.RotateRootKey(in.Passphrase); err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct{ Rotated bool }{true})
	})
	mux.HandleFunc("GET /v1/secrets/collections", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Collections []secrets.Collection `json:"collections"`
		}{Collections: v.Collections()})
	})
	mux.HandleFunc("POST /v1/secrets/collections", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Name        string   `json:"name"`
			Description string   `json:"description,omitempty"`
			Level       string   `json:"level,omitempty"`
			Actions     []string `json:"actions,omitempty"`
		}
		if !readBody(w, r, &in) {
			return
		}
		col, err := v.CreateCollection(who, in.Name, in.Description, in.Level, in.Actions)
		if err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, col)
	})
	mux.HandleFunc("GET /v1/secrets/items", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		_ = who
		col, found := v.Collection(r.URL.Query().Get("collection"))
		if !found {
			fail(w, http.StatusNotFound, errors.New("no such collection"))
			return
		}
		out := struct {
			Collection secrets.Collection `json:"collection"`
			Items      []secrets.Item     `json:"items"`
		}{Collection: col, Items: v.Items(col.Name, r.URL.Query().Get("state"))}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /v1/secrets/items", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		var in secrets.ItemIn
		if !readBody(w, r, &in) {
			return
		}
		item, err := v.SetItem(who, in)
		if err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
	mux.HandleFunc("GET /v1/secrets/item", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		_ = who
		item, col, found := v.Item(r.URL.Query().Get("ref"))
		if !found {
			fail(w, http.StatusNotFound, errors.New("no such item"))
			return
		}
		versions := v.Versions(item.ID)
		for i := range versions {
			versions[i].CT = nil
		}
		writeJSON(w, http.StatusOK, struct {
			Item       secrets.Item          `json:"item"`
			Collection secrets.Collection    `json:"collection"`
			Level      string                `json:"level"`
			Versions   []secrets.ItemVersion `json:"versions"`
		}{Item: item, Collection: col, Level: v.EffectiveLevel(item), Versions: versions})
	})
	mux.HandleFunc("POST /v1/secrets/reveal", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Ref      string   `json:"ref"`
			Fields   []string `json:"fields,omitempty"`
			Password string   `json:"password"`
		}
		if !readBody(w, r, &in) {
			return
		}
		values, fields, err := v.Reveal(who, in.Ref, in.Fields, in.Password)
		if err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Values map[string]string `json:"values"`
			Fields []secrets.Field   `json:"fields"`
		}{Values: values, Fields: fields})
	})
	mux.HandleFunc("POST /v1/secrets/state-change", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Ref    string `json:"ref"`
			Action string `json:"action"` // archive, rm, restore, rollback, generate
			Reason string `json:"reason,omitempty"`
			// rollback
			Version int64 `json:"version,omitempty"`
			// generate
			Section string `json:"section,omitempty"`
			Field   string `json:"field,omitempty"`
			Length  int    `json:"length,omitempty"`
			Charset string `json:"charset,omitempty"`
		}
		if !readBody(w, r, &in) {
			return
		}
		var item secrets.Item
		var err error
		switch in.Action {
		case "archive":
			item, err = v.ArchiveItem(who, in.Ref)
		case "rm":
			item, err = v.TrashItem(who, in.Ref)
		case "restore":
			item, err = v.RestoreItem(who, in.Ref)
		case "rollback":
			item, err = v.RollbackItem(who, in.Ref, in.Version, in.Reason)
		case "generate":
			item, err = v.Generate(who, in.Ref, in.Section, in.Field, in.Reason, secrets.GenSpec{Length: in.Length, Charset: in.Charset})
		default:
			fail(w, http.StatusBadRequest, errors.New("action is archive, rm, restore, rollback or generate"))
			return
		}
		if err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, item)
	})
	mux.HandleFunc("GET /v1/secrets/perms", func(w http.ResponseWriter, r *http.Request) {
		col, found := v.Collection(r.URL.Query().Get("collection"))
		if !found {
			fail(w, http.StatusNotFound, errors.New("no such collection"))
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Permissions []secrets.Permission `json:"permissions"`
		}{Permissions: v.Permissions(col.ID)})
	})
	mux.HandleFunc("POST /v1/secrets/perms", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Collection string `json:"collection"`
			Subject    string `json:"subject"`
			Perm       string `json:"perm"`
			Reason     string `json:"reason,omitempty"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if err := v.SetPermission(who, in.Collection, in.Subject, in.Perm, in.Reason); err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct{ Set bool }{true})
	})
	mux.HandleFunc("GET /v1/secrets/tags", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Tags []secrets.Tag `json:"tags"`
		}{Tags: v.Tags()})
	})
	mux.HandleFunc("POST /v1/secrets/tags", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Name        string   `json:"name"`
			Level       string   `json:"level,omitempty"`
			Description string   `json:"description,omitempty"`
			Actions     []string `json:"actions,omitempty"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if _, err := v.SetTag(who, in.Name, in.Level, in.Description, in.Actions); err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct{ Set bool }{true})
	})
	mux.HandleFunc("POST /v1/secrets/tags/delete", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		var in struct {
			Name string `json:"name"`
		}
		if !readBody(w, r, &in) {
			return
		}
		if err := v.DeleteTag(who, in.Name); err != nil {
			failVault(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct{ Deleted bool }{true})
	})
	mux.HandleFunc("GET /v1/secrets/search", func(w http.ResponseWriter, r *http.Request) {
		who, ok := actor(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Results []secrets.SearchResult `json:"results"`
		}{Results: v.Search(who, r.URL.Query().Get("q"), r.URL.Query().Get("collection"), r.URL.Query()["tag"])})
	})
	return mux
}

// ---- the client the CLI uses

// VaultState is the vault's state as the socket serves it.
type VaultState struct {
	Sealed bool     `json:"sealed"`
	Mode   string   `json:"mode"`
	Bad    []string `json:"rows_failed_signature,omitempty"`
}

// VaultState asks for the vault's state.
func (c *Client) VaultState(ctx context.Context) (VaultState, error) {
	var out VaultState
	return out, c.do(ctx, "GET", "/v1/secrets/state", nil, &out)
}

// VaultInit sets the first passphrase of a passphrase-mode vault.
func (c *Client) VaultInit(ctx context.Context, passphrase string) (VaultState, error) {
	var out VaultState
	return out, c.do(ctx, "POST", "/v1/secrets/init", map[string]string{"passphrase": passphrase}, &out)
}

// VaultUnseal unseals with the passphrase.
func (c *Client) VaultUnseal(ctx context.Context, passphrase string) (VaultState, error) {
	var out VaultState
	return out, c.do(ctx, "POST", "/v1/secrets/unseal", struct {
		Passphrase string `json:"passphrase"`
	}{Passphrase: passphrase}, &out)
}

// VaultSeal seals the vault again.
func (c *Client) VaultSeal(ctx context.Context) error {
	return c.do(ctx, "POST", "/v1/secrets/seal", nil, nil)
}

// VaultRotate rotates the root key, or one collection's key when collection is named.
func (c *Client) VaultRotate(ctx context.Context, passphrase, collection string) error {
	return c.do(ctx, "POST", "/v1/secrets/rotate", struct {
		Passphrase string `json:"passphrase,omitempty"`
		Collection string `json:"collection,omitempty"`
	}{Passphrase: passphrase, Collection: collection}, nil)
}

// VaultCollections lists the collections.
func (c *Client) VaultCollections(ctx context.Context) ([]secrets.Collection, error) {
	var out struct {
		Collections []secrets.Collection `json:"collections"`
	}
	return out.Collections, c.do(ctx, "GET", "/v1/secrets/collections", nil, &out)
}

// VaultCreateCollection makes a collection.
func (c *Client) VaultCreateCollection(ctx context.Context, user, name, description, level string) (secrets.Collection, error) {
	var out secrets.Collection
	return out, c.do(ctx, "POST", "/v1/secrets/collections?user="+user, struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Level       string `json:"level,omitempty"`
	}{Name: name, Description: description, Level: level}, &out)
}

// VaultItems lists a collection's items.
func (c *Client) VaultItems(ctx context.Context, user, collection, state string) (secrets.Collection, []secrets.Item, error) {
	var out struct {
		Collection secrets.Collection `json:"collection"`
		Items      []secrets.Item     `json:"items"`
	}
	err := c.do(ctx, "GET", "/v1/secrets/items?user="+user+"&collection="+collection+"&state="+state, nil, &out)
	return out.Collection, out.Items, err
}

// VaultItem shows one item.
func (c *Client) VaultItem(ctx context.Context, user, ref string) (secrets.Item, secrets.Collection, string, []secrets.ItemVersion, error) {
	var out struct {
		Item       secrets.Item          `json:"item"`
		Collection secrets.Collection    `json:"collection"`
		Level      string                `json:"level"`
		Versions   []secrets.ItemVersion `json:"versions"`
	}
	err := c.do(ctx, "GET", "/v1/secrets/item?user="+user+"&ref="+ref, nil, &out)
	return out.Item, out.Collection, out.Level, out.Versions, err
}

// VaultSetItem makes an item or writes its next version.
func (c *Client) VaultSetItem(ctx context.Context, user string, in secrets.ItemIn) (secrets.Item, error) {
	var out secrets.Item
	return out, c.do(ctx, "POST", "/v1/secrets/items?user="+user, in, &out)
}

// VaultReveal views an item's values, with the account's password.
func (c *Client) VaultReveal(ctx context.Context, user, ref, password string, fields []string) (map[string]string, []secrets.Field, error) {
	var out struct {
		Values map[string]string `json:"values"`
		Fields []secrets.Field   `json:"fields"`
	}
	err := c.do(ctx, "POST", "/v1/secrets/reveal?user="+user, struct {
		Ref      string   `json:"ref"`
		Fields   []string `json:"fields,omitempty"`
		Password string   `json:"password"`
	}{Ref: ref, Fields: fields, Password: password}, &out)
	return out.Values, out.Fields, err
}

// VaultStateChange archives, removes, restores, rolls back or generates.
func (c *Client) VaultStateChange(ctx context.Context, user, ref, action, reason string, version int64, section, field string, length int, charset string) (secrets.Item, error) {
	var out secrets.Item
	return out, c.do(ctx, "POST", "/v1/secrets/state-change?user="+user, struct {
		Ref     string `json:"ref"`
		Action  string `json:"action"`
		Reason  string `json:"reason,omitempty"`
		Version int64  `json:"version,omitempty"`
		Section string `json:"section,omitempty"`
		Field   string `json:"field,omitempty"`
		Length  int    `json:"length,omitempty"`
		Charset string `json:"charset,omitempty"`
	}{Ref: ref, Action: action, Reason: reason, Version: version, Section: section, Field: field, Length: length, Charset: charset}, &out)
}

// VaultPerms lists a collection's permission rows.
func (c *Client) VaultPerms(ctx context.Context, collection string) ([]secrets.Permission, error) {
	var out struct {
		Permissions []secrets.Permission `json:"permissions"`
	}
	return out.Permissions, c.do(ctx, "GET", "/v1/secrets/perms?collection="+collection, nil, &out)
}

// VaultSetPerm grants or removes a permission (perm empty removes).
func (c *Client) VaultSetPerm(ctx context.Context, user, collection, subject, perm, reason string) error {
	return c.do(ctx, "POST", "/v1/secrets/perms?user="+user, struct {
		Collection string `json:"collection"`
		Subject    string `json:"subject"`
		Perm       string `json:"perm"`
		Reason     string `json:"reason,omitempty"`
	}{Collection: collection, Subject: subject, Perm: perm, Reason: reason}, nil)
}

// VaultTags lists the tags.
func (c *Client) VaultTags(ctx context.Context) ([]secrets.Tag, error) {
	var out struct {
		Tags []secrets.Tag `json:"tags"`
	}
	return out.Tags, c.do(ctx, "GET", "/v1/secrets/tags", nil, &out)
}

// VaultSetTag adds or changes a tag.
func (c *Client) VaultSetTag(ctx context.Context, user, name, level, description string) error {
	return c.do(ctx, "POST", "/v1/secrets/tags?user="+user, struct {
		Name        string `json:"name"`
		Level       string `json:"level,omitempty"`
		Description string `json:"description,omitempty"`
	}{Name: name, Level: level, Description: description}, nil)
}

// VaultDeleteTag removes a tag.
func (c *Client) VaultDeleteTag(ctx context.Context, user, name string) error {
	return c.do(ctx, "POST", "/v1/secrets/tags/delete?user="+user, struct {
		Name string `json:"name"`
	}{Name: name}, nil)
}

// VaultSearch searches.
func (c *Client) VaultSearch(ctx context.Context, user, query, collection string, tags []string) ([]secrets.SearchResult, error) {
	var out struct {
		Results []secrets.SearchResult `json:"results"`
	}
	url := "/v1/secrets/search?user=" + user + "&q=" + query
	if collection != "" {
		url += "&collection=" + collection
	}
	for _, t := range tags {
		url += "&tag=" + t
	}
	return out.Results, c.do(ctx, "GET", url, nil, &out)
}
