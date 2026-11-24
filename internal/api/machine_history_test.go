package api

import (
	"fmt"
	"net/http"
	"testing"
)

func TestMachineHistoryRoutes(t *testing.T) {
	e, c, _ := lockSetup(t)
	before, _ := e.Store.Machine("edge-1")
	writer := e.Token(t, "writer", "status:read", "machines:write")
	reader := e.Token(t, "reader", "status:read")
	const path = "/api/v1/machines/edge-1"
	if code, out := c.do(http.MethodGet, path+"/history", reader, ""); code != 200 || out["version"] != float64(before.MetadataVersion) {
		t.Fatalf("history: %d %v", code, out)
	}
	if code, out := c.do(http.MethodPatch, path, writer, fmt.Sprintf(`{"note":"updated","if_version":%d}`, before.MetadataVersion)); code != 200 {
		t.Fatalf("edit: %d %v", code, out)
	}
	edit, _ := e.Store.Machine("edge-1")
	body := fmt.Sprintf(`{"version":%d,"if_version":%d}`, before.MetadataVersion, edit.MetadataVersion)
	if code, _ := c.do(http.MethodPost, path+"/restore", reader, body); code != 403 {
		t.Fatalf("reader restored a version: %d", code)
	}
	if code, out := c.do(http.MethodPost, path+"/restore", writer, body); code != 200 || out["note"] != before.Note || out["host_key_fingerprint"] == "" {
		t.Fatalf("restore: %d %v", code, out)
	}
	if !lockAudited(e.Store, "machine_restore", "edge-1", "restored version") {
		t.Fatal("restore is missing from audit")
	}
	if code, out := c.do(http.MethodPost, path+"/restore", writer, body); code != 409 || out["code"] != "machine.changed" {
		t.Fatalf("stale restore: %d %v", code, out)
	}
}
