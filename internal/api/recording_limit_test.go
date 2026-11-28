package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openlinkos/beacon/internal/sshgate"
)

// TestRecordingLimitHasItsCode: a machine operation stopped at its recording limit answers with a code of
// its own, which openapi.yaml lists.
func TestRecordingLimitHasItsCode(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).gateError(w, fmt.Errorf("%w (16 MiB) in session 20261102-100000-abcdef-1", sshgate.ErrRecordingLimit))
	var body struct{ Code, Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusRequestEntityTooLarge || body.Code != "ssh.recording_limit" {
		t.Fatalf("%d %+v", w.Code, body)
	}
}
