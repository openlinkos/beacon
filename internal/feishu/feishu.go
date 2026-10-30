// Package feishu is the Feishu (Lark) self-built app: it sends interactive cards to the alert group,
// updates them in place, buzzes people, and receives button clicks, either over Feishu's long
// connection (no public callback URL needed) or as HTTP callbacks.
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Card is a card in Feishu's card JSON 2.0, as a JSON object.
type Card map[string]any

// API is what the bridge needs from the app. Every call counts against the app's monthly API quota.
type API interface {
	// SendCard posts a card to a chat. dedupe makes Feishu drop a repeat of the same message within an hour.
	SendCard(ctx context.Context, chatID string, card Card, dedupe string) (messageID string, err error)
	// PatchCard replaces a sent card (it must have been sent with update_multi).
	PatchCard(ctx context.Context, messageID string, card Card) error
	// UrgentApp buzzes people in the Feishu app about a message the bot sent.
	UrgentApp(ctx context.Context, messageID string, openIDs []string) error
}

// BaseURL returns the open platform address for domain (feishu or lark).
func BaseURL(domain string) string {
	if domain == "lark" {
		return lark.LarkBaseUrl
	}
	return lark.FeishuBaseUrl
}

// Client is the API backed by the official SDK.
type Client struct {
	c     *lark.Client
	count func()
}

// New creates a client. count is called once per API call, for the quota budget.
func New(appID, appSecret, domain string, log *slog.Logger, count func()) *Client {
	c := lark.NewClient(appID, appSecret,
		lark.WithOpenBaseUrl(BaseURL(domain)),
		lark.WithReqTimeout(10*time.Second),
		lark.WithLogger(sdkLogger{log}),
		lark.WithLogLevel(larkcore.LogLevelWarn))
	if count == nil {
		count = func() {}
	}
	return &Client{c: c, count: count}
}

func cardJSON(card Card) (string, error) {
	b, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// SendCard implements API.
func (c *Client) SendCard(ctx context.Context, chatID string, card Card, dedupe string) (string, error) {
	content, err := cardJSON(card)
	if err != nil {
		return "", err
	}
	body := larkim.NewCreateMessageReqBodyBuilder().ReceiveId(chatID).MsgType("interactive").Content(content)
	if dedupe != "" {
		body = body.Uuid(dedupe)
	}
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType("chat_id").Body(body.Build()).Build()
	c.count()
	resp, err := c.c.Im.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("feishu send: %w", err)
	}
	if !resp.Success() || resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("feishu send: code %d: %s", resp.Code, resp.Msg)
	}
	return *resp.Data.MessageId, nil
}

// PatchCard implements API.
func (c *Client) PatchCard(ctx context.Context, messageID string, card Card) error {
	content, err := cardJSON(card)
	if err != nil {
		return err
	}
	req := larkim.NewPatchMessageReqBuilder().MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().Content(content).Build()).Build()
	c.count()
	resp, err := c.c.Im.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu patch: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu patch: code %d: %s", resp.Code, resp.Msg)
	}
	return nil
}

// UrgentApp implements API.
func (c *Client) UrgentApp(ctx context.Context, messageID string, openIDs []string) error {
	req := larkim.NewUrgentAppMessageReqBuilder().MessageId(messageID).UserIdType("open_id").
		UrgentReceivers(larkim.NewUrgentReceiversBuilder().UserIdList(openIDs).Build()).Build()
	c.count()
	resp, err := c.c.Im.Message.UrgentApp(ctx, req)
	if err != nil {
		return fmt.Errorf("feishu buzz: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu buzz: code %d: %s", resp.Code, resp.Msg)
	}
	return nil
}

// ---- Callbacks

// Action is a click on a card button (or a form submission).
type Action struct {
	OpenID    string
	Value     map[string]any
	Form      map[string]any
	MessageID string
}

// Response is what the card shows after a click: a toast and, optionally, the card's new content.
// Returning the card here updates it without spending an API call.
type Response struct {
	Toast     string
	ToastKind string // success, info, warning or error
	Card      Card
}

// Handler handles a click. It must answer within three seconds.
type Handler func(ctx context.Context, a Action) Response

func toSDK(r Response) *callback.CardActionTriggerResponse {
	out := &callback.CardActionTriggerResponse{}
	if r.Toast != "" {
		kind := r.ToastKind
		if kind == "" {
			kind = "info"
		}
		out.Toast = &callback.Toast{Type: kind, Content: r.Toast}
	}
	if r.Card != nil {
		out.Card = &callback.Card{Type: "raw", Data: map[string]any(r.Card)}
	}
	return out
}

func dispatch(verificationToken, encryptKey string, h Handler) *dispatcher.EventDispatcher {
	return dispatcher.NewEventDispatcher(verificationToken, encryptKey).
		OnP2CardActionTrigger(func(ctx context.Context, ev *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			if ev == nil || ev.Event == nil || ev.Event.Operator == nil || ev.Event.Action == nil {
				return nil, errors.New("card callback without an operator or action")
			}
			a := Action{OpenID: ev.Event.Operator.OpenID, Value: ev.Event.Action.Value, Form: ev.Event.Action.FormValue}
			if ev.Event.Context != nil {
				a.MessageID = ev.Event.Context.OpenMessageID
			}
			ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
			defer cancel()
			return toSDK(h(ctx, a)), nil
		})
}

// HTTPHandler serves card callbacks that Feishu posts to a public URL (callback mode "http").
func HTTPHandler(verificationToken, encryptKey string, h Handler) http.HandlerFunc {
	return httpserverext.NewEventHandlerFunc(dispatch(verificationToken, encryptKey, h), larkevent.WithLogLevel(larkcore.LogLevelWarn))
}

// Listener keeps the long connection over which Feishu delivers card callbacks (callback mode
// "websocket"). Feishu hands each callback to one connection, so only one process may listen.
type Listener struct {
	client *larkws.Client
	ready  atomic.Bool
	log    *slog.Logger
}

// Listen prepares the long connection; Run starts it.
func Listen(appID, appSecret, domain string, h Handler, log *slog.Logger) *Listener {
	l := &Listener{log: log}
	l.client = larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(dispatch("", "", h)),
		larkws.WithDomain(BaseURL(domain)),
		larkws.WithAutoReconnect(true),
		larkws.WithLogger(sdkLogger{log}),
		larkws.WithLogLevel(larkcore.LogLevelWarn),
		larkws.WithOnReady(func() { l.ready.Store(true); log.Info("feishu long connection ready") }),
		larkws.WithOnReconnected(func() { l.ready.Store(true); log.Info("feishu long connection back") }),
		larkws.WithOnDisconnected(func() { l.ready.Store(false); log.Warn("feishu long connection lost") }),
		larkws.WithOnReconnecting(func() { l.ready.Store(false) }),
		larkws.WithOnError(func(err error) { log.Warn("feishu long connection error", "err", err) }),
	)
	return l
}

// Run blocks until ctx is done, reconnecting as needed.
func (l *Listener) Run(ctx context.Context) error {
	err := l.client.Start(ctx)
	l.ready.Store(false)
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// Ready reports whether the long connection is up.
func (l *Listener) Ready() bool { return l.ready.Load() }

// sdkLogger sends the SDK's logs to slog.
type sdkLogger struct{ l *slog.Logger }

func (s sdkLogger) Debug(_ context.Context, v ...any) {
	s.l.Debug("feishu sdk", "msg", fmt.Sprint(v...))
}
func (s sdkLogger) Info(_ context.Context, v ...any) { s.l.Info("feishu sdk", "msg", fmt.Sprint(v...)) }
func (s sdkLogger) Warn(_ context.Context, v ...any) { s.l.Warn("feishu sdk", "msg", fmt.Sprint(v...)) }
func (s sdkLogger) Error(_ context.Context, v ...any) {
	s.l.Error("feishu sdk", "msg", fmt.Sprint(v...))
}
