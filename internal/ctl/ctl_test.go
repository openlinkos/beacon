package ctl

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openlinkos/status/internal/store"
)

func TestHandler(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(Handler(st, nil))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()

	out, err := c.CreateToken(ctx, TokenRequest{Name: "claude-code", Scopes: []string{"status:read"}, TTL: "30d"})
	if err != nil || !strings.HasPrefix(out.Secret, store.TokenPrefix) || time.Until(out.Token.Expires) < 29*24*time.Hour {
		t.Fatalf("CreateToken: %+v %v", out, err)
	}
	if _, err := c.CreateToken(ctx, TokenRequest{Name: "x", Scopes: []string{"root"}, TTL: "1h"}); err == nil || !strings.Contains(err.Error(), "unknown scope") {
		t.Errorf("bad scope: %v", err)
	}
	if _, err := c.CreateToken(ctx, TokenRequest{Name: "x", Scopes: []string{"status:read"}, TTL: "soon"}); err == nil {
		t.Error("bad ttl accepted")
	}
	list, err := c.Tokens(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "claude-code" || !strings.HasPrefix(list[0].CreatedBy, "cli:") {
		t.Fatalf("Tokens: %+v %v", list, err)
	}
	if err := c.RevokeToken(ctx, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if err := c.RevokeToken(ctx, "claude-code"); err == nil {
		t.Error("revoking twice should fail")
	}

	d, err := c.Drill(ctx, "15m")
	if err != nil || time.Until(d.Until) < 14*time.Minute {
		t.Fatalf("Drill: %+v %v", d, err)
	}
	if _, err := c.Drill(ctx, "5h"); err == nil {
		t.Error("a 5h drill should be refused")
	}
	if s, _ := c.DrillStatus(ctx); s.Until.IsZero() {
		t.Error("drill not stored")
	}
	if _, err := c.Drill(ctx, "off"); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.DrillStatus(ctx); !s.Until.IsZero() {
		t.Error("drill not ended")
	}
	if n := len(st.AuditLog(10)); n != 4 {
		t.Errorf("audit entries: %d", n)
	}
}

func TestParseTTL(t *testing.T) {
	if d, err := ParseTTL("365d"); err != nil || d != 365*24*time.Hour {
		t.Errorf("365d: %v %v", d, err)
	}
	if d, err := ParseTTL("90m"); err != nil || d != 90*time.Minute {
		t.Errorf("90m: %v %v", d, err)
	}
	for _, bad := range []string{"d", "-1d", "x"} {
		if _, err := ParseTTL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEnroll(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	active := func(loc string) error {
		if loc != "cdn-pre-us1" {
			return errors.New("location " + loc + " is not active")
		}
		return nil
	}
	srv := httptest.NewServer(Handler(st, active))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()

	out, err := c.Enroll(ctx, EnrollRequest{Kind: "probe", Subject: "cdn-pre-us1"})
	if err != nil || out.Token.Name != "probe-cdn-pre-us1" || out.Token.Subject != "cdn-pre-us1" || !out.Token.Has("probe:write") {
		t.Fatalf("Enroll: %+v %v", out, err)
	}
	if _, err := c.Enroll(ctx, EnrollRequest{Kind: "probe", Subject: "cdn-pre-us1"}); err == nil || !strings.Contains(err.Error(), "-rotate") {
		t.Fatalf("enrolling twice must ask for -rotate: %v", err)
	}
	again, err := c.Enroll(ctx, EnrollRequest{Kind: "probe", Subject: "cdn-pre-us1", Rotate: true})
	if err != nil || again.Secret == out.Secret {
		t.Fatalf("rotate: %+v %v", again, err)
	}
	if _, ok := st.VerifyToken(out.Secret); ok {
		t.Error("the old token must stop working after a rotation")
	}
	if _, err := c.Enroll(ctx, EnrollRequest{Kind: "probe", Subject: "cdn-gone"}); err == nil || !strings.Contains(err.Error(), "not active") {
		t.Errorf("an unknown or expired location: %v", err)
	}
	if _, err := c.Enroll(ctx, EnrollRequest{Kind: "robot", Subject: "x"}); err == nil {
		t.Error("unknown kind accepted")
	}
	h, err := c.Enroll(ctx, EnrollRequest{Kind: "host", Subject: "worker-us"})
	if err != nil || !h.Token.Has("host:write") {
		t.Fatalf("host: %+v %v", h, err)
	}
	list, _ := c.Tokens(ctx)
	if len(list) != 2 || list[0].Subject == "" {
		t.Fatalf("tokens %+v", list)
	}
}
