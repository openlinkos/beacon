package api

import (
	"strings"
	"testing"
)

func TestConfigIfVersion(t *testing.T) {
	e, c := setupConfig(t)
	write := e.Token(t, "claude-code", "config:write")
	loc := `{"name": "United States - Los Angeles", "region": "overseas"}`
	if code, out := c.do("PUT", "/api/v1/config/locations/edge-1?if_version=0", write, loc); code != 400 || out["code"] != "bad_input" {
		t.Errorf("if_version=0: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/config/locations/edge-1?if_version=x", write, loc); code != 400 || out["code"] != "bad_input" {
		t.Errorf("if_version=x: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/config/locations/edge-1?if_version=7", write, loc); code != 409 || out["code"] != "config.conflict" || !strings.Contains(out["error"].(string), "version 0 is in force") {
		t.Errorf("a version that never was: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/config/locations/edge-1", write, loc); code != 200 || out["version"] != float64(1) {
		t.Fatalf("put: %d %v", code, out)
	}
	if code, out := c.do("PUT", "/api/v1/config/locations/edge-1?if_version=1", write, `{"name": "United States - Los Angeles", "region": "overseas", "network": "CN2 GIA"}`); code != 200 || out["version"] != float64(2) {
		t.Fatalf("put on the version in force: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/config/locations/edge-1?if_version=1", write, ""); code != 409 || out["code"] != "config.conflict" || !strings.Contains(out["error"].(string), "version 2 is in force, not 1") {
		t.Errorf("a stale delete: %d %v", code, out)
	}
	if code, out := c.do("DELETE", "/api/v1/config/locations/edge-1?if_version=2", write, ""); code != 200 || out["version"] != float64(3) {
		t.Errorf("delete on the version in force: %d %v", code, out)
	}
}
