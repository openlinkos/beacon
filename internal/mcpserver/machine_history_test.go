package mcpserver

import (
	"strings"
	"testing"
)

func TestMachineHistoryTools(t *testing.T) {
	e, u := lockMCP(t)
	before, _ := e.Store.Machine("edge-1")
	reader := connect(t, u, e.Token(t, "reader", "status:read"))
	writer := connect(t, u, e.Token(t, "writer", "status:read", "machines:write"))
	if res, txt := call(t, reader, "machine_history", map[string]any{"name": "edge-1"}); res.IsError || !strings.Contains(txt, "relay") {
		t.Fatalf("history: %s", txt)
	}
	if res, txt := call(t, writer, "update_machine", map[string]any{"name": "edge-1", "note": "new", "if_version": before.MetadataVersion}); res.IsError {
		t.Fatalf("edit: %s", txt)
	}
	edit, _ := e.Store.Machine("edge-1")
	undo := map[string]any{"name": "edge-1", "version": before.MetadataVersion, "if_version": edit.MetadataVersion}
	if res, txt := call(t, reader, "restore_machine", undo); !res.IsError || !strings.Contains(txt, "machines:write") {
		t.Errorf("reader restored a version: %s", txt)
	}
	if res, txt := call(t, writer, "restore_machine", undo); res.IsError || !strings.Contains(txt, "relay") {
		t.Fatalf("restore: %s", txt)
	}
	if res, txt := call(t, writer, "restore_machine", undo); !res.IsError || !strings.Contains(txt, "machine settings changed") {
		t.Errorf("stale restore: %s", txt)
	}
}
