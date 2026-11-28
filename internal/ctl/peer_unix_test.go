//go:build linux || darwin

package ctl

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/openlinkos/beacon/internal/store"
)

// TestAuditNamesThePeer checks that a command over the socket is recorded under the user the operating
// system says connected, with the name the client sent kept only as a claim.
func TestAuditNamesThePeer(t *testing.T) {
	dir, err := os.MkdirTemp("", "ctl") // short: a socket's path has a small limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ln, err := Listen(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: Handler(st, nil, nil), ConnContext: ConnContext}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	me := "uid-" + strconv.Itoa(os.Getuid())
	if u, err := user.Current(); err == nil && userRe.MatchString(u.Username) {
		me = u.Username
	}
	for i, tc := range []struct{ claim, want string }{
		{claim: "mallory", want: "cli:" + me + " (claims mallory)"},
		{claim: me, want: "cli:" + me},
		{claim: "", want: "cli:" + me},
	} {
		c := newClient(func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", Path(dir))
		})
		c.hc.Transport = claiming{base: c.hc.Transport, claim: tc.claim}
		if _, err := c.CreateToken(context.Background(), TokenRequest{Name: "t-" + strconv.Itoa(i), Scopes: []string{"status:read"}, TTL: "1h"}); err != nil {
			t.Fatal(err)
		}
		if got := st.AuditLog(1)[0].Actor; got != tc.want {
			t.Errorf("claiming %q: recorded as %q, want %q", tc.claim, got, tc.want)
		}
	}
}

// claiming sends claim as the user's name, whatever the process is.
type claiming struct {
	base  http.RoundTripper
	claim string
}

func (c claiming) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if c.claim == "" {
		r.Header.Del(UserHeader)
	} else {
		r.Header.Set(UserHeader, c.claim)
	}
	return c.base.RoundTrip(r)
}
