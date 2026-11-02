package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/store"
	"github.com/openlinkos/beacon/internal/version"
)

// Waking agents. A hook from the configuration's agents list is called when a new alert group it
// matches is first notified, and, if it asks for that, when the group resolves: a webhook gets a signed
// JSON payload, a GitHub workflow is dispatched. Either way the agent learns the group's id and where to
// fetch the evidence; Beacon calls no model and decides nothing for it. The calls run beside alert
// delivery and never hold it up. Each is retried with backoff, and its outcome is kept on the group, in
// the audit log, on the card and in the alert chat.

// wakeBackoff is the wait before each retry: a call is tried at most five times, within about five
// minutes. Tests shorten it.
var wakeBackoff = []time.Duration{5 * time.Second, 20 * time.Second, time.Minute, 3 * time.Minute}

const (
	wakeTimeout = 10 * time.Second // for one attempt
	wakeResume  = 10 * time.Minute // a call that a restart interrupted is taken up again when younger
	wakeAtOnce  = 8                // calls in flight
)

// hooks are the calls in flight.
type hooks struct {
	once sync.Once
	sem  chan struct{}
	hc   *http.Client
	wg   sync.WaitGroup
}

func (h *hooks) init() {
	h.once.Do(func() {
		h.sem = make(chan struct{}, wakeAtOnce)
		// A hook that redirects has its answer taken as it is: the payload and its signature go to the
		// address configured and nowhere else.
		h.hc = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	})
}

// waitWakes waits for the calls in flight (for tests).
func (b *Bridge) waitWakes() { b.hooks.wg.Wait() }

// HookPayload is what a webhook receives. Delivery stays the same when a call is retried, and At is when
// the event happened, so a receiver can refuse a delivery it has seen or one that is too old.
type HookPayload struct {
	Delivery  string     `json:"delivery"`
	Event     string     `json:"event"` // firing or resolved
	At        time.Time  `json:"at"`
	Hook      string     `json:"hook"`
	Alert     AlertBrief `json:"alert"`
	Links     AlertLinks `json:"links"`
	Hint      string     `json:"hint"`
	Untrusted Untrusted  `json:"untrusted"`
}

const (
	hintFiring = "Beacon woke you for this alert group. Get its evidence in one call: the MCP tool get_evidence with " +
		"alert.id, or GET links.evidence with a token that has status:read. get_timeline shows what happened in order; " +
		"add_note leaves what you find or do on the alert, for people to see. Beacon does not decide what to do: you do."
	hintResolved = "This alert group has resolved. get_timeline shows what happened; add_note leaves a closing note for people to see."
)

// dispatch is the body of a GitHub workflow_dispatch: the workflow must declare the inputs alert and event.
type dispatch struct {
	Ref    string            `json:"ref"`
	Inputs map[string]string `json:"inputs"`
}

// signature is a webhook body's X-Beacon-Signature: sha256= and the hex HMAC-SHA256 of the body.
func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newDelivery() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// takes reports whether hook h is called for g on event. A resolved group calls back only the hooks
// that were woken for it and asked to hear of the end.
func (b *Bridge) takes(h config.Agent, g *store.Group, event string) bool {
	if event == "resolved" {
		return h.Resolved && slices.ContainsFunc(g.Agents, func(c store.AgentCall) bool { return c.Hook == h.Name && c.Event == "firing" })
	}
	if !slices.Contains(h.Severities, g.Severity) {
		return false
	}
	if h.Service == "" {
		return true
	}
	re := h.ServiceRe
	if re == nil {
		var err error
		if re, err = regexp.Compile("^(?:" + h.Service + ")$"); err != nil {
			return false
		}
	}
	return slices.ContainsFunc(b.groupServices(g), re.MatchString)
}

// wake calls the hooks that take g for event (firing or resolved), recording each call on g as pending.
// It is called with b.mu held, before g is saved; the calls run on their own.
func (b *Bridge) wake(g *store.Group, event string) {
	for _, h := range b.d.Config.Agents {
		if !b.takes(h, g, event) {
			continue
		}
		c := store.AgentCall{Hook: h.Name, Kind: h.Kind, Event: event, Delivery: newDelivery(), Status: "pending", At: b.now()}
		g.Agents = append(g.Agents, c)
		b.startCall(h, g.ID, c, b.hookBody(g, h, c))
	}
}

func (b *Bridge) hookBody(g *store.Group, h config.Agent, c store.AgentCall) []byte {
	var v any = dispatch{Ref: h.Ref, Inputs: map[string]string{"alert": g.ID, "event": c.Event}}
	if h.Kind != "github" {
		hint := hintFiring
		if c.Event == "resolved" {
			hint = hintResolved
		}
		v = HookPayload{Delivery: c.Delivery, Event: c.Event, At: c.At, Hook: h.Name, Alert: b.brief(g), Links: b.links(g), Hint: hint,
			Untrusted: untrusted("alert.title", "alert.description", "alert.service", "alert.services", "alert.labels")}
	}
	body, _ := json.Marshal(v)
	return body
}

func (b *Bridge) startCall(h config.Agent, groupID string, c store.AgentCall, body []byte) {
	b.hooks.init()
	b.hooks.wg.Add(1)
	go func() {
		defer b.hooks.wg.Done()
		b.hooks.sem <- struct{}{}
		defer func() { <-b.hooks.sem }()
		b.call(h, groupID, c, body)
	}()
}

// call tries the hook until it answers, it refuses for good, or the retries run out.
func (b *Bridge) call(h config.Agent, groupID string, c store.AgentCall, body []byte) {
	ctx := context.Background()
	for {
		code, err := b.send(ctx, h, c, body)
		c.Attempts++
		c.Code, c.Error = code, ""
		switch {
		case err == nil:
			c.Status, c.Done = "ok", b.now()
			b.count("agent", "ok")
		case c.Attempts > len(wakeBackoff) || !retryable(code):
			c.Status, c.Done, c.Error = "failed", b.now(), err.Error()
			b.count("agent", "error")
		default:
			c.Error = err.Error()
		}
		b.recordCall(ctx, groupID, c)
		if c.Status != "pending" {
			return
		}
		time.Sleep(wakeBackoff[c.Attempts-1])
	}
}

// retryable says whether a failed attempt is worth repeating: no answer, a timeout, too many requests
// or a server error. Any other refusal (a wrong token, an unknown workflow) stays.
func retryable(code int) bool {
	return code == 0 || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

// send makes one attempt and returns the HTTP status (0 without an answer).
func (b *Bridge) send(ctx context.Context, h config.Agent, c store.AgentCall, body []byte) (int, error) {
	b.hooks.init()
	ctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	target := h.URL
	if h.Kind == "github" {
		target = strings.TrimSuffix(h.API, "/") + "/repos/" + h.Repository + "/actions/workflows/" + url.PathEscape(h.Workflow) + "/dispatches"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	if h.Kind == "github" {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("Authorization", "Bearer "+h.Secret)
	} else {
		for name, value := range h.Headers { // the receiver's own secret, when it checks one
			req.Header.Set(name, value)
		}
		if h.Secret != "" {
			req.Header.Set("X-Beacon-Signature", signature(h.Secret, body))
		}
		req.Header.Set("X-Beacon-Event", c.Event)
		req.Header.Set("X-Beacon-Delivery", c.Delivery)
	}
	resp, err := b.hooks.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the address, which may carry a query string
		}
		return 0, fmt.Errorf("no answer: %s", cleanLine(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 == 2 {
		return resp.StatusCode, nil
	}
	var gh struct {
		Message string `json:"message"`
	}
	msg := string(answer)
	if json.Unmarshal(answer, &gh) == nil && gh.Message != "" {
		msg = gh.Message
	}
	return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, clean(msg, 200, false))
}

// recordCall saves a call's progress on its group. When the call is done, it is also audited and
// echoed to the chat, and the card shows it.
func (b *Bridge) recordCall(ctx context.Context, groupID string, c store.AgentCall) {
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.d.Store.Group(groupID)
	i := -1
	if ok {
		i = slices.IndexFunc(g.Agents, func(x store.AgentCall) bool { return x.Delivery == c.Delivery })
	}
	if i >= 0 {
		g.Agents[i] = c
		if c.Status != "pending" && c.Event == "firing" && g.Firing() && b.patchAllowed(&g, false) {
			b.patch(ctx, &g)
		}
		if err := b.d.Store.PutGroup(g); err != nil {
			b.d.Log.Error("save group", "err", err)
		}
	}
	if c.Status == "pending" {
		return
	}
	if !ok {
		g = store.Group{ID: groupID}
	}
	b.called(ctx, &g, c, ok)
}

// called audits a finished call and, when its group is still known, tells the chat.
func (b *Bridge) called(ctx context.Context, g *store.Group, c store.AgentCall, echo bool) {
	outcome := "ok"
	if c.Status != "ok" {
		outcome = "failed: " + c.Error
	}
	_ = b.d.Store.Audit(store.AuditEntry{Time: b.now(), Actor: "beacon", Via: "hook", Action: "wake_agent", Target: g.ID,
		Detail: fmt.Sprintf("%s (%s, %s): %s, %d attempts", c.Hook, c.Kind, c.Event, outcome, c.Attempts)})
	if !echo {
		return
	}
	title := chatSafe(b.title(g))
	switch {
	case c.Status != "ok":
		b.echo(ctx, b.t("Could not reach agent %s about \"%s\": %s", c.Hook, title, chatSafe(c.Error)))
	case c.Event == "resolved":
		b.echo(ctx, b.t("Told agent %s that \"%s\" resolved", c.Hook, title))
	default:
		b.echo(ctx, b.t("Woke agent %s for \"%s\"", c.Hook, title))
	}
}

// hook is the configured hook called name.
func (b *Bridge) hook(name string) (config.Agent, bool) {
	i := slices.IndexFunc(b.d.Config.Agents, func(h config.Agent) bool { return h.Name == name })
	if i < 0 {
		return config.Agent{}, false
	}
	return b.d.Config.Agents[i], true
}

// resumeWakes takes up the calls a restart interrupted: one still pending is tried again when it was
// made in the last ten minutes, and given up otherwise.
func (b *Bridge) resumeWakes(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range b.d.Store.Groups() {
		changed := false
		for i, c := range g.Agents {
			if c.Status != "pending" {
				continue
			}
			h, ok := b.hook(c.Hook)
			if ok && b.now().Sub(c.At) < wakeResume {
				b.startCall(h, g.ID, c, b.hookBody(&g, h, c))
				continue
			}
			c.Status, c.Done, c.Error = "failed", b.now(), "interrupted by a restart of Beacon"
			if !ok {
				c.Error = "the hook is no longer configured"
			}
			g.Agents[i], changed = c, true
			b.called(ctx, &g, c, true)
		}
		if changed {
			if err := b.d.Store.PutGroup(g); err != nil {
				b.d.Log.Error("save group", "err", err)
			}
		}
	}
}
