package oauth

import (
	"fmt"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/openlinkos/beacon/internal/api"
	"github.com/openlinkos/beacon/internal/clientip"
)

// TestRegistrationsCountPerNetwork: dynamic registrations are counted per IPv4 address and per IPv6
// /64, the block one host usually holds, so that a host cannot pass the limit by changing addresses.
func TestRegistrationsCountPerNetwork(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := &Server{Auth: &api.Auth{IP: clientip.New([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, false)}, Now: func() time.Time { return now }}
	from := func(addr string) bool {
		r := httptest.NewRequest("POST", RegisterPath, nil)
		r.RemoteAddr = "127.0.0.1:40000"
		r.Header.Set("X-Forwarded-For", addr)
		return s.allowRegister(r)
	}
	for i := range registerLimit {
		if !from(fmt.Sprintf("2001:db8:1:2::%x", i+1)) {
			t.Fatalf("registration %d from one /64 refused", i+1)
		}
	}
	if from("2001:db8:1:2:ffff:ffff:ffff:fffe") {
		t.Error("another address in the same /64 passed the limit")
	}
	if !from("2001:db8:1:3::1") {
		t.Error("the next /64 was refused")
	}
	for i := range registerLimit {
		if !from("192.0.2.10") {
			t.Fatalf("registration %d from an IPv4 address refused", i+1)
		}
	}
	if from("192.0.2.10") {
		t.Error("an IPv4 address passed the limit")
	}
	if !from("192.0.2.11") {
		t.Error("the next IPv4 address was refused")
	}
	now = now.Add(registerWindow)
	if !from("2001:db8:1:2::99") {
		t.Error("the limit outlived its window")
	}
}
