package secrets

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/openlinkos/beacon/internal/feishu"
	"github.com/openlinkos/beacon/internal/version"
)

// The events that wake an agent about its secrets — secret.granted, secret.denied,
// secret.expiring, secret.changed, secret.revoked — travel the way the on-call hooks travel: a
// JSON payload POSTed to the hook configured under the agent's name, signed with the hook's
// secret (X-Beacon-Signature: sha256=<hex> of the body under HMAC-SHA256), with the event's name
// and a delivery id in the headers. The wiring delivers; this package says what happened.

// HookSender delivers one event to one agent's hook.
type HookSender interface {
	SendSecretHook(ctx context.Context, agent, event string, payload []byte) error
}

// CardSender posts the card a person decides a request on, to the chat the alerts go to.
type CardSender interface {
	SendSecretCard(ctx context.Context, card feishu.Card) error
}

// hookPayload is what a secret event carries. Nothing secret is ever in it: names, ids, reasons.
// The secret.request event — the review chain waking its agent — carries the asker in Requester
// and the action in Action; the full context of the ask is get_request's, a call away.
type hookPayload struct {
	Delivery  string    `json:"delivery"`
	Event     string    `json:"event"`
	At        time.Time `json:"at"`
	Agent     string    `json:"agent,omitempty"`
	Requester string    `json:"requester,omitempty"`
	Request   string    `json:"request,omitempty"`
	Grant     string    `json:"grant,omitempty"`
	Item      string    `json:"item,omitempty"`
	Level     string    `json:"level,omitempty"`
	Uses      int64     `json:"uses,omitempty"`     // 0: unlimited
	Duration  int64     `json:"duration,omitempty"` // seconds; 0: no expiry
	Reason    string    `json:"reason,omitempty"`
	Action    string    `json:"action,omitempty"`
	Version   int64     `json:"version,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

// sendSecretHook delivers one event, off the caller's path: a hook that cannot reach its receiver
// is logged, never fatal to what happened. The delivery id makes the event refusable as a repeat.
func (s *Service) sendSecretHook(agent, event string, p hookPayload) {
	if s.d.Hooks == nil {
		return
	}
	p.Delivery, p.Event, p.At, p.Agent = newDeliveryID(), event, s.now(), agent
	body, err := json.Marshal(p)
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()
		if err := s.d.Hooks.SendSecretHook(ctx, agent, event, body); err != nil {
			s.d.Log.Warn("a secret event could not be delivered", "agent", agent, "event", event, "err", err)
		}
	}()
}

// newDeliveryID is a fresh delivery id, as the alert hooks' is.
func newDeliveryID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SecretHookSignature is the signature a receiver checks: sha256= and the hex HMAC-SHA256 of the
// body under the hook's secret — the same shape the alert hooks carry.
func SecretHookSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// SecretHookUserAgent is the user agent the delivery carries.
func SecretHookUserAgent() string { return version.UserAgent() }

// pushApprovalCard sends the Feishu card a person decides a pending request on: the ask, the
// items, the level, and the three decisions — approve as asked, approve once, deny — each with the
// reason a decision must carry. The buttons' values come back through the bridge's card action
// door (HandleAction), which hands them to Decide; only the people on call may act on cards, as
// everywhere in Beacon.
func (s *Service) pushApprovalCard(r Request) {
	if s.d.Cards == nil || r.Level == "" {
		return
	}
	first := r.Items[0].Title
	if len(r.Items) > 1 {
		first = fmt.Sprintf("%s and %d more", first, len(r.Items)-1)
	}
	val := func(a string) map[string]any { return map[string]any{"a": a, "r": r.ID} }
	approve := feishu.Button(s.t("Approve as asked"), "primary_filled", val("secret_approve"))
	approve["form_action_type"] = "submit"
	approve["name"] = "secreq_approve"
	once := feishu.Button(s.t("Approve once"), "default", val("secret_once"))
	once["form_action_type"] = "submit"
	once["name"] = "secreq_once"
	deny := feishu.Button(s.t("Deny"), "danger", val("secret_deny"))
	deny["form_action_type"] = "submit"
	deny["name"] = "secreq_deny"
	card := feishu.NewCard(
		feishu.Header(s.t("A secret request waits"), s.t("%s asks for %s", r.Agent, first), feishu.Yellow, feishu.Tag(r.Level, "orange")),
		feishu.Markdown(s.t("**Why:** %s\n**Uses:** %s · **For:** %s\n**Level:** %s",
			r.Reason, usesWords(s, r.Uses), durWords(s, r.Duration), r.Level)),
		feishu.Hr(),
		map[string]any{
			"tag": "form", "name": "secreq_" + r.ID,
			"elements": []any{
				map[string]any{"tag": "input", "name": "note",
					"placeholder": Text(s.t("The reason for the decision")), "max_length": 200},
				feishu.Row(approve, once, deny),
			},
		},
	)
	go func() {
		ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
		defer cancel()
		if err := s.d.Cards.SendSecretCard(ctx, card); err != nil {
			s.d.Log.Warn("the approval card could not be sent", "request", r.ID, "err", err)
		}
	}()
}

// Text is feishu.Text for the card builder's callers in this package.
func Text(str string) map[string]any { return feishu.Text(str) }

// usesWords and durWords write a grant's shape for people.
func usesWords(s *Service, uses int64) string {
	if uses == 0 {
		return s.t("unlimited uses")
	}
	return s.t("%d uses", uses)
}

func durWords(s *Service, dur int64) string {
	switch {
	case dur == 0:
		return s.t("no expiry")
	case dur%86400 == 0:
		return s.t("%d days", dur/86400)
	case dur%3600 == 0:
		return s.t("%d hours", dur/3600)
	default:
		return s.t("%d minutes", dur/60)
	}
}

// mustID makes a random id; the random source does not fail in practice, and the clock stands in
// if it ever does.
func mustID() string {
	id, err := newID()
	if err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return id
}

// HookHTTP delivers a secret event over HTTP the way the on-call hooks go: a POST of the JSON
// body, signed with the hook's secret in X-Beacon-Signature (sha256=<hex> HMAC-SHA256), the
// event's name in X-Beacon-Event and the delivery id in X-Beacon-Delivery, with the receiver's
// own headers carried along. The wiring builds one from the webhook the configuration names for
// the agent; a receiver that does not answer 2xx is an error the caller logs.
type HookHTTP struct {
	URL     string
	Secret  string
	Headers map[string]string
	Client  *http.Client
}

// SendSecretHook delivers the payload.
func (h *HookHTTP) SendSecretHook(ctx context.Context, agent, event string, payload []byte) error {
	var p hookPayload
	_ = json.Unmarshal(payload, &p)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", SecretHookUserAgent())
	for name, value := range h.Headers {
		req.Header.Set(name, value)
	}
	if h.Secret != "" {
		req.Header.Set("X-Beacon-Signature", SecretHookSignature(h.Secret, payload))
	}
	req.Header.Set("X-Beacon-Event", event)
	req.Header.Set("X-Beacon-Delivery", p.Delivery)
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s", resp.Status)
	}
	return nil
}

// DecideFromCard is the bridge's door for the Feishu card's buttons: the person taken from the
// on-call list decides the request, as the once-variant's single use allows. The vault checks
// what the person may decide, and the requester itself never reviews its own request.
func (s *Service) DecideFromCard(person, requestID string, approve bool, once bool, note string) error {
	role := ""
	if acc, ok := s.d.Store.Account(person); ok {
		role = acc.Role
	}
	var uses *int64
	if once && approve {
		one := int64(1)
		uses = &one
	}
	_, _, err := s.Decide(DecideActor{Name: person, Kind: "person", Via: "card", Role: role},
		requestID, approve, note, uses, nil)
	return err
}

// CardOverFeishu sends the approval cards through the Feishu app to the configured chat.
type CardOverFeishu struct {
	API interface {
		SendCard(ctx context.Context, chatID string, card feishu.Card, dedupe string) (string, error)
	}
	ChatID string
}

// SendSecretCard posts the card.
func (c *CardOverFeishu) SendSecretCard(ctx context.Context, card feishu.Card) error {
	_, err := c.API.SendCard(ctx, c.ChatID, card, "")
	return err
}
