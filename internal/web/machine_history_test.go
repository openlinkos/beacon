package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
)

func TestConsoleMachineHistory(t *testing.T) {
	e := newEnv(t, i18n.EN)
	op := e.owner()
	reader := e.invite(op, "history-reader", "viewer")
	const path = "/console/machines/edge-1"
	initial, _ := e.st.Machine("edge-1")
	base := strconv.FormatInt(initial.MetadataVersion, 10)
	if a := op.get(path); a.code != http.StatusOK || !strings.Contains(a.body, path+"/history") || !strings.Contains(a.body, `name="if_version" value="`+base+`"`) {
		t.Fatalf("machine route does not expose history and the edit version: %d", a.code)
	}
	f := url.Values{"note": {"updated note"}, "user": {initial.User}, "if_version": {base}}
	if a := op.post(path+"/edit", f); !strings.Contains(a.header.Get("Location"), "done=saved") {
		t.Fatalf("edit: %d %s", a.code, a.header.Get("Location"))
	}
	if a := op.post(path+"/edit", f); !strings.Contains(a.header.Get("Location"), "error=machine.changed") {
		t.Fatalf("stale edit: %d %s", a.code, a.header.Get("Location"))
	}
	after, _ := e.st.Machine("edge-1")
	current := strconv.FormatInt(after.MetadataVersion, 10)
	if a := reader.get(path + "/history"); a.code != http.StatusOK || !strings.Contains(a.body, "updated note") || strings.Contains(a.body, `action="`+path+`/restore"`) {
		t.Fatalf("viewer history should show the revision without restore controls: %d", a.code)
	}
	if a := op.get(path + "/history"); a.code != http.StatusOK || !strings.Contains(a.body, `action="`+path+`/restore"`) || strings.Contains(a.body, "<script>alert(1)</script>") {
		t.Fatalf("restore form absent or note unescaped: %d", a.code)
	}
	restore := url.Values{"version": {base}, "if_version": {current}}
	if a := reader.post(path+"/restore", clone(restore)); a.code != http.StatusForbidden {
		t.Errorf("viewer could restore: %d", a.code)
	}
	if a := op.post(path+"/restore", restore); !strings.Contains(a.header.Get("Location"), "done=machine_restored") {
		t.Fatalf("restore: %d %s", a.code, a.header.Get("Location"))
	}
	if got, _ := e.st.Machine("edge-1"); got.Note != initial.Note || got.MetadataVersion <= after.MetadataVersion {
		t.Errorf("unexpected restored machine: %+v", got)
	}
}
