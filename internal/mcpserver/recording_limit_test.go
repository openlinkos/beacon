package mcpserver

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/sshgate"
)

// TestRecordingLimitHasItsCode: the tools name a stop at the recording limit with the REST API's code.
func TestRecordingLimitHasItsCode(t *testing.T) {
	err := sessionToolError(fmt.Errorf("%w (16 MiB) in session 20261102-100000-abcdef-1", sshgate.ErrRecordingLimit))
	if !errors.Is(err, sshgate.ErrRecordingLimit) || !strings.HasPrefix(err.Error(), "ssh.recording_limit: ") {
		t.Fatalf("%v", err)
	}
}
