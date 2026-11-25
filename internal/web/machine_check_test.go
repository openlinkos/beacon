package web

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

func TestConsoleMachineSSHCheck(t *testing.T) {
	e := newEnv(t, i18n.EN)
	owner := e.owner()
	viewer := e.invite(owner, "reader", "viewer")
	if err := e.st.PutMachine(store.Machine{Name: "edge-1", Address: store.ConnectorAddress, User: "beacon"}); err != nil {
		t.Fatal(err)
	}
	m, ok := e.st.Machine("edge-1")
	if !ok {
		t.Fatal("fixture is missing its machine")
	}
	path := "/console/machines/edge-1"
	action := path + "/check?registration=" + url.QueryEscape(m.Registration())
	page := owner.get(path)
	if page.code != http.StatusOK || !strings.Contains(html.UnescapeString(page.body), action+"#ssh-check-result") ||
		!strings.Contains(page.body, "Check machine SSH") || strings.Contains(page.body, "Machine SSH is ready") {
		t.Fatal("machine detail should offer a point-in-time check before setting up a personal key")
	}
	if read := viewer.get(path); read.code != http.StatusOK || strings.Contains(read.body, "Check machine SSH") {
		t.Fatal("read-only operator can see the SSH check action")
	}
	if response := owner.post(action, url.Values{"csrf": {"wrong"}}); response.code != http.StatusForbidden {
		t.Fatalf("invalid CSRF should fail: %d", response.code)
	}
	if response := viewer.post(action, nil); response.code != http.StatusForbidden {
		t.Fatalf("view-only account can run an SSH check: %d", response.code)
	}
	if response := owner.post(path+"/check?registration=stale", nil); response.code != http.StatusNotFound {
		t.Fatalf("stale machine registration should fail: %d", response.code)
	}
	if response := owner.post(action, nil); response.code != http.StatusOK ||
		!strings.Contains(response.body, `id="ssh-check-result"`) || !strings.Contains(response.body, "The connector is offline.") ||
		strings.Contains(response.body, "Machine SSH is ready") {
		t.Fatalf("missing clear connector result: %d", response.code)
	}
	out, _, err := e.st.AuditQuery(store.AuditFilter{Action: "machine_ssh_check", Target: "edge-1"})
	if err != nil || len(out) != 2 || out[0].Actor != "alice" || out[0].Via != "console" ||
		!strings.HasSuffix(out[0].Detail, " connector_disconnected") {
		t.Fatalf("only permitted check should produce audit history: %+v, %v", out, err)
	}
}
