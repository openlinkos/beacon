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

	"github.com/openlinkos/beacon/internal/sshgate"
	"github.com/openlinkos/beacon/internal/store"
)

// fakeMachines is the SSH channel as far as the socket's connector route needs it.
type fakeMachines struct {
	name, key string
	who       sshgate.Actor
	rechecks  int
}

func (f *fakeMachines) Add(context.Context, sshgate.Actor, sshgate.MachineInput) (sshgate.Added, error) {
	return sshgate.Added{}, nil
}
func (f *fakeMachines) Remove(context.Context, sshgate.Actor, string) error { return nil }
func (f *fakeMachines) Recheck() int                                        { f.rechecks++; return 0 }
func (f *fakeMachines) List(sshgate.Actor, string) (sshgate.Machines, error) {
	return sshgate.Machines{}, nil
}
func (f *fakeMachines) SetPaused(context.Context, sshgate.Actor, bool) error { return nil }
func (f *fakeMachines) PausedSince() time.Time                               { return time.Time{} }
func (f *fakeMachines) CAPublicKey() string                                  { return "" }
func (f *fakeMachines) HostKey() string                                      { return "" }
func (f *fakeMachines) SetConnector(_ context.Context, who sshgate.Actor, name, key string) (sshgate.MachineView, error) {
	if name == "box" {
		return sshgate.MachineView{}, errors.New("box is dialled, not a connector machine")
	}
	f.name, f.key, f.who = name, key, who
	connected := false
	return sshgate.MachineView{Name: name, Address: store.ConnectorAddress, Connector: true, Connected: &connected}, nil
}

func TestSetMachineConnector(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fm := &fakeMachines{}
	srv := httptest.NewServer(Handler(st, nil, fm))
	defer srv.Close()
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(srv.URL, "http://"))
	})
	ctx := context.Background()
	v, err := c.SetMachineConnector(ctx, "nat-box", "ssh-ed25519 AAAA")
	if err != nil || !v.Connector || v.Address != "connector" || v.Connected == nil || *v.Connected {
		t.Fatalf("SetMachineConnector: %+v %v", v, err)
	}
	if fm.name != "nat-box" || fm.key != "ssh-ed25519 AAAA" || fm.who.Via != "cli" || !strings.HasPrefix(fm.who.Name, "cli:") {
		t.Errorf("the channel was asked %+v", fm)
	}
	if _, err := c.SetMachineConnector(ctx, "box", "ssh-ed25519 AAAA"); err == nil || !strings.Contains(err.Error(), "not a connector") {
		t.Errorf("a dialled machine: %v", err)
	}
	off := httptest.NewServer(Handler(st, nil, nil))
	defer off.Close()
	c2 := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(off.URL, "http://"))
	})
	if _, err := c2.SetMachineConnector(ctx, "nat-box", "ssh-ed25519 AAAA"); err == nil || !strings.Contains(err.Error(), "off") {
		t.Errorf("with the channel off: %v", err)
	}
}
