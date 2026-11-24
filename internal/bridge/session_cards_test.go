package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/store"
)

func sessionCardRecord(e *env, suffix, machine string) store.SessionRecord {
	return store.SessionRecord{
		ID: e.now.UTC().Format("20060102-150405") + suffix, Agent: "operator", Via: "ssh", Machine: machine,
		Kind: "shell", Started: e.now,
	}
}

func putCardSession(t *testing.T, e *env, r store.SessionRecord) {
	t.Helper()
	if err := e.st.PutSession(r); err != nil {
		t.Fatal(err)
	}
}

func TestFinishedSessionsRefreshOnlyTheRelatedCard(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	withPages(t, e)
	w := hook("firing", "warning", "f1")
	w.GroupLabels["machine"] = "edge-1"
	other := hook("firing", "warning", "f2")
	other.GroupKey = "another-group"
	other.GroupLabels["machine"] = "edge-2"
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := e.b.HandleWebhook(ctx, other); err != nil {
		t.Fatal(err)
	}
	session := sessionCardRecord(e, "-a", "edge-1")
	putCardSession(t, e, session)
	e.now = e.now.Add(patchEvery)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 0 {
		t.Fatal("an open session refreshed a card")
	}

	session.Ended, session.Commands = e.now, 3
	putCardSession(t, e, session)
	next := sessionCardRecord(e, "-b", "edge-1")
	next.Kind, next.Ended, next.Command = "exec", e.now, "private command marker"
	putCardSession(t, e, next)
	unrelated := sessionCardRecord(e, "-c", "unrelated")
	unrelated.Ended = e.now
	putCardSession(t, e, unrelated)

	// A new bridge on the same database first sees both closed sessions together.
	e.b = New(e.b.d)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 1 {
		t.Fatalf("got %d patches for two related session closes", len(e.fs.patches))
	}
	card := cardText(e.fs.patches[0])
	for _, want := range []string{"**Finished SSH sessions**", "2 sessions on 1 machines, 4 commands",
		"Latest session ended at " + e.b.clock(e.now), "/console/alerts/" + GroupID(w.GroupKey)} {
		if !strings.Contains(card, want) {
			t.Errorf("patched card lacks %q", want)
		}
	}
	if strings.Contains(card, next.Command) || strings.Contains(card, "/console/sessions/") {
		t.Error("the card included private session text or a new replay link")
	}
	e.now = e.now.Add(patchEvery)
	e.b = New(e.b.d)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 1 || len(e.fs.sends) != 2 || e.st.Counter(e.b.quotaKey()) != 3 {
		t.Fatal("the stored summary did not prevent an extra Feishu call after restart")
	}

	for _, machine := range []string{"edge-1", "edge-2"} {
		closed := sessionCardRecord(e, "-"+machine, machine)
		closed.Ended = e.now
		putCardSession(t, e, closed)
	}
	e.b.Tick(ctx)
	if len(e.fs.patches) != 2 {
		t.Fatal("one tick should patch only one of the two pending cards")
	}
	e.b.Tick(ctx)
	if len(e.fs.patches) != 3 {
		t.Fatal("the next tick should patch the remaining card")
	}
}

type interceptedSessionPatch struct {
	*fakeFeishu
	beforeReply func() error
}

func (f *interceptedSessionPatch) PatchCard(ctx context.Context, id string, c feishu.Card) error {
	if err := f.fakeFeishu.PatchCard(ctx, id, c); err != nil {
		return err
	}
	return f.beforeReply()
}

func TestResolvedSessionCardHonoursBudgetAndRetries(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	w := hook("firing", "warning", "f1")
	w.CommonLabels["machine"] = "edge-1"
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	r := sessionCardRecord(e, "-a", "edge-1")
	putCardSession(t, e, r)
	w.Status = "resolved"
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	fail := true
	api := &interceptedSessionPatch{fakeFeishu: e.fs, beforeReply: func() error {
		if fail {
			return errors.New("fixture Feishu patch failed")
		}
		return nil
	}}
	e.b.d.Feishu = api
	r.Ended = e.now.Add(time.Second)
	putCardSession(t, e, r)
	e.now = e.now.Add(patchEvery - time.Second)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 1 {
		t.Fatal("the session update ignored the per-card interval")
	}

	e.now = e.now.Add(time.Second)
	e.b.d.Config.Feishu.MonthlyBudget = 100
	e.st.Add(e.b.quotaKey(), 88)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 1 {
		t.Fatal("the session refresh used Feishu's final 10% budget")
	}
	e.b.d.Config.Feishu.MonthlyBudget = 1000
	e.b.Tick(ctx)
	g, _ := e.st.Group(GroupID(w.GroupKey))
	if len(e.fs.patches) != 2 || g.CardSessionHash != "" || !g.CardSessionAttemptAt.Equal(e.now) {
		t.Fatal("the failed patch advanced the delivered session summary or missed backoff")
	}

	e.now = e.now.Add(patchEvery - time.Second)
	e.b = New(e.b.d)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 2 {
		t.Fatal("the retry interval did not survive restart")
	}
	e.now = e.now.Add(time.Second)
	fail = false
	e.b.Tick(ctx)
	if len(e.fs.patches) != 3 {
		t.Fatal("the resolved card did not receive the retried session update")
	}
	if card := cardText(e.fs.patches[2]); !strings.Contains(card, `"template":"green"`) ||
		!strings.Contains(card, "**Finished SSH sessions**") || strings.Contains(card, "Acknowledge") {
		t.Errorf("the refreshed card lost resolved state: %s", card)
	}
}

func TestSessionClosingDuringPatchStaysPending(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	w := hook("firing", "warning", "f1")
	w.GroupLabels["machine"] = "edge-1"
	if err := e.b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	first := sessionCardRecord(e, "-a", "edge-1")
	first.Ended = e.now
	putCardSession(t, e, first)
	second := sessionCardRecord(e, "-b", "edge-1")
	putCardSession(t, e, second)
	e.b.d.Feishu = &interceptedSessionPatch{fakeFeishu: e.fs, beforeReply: func() error {
		second.Ended = e.now
		return e.st.PutSession(second)
	}}

	e.now = e.now.Add(patchEvery)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 1 || !strings.Contains(cardText(e.fs.patches[0]), "1 sessions") {
		t.Fatal("the first patch should only show the session that finished before it was rendered")
	}
	e.b.d.Feishu = e.fs
	e.now = e.now.Add(patchEvery)
	e.b.Tick(ctx)
	if len(e.fs.patches) != 2 || !strings.Contains(cardText(e.fs.patches[1]), "2 sessions") {
		t.Fatal("the session that closed while the first patch was in flight was lost")
	}
}
