package ctl

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/alertmanager"
	"github.com/openlinkos/beacon/internal/bridge"
	"github.com/openlinkos/beacon/internal/store"
)

// fakeSilences stands in for the bridge: one firing group "g1", and it records who did what.
type fakeSilences struct {
	silences []alertmanager.Silence
	actors   []bridge.Actor
	expired  []string
}

func (f *fakeSilences) Silence(_ context.Context, id string, d time.Duration, who bridge.Actor, comment string) (bridge.GroupView, error) {
	if id != "g1" {
		return bridge.GroupView{}, bridge.ErrNotFound
	}
	if comment == "" {
		return bridge.GroupView{}, bridge.ErrBadInput
	}
	f.actors = append(f.actors, who)
	s := alertmanager.Silence{ID: "sil-g1", EndsAt: time.Now().Add(d), CreatedBy: who.Name, Comment: comment,
		Matchers: []alertmanager.Matcher{{Name: "alertname", Value: "ServiceDown", IsEqual: true}}}
	f.silences = append(f.silences, s)
	return bridge.GroupView{ID: id, Status: "firing", Title: "example.com is down", Silence: &store.Silence{ID: s.ID, By: who.Name, Until: s.EndsAt}}, nil
}

func (f *fakeSilences) SilenceLabels(_ context.Context, labels map[string]string, d time.Duration, who bridge.Actor, comment string) (alertmanager.Silence, error) {
	if d > 24*time.Hour || comment == "" {
		return alertmanager.Silence{}, bridge.ErrBadInput
	}
	f.actors = append(f.actors, who)
	s := alertmanager.Silence{ID: "sil-labels", EndsAt: time.Now().Add(d), CreatedBy: who.Name, Comment: comment}
	for k, v := range labels {
		s.Matchers = append(s.Matchers, alertmanager.Matcher{Name: k, Value: v, IsEqual: true})
	}
	f.silences = append(f.silences, s)
	return s, nil
}

func (f *fakeSilences) ExpireSilence(_ context.Context, id string, who bridge.Actor) error {
	for _, s := range f.silences {
		if s.ID == id {
			f.expired = append(f.expired, id)
			f.actors = append(f.actors, who)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeSilences) Silences(context.Context) ([]alertmanager.Silence, error) {
	return f.silences, nil
}

func TestSilenceHandler(t *testing.T) {
	f := &fakeSilences{}
	srv := httptest.NewServer(SilenceHandler(f))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()

	list, err := c.ListSilences(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("no silences yet: %v %v", list, err)
	}
	out, err := c.CreateSilence(ctx, SilenceRequest{Matchers: map[string]string{"alertname": "ServiceDown", "service": "example.com"}, Duration: "4h", Comment: "upgrading"})
	if err != nil || out.Silence.ID != "sil-labels" || len(out.Silence.Matchers) != 2 || out.Group != nil {
		t.Fatalf("CreateSilence with matchers: %+v %v", out, err)
	}
	if len(f.actors) != 1 || f.actors[0].Via != "cli" || !strings.HasPrefix(f.actors[0].Name, "cli:") {
		t.Errorf("the actor is the command line's user: %+v", f.actors)
	}
	out, err = c.CreateSilence(ctx, SilenceRequest{Alert: "g1", Duration: "1h", Comment: "known"})
	if err != nil || out.Group == nil || out.Group.ID != "g1" || out.Silence.ID != "sil-g1" || len(out.Silence.Matchers) != 1 {
		t.Fatalf("CreateSilence for a group: %+v %v", out, err)
	}
	for _, bad := range []SilenceRequest{
		{Duration: "1h", Comment: "x"}, // neither
		{Alert: "g1", Matchers: map[string]string{"a": "b"}, Duration: "1h", Comment: "x"}, // both
		{Alert: "g1", Duration: "soon", Comment: "x"},                                      // not a duration
		{Alert: "g1", Duration: "1h"},                                                      // no comment
		{Matchers: map[string]string{"a": "b"}, Duration: "25h", Comment: "x"},             // too long
	} {
		if _, err := c.CreateSilence(ctx, bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if _, err := c.CreateSilence(ctx, SilenceRequest{Alert: "nope", Duration: "1h", Comment: "x"}); err == nil || !strings.Contains(err.Error(), "no such alert group") {
		t.Errorf("an unknown group: %v", err)
	}
	list, err = c.ListSilences(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSilences: %v %v", list, err)
	}
	if err := c.ExpireSilence(ctx, "sil-labels"); err != nil || len(f.expired) != 1 {
		t.Fatalf("ExpireSilence: %v", err)
	}
	if err := c.ExpireSilence(ctx, "gone"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("an unknown silence: %v", err)
	}
}
