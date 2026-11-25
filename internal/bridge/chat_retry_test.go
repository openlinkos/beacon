package bridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/config"
	"github.com/openlinkos/beacon/internal/notify"
)

func TestFeishuChatFailureRetriesAcrossRestart(t *testing.T) {
	e := newEnv(t)
	deps := e.b.d
	var calls atomic.Int64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			// The server reached HTTP successfully but rejected this push.
			_, _ = io.WriteString(w, `{"StatusCode":19021}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer sink.Close()
	deps.Bot = &notify.GroupBot{URL: sink.URL}
	deps.Feishu = nil
	deps.Pushover = nil
	deps.Config.Escalation["critical"] = config.Policy{Channel: "webhook"}
	b := New(deps)
	w := hook("firing", "critical", "first-chat-retry")
	ctx := context.Background()
	start := e.now
	if err := b.HandleWebhook(ctx, w); err != nil {
		t.Fatal(err)
	}
	id := GroupID(w.GroupKey)
	g, _ := e.st.Group(id)
	if calls.Load() != 1 || g.Channel != "none" || latestNotification(&g, "webhook").Result != "failed" {
		t.Fatalf("rejected first send was not recorded: calls=%d channel=%q attempts=%+v", calls.Load(), g.Channel, g.Notifications)
	}
	b = New(deps)
	e.now = start.Add(notificationRetry - time.Second)
	b.Tick(ctx)
	if calls.Load() != 1 {
		t.Fatal("retried before the interval")
	}
	e.now = start.Add(notificationRetry)
	b.Tick(ctx)
	g, _ = e.st.Group(id)
	if calls.Load() != 2 || g.Channel != "webhook" || !g.ChannelAt.Equal(e.now) || !g.NotifiedAt.Equal(start) {
		t.Fatalf("retry did not deliver without resetting escalation: calls=%d channel=%q notified=%s accepted=%s",
			calls.Load(), g.Channel, g.NotifiedAt, g.ChannelAt)
	}
	e.now = e.now.Add(2 * notificationRetry)
	b.Tick(ctx)
	if calls.Load() != 2 {
		t.Fatal("resent an accepted incident")
	}
}
