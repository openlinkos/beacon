package sshgate

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/openlinkos/beacon/internal/scrub"
)

// The vault's delivery of a declared file: the value of an item written to a machine's path, as the
// owner and with the mode the item names. It is the system's session, not an agent's — the vault
// asked for it when the item changed — and it is recorded like any other. The content is a secret,
// so it never enters the recording or the transcript: the session counts the bytes and keeps
// nothing of them.
//
// What a machine's own files hold is not hidden from the shell: an agent that may run commands on
// the machine can read any file it could read with its own ssh. The scrubbing that read_file and
// run_command's secrets do is for the tools' answers — the agent's context — and says so in the
// docs; it is no fence around the machine itself.

// scrubFor is the scrubber of the values a machine may hold, for the file tools' answers.
func (g *Gate) scrubFor(machine string) *scrub.Scrubber {
	if g == nil || g.d.Scrub == nil {
		return nil
	}
	return g.d.Scrub(machine)
}

// DeliverFile writes content to a machine's path, making its directory if needed, and sets its
// owner and mode when they are given (chown needs the machine user's rights; the error says when it
// does not have them). It answers with the session's id, for the record the vault keeps beside its
// own.
func (g *Gate) DeliverFile(ctx context.Context, machine, path, content, owner, mode string) (string, error) {
	if g == nil {
		return "", ErrDisabled
	}
	if g.Paused() {
		return "", fmt.Errorf("%w since %s; a person resumes it with beacon ssh resume", ErrPaused,
			g.PausedSince().Format("2006-01-02 15:04"))
	}
	m, found := g.d.Store.Machine(machine)
	if !found {
		return "", fmt.Errorf("%w called %q", ErrNoMachine, machine)
	}
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("%w: the path is absolute, starting with /", ErrBadInput)
	}
	if owner != "" && !ownerRe.MatchString(owner) {
		return "", fmt.Errorf("%w: the owner is a user name, or user:group", ErrBadInput)
	}
	if mode != "" && !modeRe.MatchString(mode) {
		return "", fmt.Errorf("%w: the mode is octal, like 0600", ErrBadInput)
	}
	body := `f=$1
d=$(dirname -- "$f")
if [ ! -d "$d" ]; then mkdir -p -- "$d" || exit 73; fi
cat > "$f" || exit 74
if [ -n "$2" ]; then chown "$2" -- "$f" || exit 76; fi
if [ -n "$3" ]; then chmod "$3" -- "$f" || exit 77; fi`
	// the content is a secret: the session counts the bytes and records nothing of them (the last
	// argument, false, keeps the input out of the recording and the transcript)
	t, _, err := g.execute(ctx, Actor{Name: "beacon", Via: "vault"}, m, "file", "deliver "+path,
		script(body, path, owner, mode), []byte(content), time.Minute, false, nil)
	if err != nil {
		return "", err
	}
	if err := fileError(t, path); err != nil {
		return "", err
	}
	return t.ID, nil
}

// ownerRe and modeRe are the shapes an owner and a mode must have (they are the vault's own checks,
// made again at the door).
var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*(:[A-Za-z0-9_.][A-Za-z0-9_.-]*)?$`)
	modeRe  = regexp.MustCompile(`^[0-7]{3,4}$`)
)
