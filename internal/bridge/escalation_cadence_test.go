package bridge

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/openlinkos/beacon/internal/config"
)

type barkCall func(context.Context, string, string, string) error

func (f barkCall) Send(ctx context.Context, title, body, link string) error {
	return f(ctx, title, body, link)
}

func runBarkFixture(t *testing.T, healthcheck func(context.Context, bool) error) (*env, <-chan time.Time) {
	t.Helper()
	e := newEnv(t)
	start := time.Now()
	e.b.d.Now = func() time.Time { return e.now.Add(time.Since(start)) }
	e.b.d.Config.Escalation["critical"] = config.Policy{Channel: "none", Bark: config.Duration(20 * time.Second)}
	received := make(chan time.Time, 4)
	e.b.d.Bark = barkCall(func(context.Context, string, string, string) error {
		received <- e.b.now()
		return nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.b.Run(ctx, healthcheck)
	}()
	t.Cleanup(func() { cancel(); <-done })
	synctest.Wait()
	return e, received
}

func TestRunDispatchesBarkNearPolicyDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, received := runBarkFixture(t, nil)
		// The policy is due just after the former 30s tick, which would otherwise wait until 45s.
		synctest.Sleep(10*time.Second + 100*time.Millisecond)
		w := hook("firing", "critical", "f1")
		if err := e.b.HandleWebhook(t.Context(), w); err != nil {
			t.Fatal(err)
		}
		notifiedAt := e.b.now()
		synctest.Sleep(20*time.Second - time.Nanosecond)
		if len(received) != 0 {
			t.Fatal("Bark sent before the configured delay")
		}
		synctest.Sleep(2 * time.Second)
		if len(received) != 1 {
			t.Fatalf("wanted a Bark delivery within two seconds of the policy deadline, got %d", len(received))
		}
		at := <-received
		if got := at.Sub(notifiedAt); got != 21900*time.Millisecond {
			t.Fatalf("Bark accepted at %s after the alert, want 21.9s", got)
		}
		g, _ := e.st.Group(GroupID(w.GroupKey))
		if !g.Barked.Equal(at) || latestNotification(&g, "bark").Result != "ok" {
			t.Fatal("the accepted Bark delivery was not recorded")
		}
		if err := e.b.HandleWebhook(t.Context(), w); err != nil {
			t.Fatal(err)
		}
		synctest.Sleep(time.Minute)
		if len(received) != 0 {
			t.Fatal("an accepted delivery was repeated on later ticks")
		}
		t.Logf("Bark accepted after %.1fs (34.9s with the former 15s tick at this phase)", at.Sub(notifiedAt).Seconds())
	})
}

func TestRunEscalationContinuesWhileHealthcheckWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{}, 1)
		e, received := runBarkFixture(t, func(ctx context.Context, _ bool) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		})
		synctest.Sleep(time.Minute)
		if len(started) != 1 {
			t.Fatal("the first healthcheck never started")
		}
		if err := e.b.HandleWebhook(t.Context(), hook("firing", "critical", "f1")); err != nil {
			t.Fatal(err)
		}
		synctest.Sleep(22 * time.Second)
		if len(received) != 1 {
			t.Fatal("the blocked healthcheck delayed the due phone notification")
		}
	})
}
