package bridge

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"

	"github.com/openlinkos/beacon/internal/feishu"
)

// sealedCallback is body as Feishu posts it with encryption on: encrypted with key and signed now.
func sealedCallback(t *testing.T, body []byte, key string) *http.Request {
	t.Helper()
	k := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		t.Fatal(err)
	}
	for len(body)%aes.BlockSize != 0 {
		body = append(body, ' ')
	}
	out := make([]byte, aes.BlockSize+len(body))
	if _, err := rand.Read(out[:aes.BlockSize]); err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], body)
	sealed, _ := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(out)})
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	stamp, n := strconv.FormatInt(time.Now().Unix(), 10), base64.RawURLEncoding.EncodeToString(nonce)
	req := httptest.NewRequest(http.MethodPost, "/feishu/callback", bytes.NewReader(sealed))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(larkevent.EventRequestTimestamp, stamp)
	req.Header.Set(larkevent.EventRequestNonce, n)
	req.Header.Set(larkevent.EventSignature, larkevent.Signature(stamp, n, key, string(sealed)))
	return req
}

func cardActionValue(t *testing.T, card feishu.Card, name string) map[string]any {
	t.Helper()
	var search func(any) map[string]any
	search = func(node any) map[string]any {
		switch n := node.(type) {
		case map[string]any:
			if n["a"] == name {
				return n
			}
			for _, child := range n {
				if value := search(child); value != nil {
					return value
				}
			}
		case []any:
			for _, child := range n {
				if value := search(child); value != nil {
					return value
				}
			}
		}
		return nil
	}
	value := search(map[string]any(card))
	if value == nil {
		t.Fatalf("card did not carry a %q action", name)
	}
	return value
}

func feishuCardCallback(t *testing.T, b *Bridge, messageID string, value map[string]any) feishu.Response {
	t.Helper()
	const token = "fixture-token"
	event := map[string]any{
		"operator": map[string]any{"open_id": "ou_a"},
		"action":   map[string]any{"value": value, "form_value": map[string]string{"note": "Following up on the earlier alert."}},
	}
	if messageID != "" {
		event["context"] = map[string]any{"open_message_id": messageID}
	}
	body, err := json.Marshal(map[string]any{
		"schema": "2.0",
		"header": map[string]string{"event_type": "card.action.trigger", "token": token, "event_id": "fixture-event"},
		"event":  event,
	})
	if err != nil {
		t.Fatal(err)
	}
	const key = "fixture-encrypt-key"
	w := httptest.NewRecorder()
	feishu.HTTPHandler(token, key, b.HandleAction).ServeHTTP(w, sealedCallback(t, body, key))
	var out struct {
		Toast struct{ Type, Content string }
		Card  any
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Toast.Type == "" {
		t.Fatalf("SDK callback status %d, response %s", w.Code, w.Body)
	}
	r := feishu.Response{ToastKind: out.Toast.Type, Toast: out.Toast.Content}
	if c, ok := out.Card.(map[string]any); ok {
		r.Card = c
	}
	return r
}

func TestEarlierFeishuCardCannotHandleNewEpisode(t *testing.T) {
	for _, action := range []string{"ack", "note", "silence"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			for _, status := range []string{"firing", "resolved", "firing"} {
				e.now = e.now.Add(time.Minute)
				if err := e.b.HandleWebhook(ctx, hook(status, "critical", "f1")); err != nil {
					t.Fatal(err)
				}
			}
			if len(e.fs.sends) != 2 {
				t.Fatalf("one card per episode: %d", len(e.fs.sends))
			}
			id := GroupID(hook("firing", "critical").GroupKey)
			previous := cardActionValue(t, e.fs.sends[0], action)
			next := cardActionValue(t, e.fs.sends[1], action)
			g, _ := e.st.Group(id)
			if previous["e"] == "" || previous["e"] == nil || previous["e"] == next["e"] || next["e"] != g.Episode {
				t.Fatal("the card actions did not carry their own episode")
			}
			e.now = e.now.Add(16 * time.Minute)
			e.b.Tick(ctx)
			if len(e.push.sent) != 1 {
				t.Fatal("the latest alert did not page through the fixture")
			}
			audits := len(e.st.AuditLog(20))
			response := feishuCardCallback(t, e.b, "om_1", previous)
			g, _ = e.st.Group(id)
			if response.ToastKind != "warning" || response.Card != nil || g.Ack != nil || g.Silence != nil ||
				len(e.am.created) != 0 || len(e.push.cancelled) != 0 || len(e.st.AuditLog(20)) != audits {
				t.Fatalf("earlier card affected the latest alert: toast=%s, acknowledged=%t, silenced=%t, paging cancellations=%d",
					response.ToastKind, g.Ack != nil, g.Silence != nil, len(e.push.cancelled))
			}
			response = feishuCardCallback(t, e.b, "om_2", next)
			if response.ToastKind != "success" || response.Card == nil || len(e.push.cancelled) != 1 {
				t.Fatalf("current card did not handle the alert: toast=%s, paging cancellations=%d", response.ToastKind, len(e.push.cancelled))
			}
		})
	}
}

func TestFeishuCardCallbackIdentity(t *testing.T) {
	for _, c := range []struct {
		name, messageID, episode string
		legacy, allowed          bool
	}{
		{name: "current episode only", episode: "current", allowed: true},
		{name: "current episode and different message", messageID: "om_old", episode: "current"},
		{name: "earlier episode with no message context", episode: "earlier"},
		{name: "empty episode with current message", messageID: "om_1"},
		{name: "legacy current message", messageID: "om_1", legacy: true, allowed: true},
		{name: "legacy different message", messageID: "om_old", legacy: true},
		{name: "legacy without message context", legacy: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.b.HandleWebhook(context.Background(), hook("firing", "critical", "f1")); err != nil {
				t.Fatal(err)
			}
			id := GroupID(hook("firing", "critical").GroupKey)
			value := maps.Clone(cardActionValue(t, e.fs.sends[0], "ack"))
			switch {
			case c.legacy:
				delete(value, "e")
			case c.episode != "current":
				value["e"] = c.episode
			}
			response := feishuCardCallback(t, e.b, c.messageID, value)
			g, _ := e.st.Group(id)
			want := "warning"
			if c.allowed {
				want = "success"
			}
			if response.ToastKind != want || (g.Ack != nil) != c.allowed || (response.Card != nil) != c.allowed {
				t.Errorf("callback status %q, acknowledged %t, returned a card %t", response.ToastKind, g.Ack != nil, response.Card != nil)
			}
		})
	}
}
