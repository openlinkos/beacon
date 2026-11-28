package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
)

// TestTokenSignInDuringLockout checks that, while the address is locked out, a token that already
// signed in from it still can, and a token new to the address waits like an unknown one.
func TestTokenSignInDuringLockout(t *testing.T) {
	e := newEnv(t, i18n.EN)
	known, fresh := e.token("known", "status:read"), e.token("fresh", "status:read")
	if a := e.browser().signIn(known, "/console"); a.code != http.StatusSeeOther {
		t.Fatalf("the first sign-in: %d %s", a.code, a.body)
	}
	b := e.browser()
	for range 20 {
		if a := b.signIn("not-a-token", "/console"); a.code != http.StatusUnauthorized {
			t.Fatalf("a bad token before the lockout: %d %s", a.code, a.body)
		}
	}
	if a := e.browser().signIn(known, "/console"); a.code != http.StatusSeeOther {
		t.Errorf("a token that signed in from here before: %d %s", a.code, a.body)
	}
	if a := e.browser().signIn(fresh, "/console"); a.code != http.StatusTooManyRequests ||
		!strings.Contains(a.body, "Too many failed sign-ins from here; try again in 10 minutes.") {
		t.Errorf("a token new to the address: %d %s", a.code, a.body)
	}
}
