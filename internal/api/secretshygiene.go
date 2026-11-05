package api

import (
	"net/http"

	"github.com/openlinkos/beacon/internal/secrets"
	"github.com/openlinkos/beacon/internal/store"
)

// The hygiene's routes: the health report and the rotation list for people (the report opens
// values the reader may read, but hands out judgements and references only), and the rotation
// itself for an agent that holds a use grant — the new value is made on the server, stored as the
// next version and never returned.

func hygieneRoutes() []string {
	return []string{
		"GET /api/v1/secrets/hygiene", "POST /api/v1/secrets/rotate",
	}
}

func init() { Routes = append(Routes, hygieneRoutes()...) }

// registerSecretsHygiene adds the hygiene's routes; the same service the grants' routes hold.
func (s *Server) registerSecretsHygiene(mux *http.ServeMux) {
	if s.Secrets == nil {
		return
	}
	mux.Handle("GET /api/v1/secrets/hygiene", s.withPrincipal("secrets:describe", s.getHygiene))
	mux.Handle("POST /api/v1/secrets/rotate", s.withPrincipal("secrets:use", s.postRotateSecret))
}

// getHygiene is the console's report in one call: the password health findings (weak, repeated,
// old, leaked when the deployment asks the corpus), the items whose rotation period ends soon or
// has passed, and the grants that have sat unused longer than the setting allows. A person's view;
// the report itself walks only the collections the reader may read.
func (s *Server) getHygiene(w http.ResponseWriter, r *http.Request, p store.Principal) {
	who, ok := s.needPerson(w, p)
	if !ok {
		return
	}
	report, err := s.Secrets.HealthReport(who)
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	rotation := s.Secrets.RotationDue()
	if rotation == nil {
		rotation = []secrets.RotationRow{}
	}
	unused := s.Secrets.UnusedGrants()
	if unused == nil {
		unused = []secrets.Grant{}
	}
	writeJSON(w, http.StatusOK, struct {
		Health   secrets.HealthReport  `json:"health"`
		Rotation []secrets.RotationRow `json:"rotation"`
		Unused   []secrets.Grant       `json:"unused_grants"`
	}{Health: report, Rotation: rotation, Unused: unused})
}

// postRotateSecret is the agent's rotation: the server makes the next value by the spec, stores it
// as the item's next version and answers with the version number — the value never travels, so an
// agent turns a secret and then works the new value into the target system through the use action.
func (s *Server) postRotateSecret(w http.ResponseWriter, r *http.Request, p store.Principal) {
	name, ok := agentOf(p)
	if !ok {
		writeError(w, http.StatusForbidden, "secrets.agent", "only an agent turns a secret; people change values in the console")
		return
	}
	var in struct {
		Ref     string `json:"ref"`
		Reason  string `json:"reason"`
		Length  int    `json:"length,omitempty"`
		Charset string `json:"charset,omitempty"` // letters, alnum or all
	}
	if !readBody(w, r, &in, 1<<16) {
		return
	}
	item, err := s.Secrets.RotateItem(name, "api", in.Ref, in.Reason, secrets.GenSpec{Length: in.Length, Charset: in.Charset})
	if err != nil {
		s.vaultError2(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item) // the overview with its version number, never the value
}
