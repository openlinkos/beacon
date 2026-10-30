package ctl

import (
	"context"
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
	srv := httptest.NewServer(Handler(st))
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
