package ctl

import (
	"context"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/store"
)

func TestDrill(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	type rec struct {
		until time.Time
		by    string
	}
	var recorded []rec
	srv := httptest.NewServer(DrillHandler(st, func(_ context.Context, until time.Time, by string) error {
		recorded = append(recorded, rec{until, by})
		return nil
	}))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()

	if _, err := c.Drill(ctx, "3h"); err == nil {
		t.Error("a drill longer than 2h started")
	}
	if _, err := c.Drill(ctx, "off"); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 0 {
		t.Fatalf("no drill ran, so none started or ended: %+v", recorded)
	}
	on, err := c.Drill(ctx, "15m")
	if err != nil || time.Until(on.Until) < 14*time.Minute {
		t.Fatalf("drill fail 15m: %+v %v", on, err)
	}
	if s, err := c.DrillStatus(ctx); err != nil || !s.Until.Equal(on.Until) {
		t.Errorf("drill status: %+v %v", s, err)
	}
	if _, err := c.Drill(ctx, "off"); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.DrillStatus(ctx); !s.Until.IsZero() {
		t.Errorf("after drill off: %+v", s)
	}
	if len(recorded) != 2 || !recorded[0].until.Equal(on.Until) || !recorded[1].until.IsZero() || !strings.HasPrefix(recorded[0].by, "cli:") {
		t.Errorf("the start with its end, then the end: %+v", recorded)
	}
	if n := len(st.AuditLog(10)); n != 3 {
		t.Errorf("each drill command is audited: %d", n)
	}
}
