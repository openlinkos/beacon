package feishu

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
)

const testEncryptKey = "encrypt-key-for-tests"

// encrypted is a card click as Feishu posts it with encryption on: the body, and headers signed at ts
// with nonce.
func encrypted(t *testing.T, ts time.Time, nonce string, sign bool) *http.Request {
	t.Helper()
	event := `{"schema":"2.0","header":{"event_id":"ev-` + nonce + `","token":"vt","create_time":"1","event_type":"card.action.trigger","tenant_key":"tk","app_id":"cli_a"},` +
		`"event":{"operator":{"open_id":"ou_a"},"action":{"value":{"a":"ack","g":"0123456789ab"}},"context":{"open_message_id":"om_1"}}}`
	key := sha256.Sum256([]byte(testEncryptKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(event)
	for len(plain)%aes.BlockSize != 0 {
		plain = append(plain, ' ')
	}
	out := make([]byte, aes.BlockSize+len(plain))
	if _, err := rand.Read(out[:aes.BlockSize]); err != nil {
		t.Fatal(err)
	}
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], plain)
	body, _ := json.Marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(out)})
	r := httptest.NewRequest(http.MethodPost, "/hooks/feishu", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	if sign {
		stamp := strconv.FormatInt(ts.Unix(), 10)
		r.Header.Set(larkevent.EventRequestTimestamp, stamp)
		r.Header.Set(larkevent.EventRequestNonce, nonce)
		r.Header.Set(larkevent.EventSignature, larkevent.Signature(stamp, nonce, testEncryptKey, string(body)))
	}
	return r
}

// TestHTTPCallbacksMustBeFresh checks that a signed HTTP callback is taken once, within five minutes
// of its timestamp, and that one the SDK refuses does not use up its nonce.
func TestHTTPCallbacksMustBeFresh(t *testing.T) {
	now := time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC)
	var clicks atomic.Int32
	h := httpHandler("vt", testEncryptKey, func(context.Context, Action) Response {
		clicks.Add(1)
		return Response{Toast: "ok"}
	}, func() time.Time { return now })
	serve := func(r *http.Request) int {
		rec := httptest.NewRecorder()
		h(rec, r)
		return rec.Code
	}

	if code := serve(encrypted(t, now, "n-1", true)); code != http.StatusOK || clicks.Load() != 1 {
		t.Fatalf("a fresh callback: %d, %d clicks", code, clicks.Load())
	}
	if code := serve(encrypted(t, now, "n-1", true)); code == http.StatusOK || clicks.Load() != 1 {
		t.Errorf("a replayed nonce: %d, %d clicks", code, clicks.Load())
	}
	for _, ts := range []time.Time{now.Add(-CallbackSkew - time.Second), now.Add(CallbackSkew + time.Second)} {
		if code := serve(encrypted(t, ts, "n-"+ts.Format("150405"), true)); code == http.StatusOK {
			t.Errorf("a callback stamped %s: %d", ts, code)
		}
	}
	noNonce := encrypted(t, now, "n-2", true)
	noNonce.Header.Del(larkevent.EventRequestNonce)
	if code := serve(noNonce); code == http.StatusOK {
		t.Errorf("a callback without a nonce: %d", code)
	}
	if code := serve(encrypted(t, now, "n-3", false)); code == http.StatusOK {
		t.Errorf("an unsigned callback: %d", code)
	}
	if clicks.Load() != 1 {
		t.Fatalf("refused callbacks reached the handler: %d clicks", clicks.Load())
	}

	// a forged signature does not use up the nonce of the genuine callback
	forged := encrypted(t, now, "n-4", true)
	forged.Header.Set(larkevent.EventSignature, strings.Repeat("0", 64))
	if code := serve(forged); code == http.StatusOK {
		t.Fatalf("a forged signature: %d", code)
	}
	if code := serve(encrypted(t, now, "n-4", true)); code != http.StatusOK || clicks.Load() != 2 {
		t.Errorf("the genuine callback after a forgery with its nonce: %d, %d clicks", code, clicks.Load())
	}
}

// TestHTTPCallbacksNeedTheEncryptKey checks that without an encrypt key no HTTP callback is taken.
func TestHTTPCallbacksNeedTheEncryptKey(t *testing.T) {
	called := false
	h := HTTPHandler("vt", "", func(context.Context, Action) Response { called = true; return Response{} })
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/hooks/feishu", strings.NewReader(`{"schema":"2.0","header":{"token":"vt","event_type":"card.action.trigger"},"event":{"operator":{"open_id":"ou_a"},"action":{"value":{}}}}`))
	h(rec, r)
	if rec.Code == http.StatusOK || called {
		t.Fatalf("a callback without encryption was taken: %d", rec.Code)
	}
}
