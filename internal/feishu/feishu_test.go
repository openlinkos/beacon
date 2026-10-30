package feishu

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCardJSON(t *testing.T) {
	c := NewCard(Header("git.linkos.io is down", "since 10:00", Red, Tag("critical", "red")),
		Markdown("**Service** git"),
		nil, // skipped
		Row(Button("Ack", "primary_filled", map[string]any{"a": "ack", "g": "abc"}), LinkButton("Runbook", "https://example.com"), nil),
		NoteForm("ack_note", "Note", "Ack with note", map[string]any{"a": "note", "g": "abc"}),
	)
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"schema":"2.0"`, `"update_multi":true`, `"template":"red"`, `"text_tag_list"`,
		`"behaviors":[{"type":"callback","value":{"a":"ack","g":"abc"}}]`,
		`"default_url":"https://example.com"`, `"form_action_type":"submit"`, `"name":"note"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	els := c["body"].(map[string]any)["elements"].([]any)
	if len(els) != 3 {
		t.Errorf("nil elements should be dropped: %d", len(els))
	}
	if Row(nil) != nil {
		t.Error("an empty row should be nil")
	}
}

func TestToSDK(t *testing.T) {
	r := toSDK(Response{Toast: "done", Card: Card{"schema": "2.0"}})
	if r.Toast.Type != "info" || r.Toast.Content != "done" || r.Card.Type != "raw" {
		t.Errorf("%+v", r)
	}
	if r := toSDK(Response{}); r.Toast != nil || r.Card != nil {
		t.Errorf("empty response: %+v", r)
	}
}
