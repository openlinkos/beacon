package sshgate

import (
	"context"
	"errors"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/store"
)

const sshCheckTimeout = 8 * time.Second

// SSHCheckStatus describes which part of the machine handshake Beacon observed.
type SSHCheckStatus string

const (
	SSHCheckReady                    SSHCheckStatus = "ready"
	SSHCheckDisconnected             SSHCheckStatus = "connector_disconnected"
	SSHCheckConnectorBusy            SSHCheckStatus = "connector_busy"
	SSHCheckForwardFailed            SSHCheckStatus = "connector_forward_failed"
	SSHCheckUnreachable              SSHCheckStatus = "unreachable"
	SSHCheckHostKeyUnavailable       SSHCheckStatus = "host_key_unavailable"
	SSHCheckHostKeyMismatch          SSHCheckStatus = "host_key_mismatch"
	SSHCheckHandshakeFailed          SSHCheckStatus = "handshake_failed"
	SSHCheckAuthenticationIncomplete SSHCheckStatus = "authentication_incomplete"
	SSHCheckAuthenticationSkipped    SSHCheckStatus = "authentication_skipped"
	SSHCheckTimedOut                 SSHCheckStatus = "timed_out"
	sshCheckInterrupted              SSHCheckStatus = "interrupted" // audit only
)

// ErrSSHCheckUnavailable means Beacon could not prepare the certificate or persist its audit.
var ErrSSHCheckUnavailable = errors.New("could not prepare or audit the machine SSH check")

var errCheckHostKey = errors.New("the machine presented a different host key")

// SSHCheck reports a point-in-time attempt by Beacon. Ready means the pinned key matched and
// the machine accepted Beacon's certificate. The check never asks the machine to open a session.
type SSHCheck struct {
	ID        string         `json:"check_id"`
	Machine   string         `json:"machine"`
	Route     string         `json:"route"`
	Status    SSHCheckStatus `json:"status"`
	CheckedAt time.Time      `json:"checked_at"`
}

// CheckSSH checks a single enrolled machine through the ordinary SSH authorization and pause
// policy. A nonempty registration binds the console form to the page that offered the check.
func (g *Gate) CheckSSH(ctx context.Context, who Actor, machine, registration string) (SSHCheck, error) {
	m, err := g.machineFor(who, machine)
	if err != nil {
		return SSHCheck{}, err
	}
	if registration != "" && registration != m.Registration() {
		return SSHCheck{}, ErrMachineChanged
	}
	if err := g.requireLeader(); err != nil {
		return SSHCheck{}, err
	}
	result := SSHCheck{ID: newID(g.now()), Machine: m.Name, Route: "direct"}
	if m.Connector() {
		result.Route = "connector"
	}
	audit := store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "machine_ssh_check", Target: m.Name, Detail: result.ID + " started"}
	if err := g.d.Store.Audit(audit); err != nil {
		return SSHCheck{}, ErrSSHCheckUnavailable
	}

	ctx, cancel := context.WithTimeout(ctx, sshCheckTimeout)
	defer cancel()
	untrack := g.track(cancel, who, m.Name, &m)
	defer untrack()
	// Re-read after tracking so that a pause or revocation concurrent with admission cannot
	// let the check start on the stale grant.
	if err = g.stillCheck(who, m); err == nil {
		result.Status = SSHCheckTimedOut
		if ctx.Err() == nil {
			result.Status, err = g.checkMachine(ctx, m, who, result.ID)
		}
	}
	if currentErr := g.stillCheck(who, m); currentErr != nil {
		err = currentErr
	}
	if err != nil || (ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		result.Status = sshCheckInterrupted
	}
	result.CheckedAt = g.now().UTC()
	audit.Detail = result.ID + " " + string(result.Status)
	if auditErr := g.d.Store.Audit(audit); auditErr != nil {
		return SSHCheck{}, ErrSSHCheckUnavailable
	}
	if err != nil {
		return SSHCheck{}, err
	}
	if result.Status == sshCheckInterrupted {
		return SSHCheck{}, ErrSSHCheckUnavailable
	}
	text := i18n.T(g.d.Lang, "could not verify machine SSH")
	if result.Status == SSHCheckReady {
		text = i18n.T(g.d.Lang, "verified the machine's host key and SSH certificate login")
	}
	g.echo.queue(who.Name, m.Name, text)
	return result, nil
}

// stillCheck reports why who may no longer work on m as it was when the work began; nil while it may.
// The pause switch, the credential, the machine's access and its identity are read afresh, and this
// instance must still hold the channel.
func (g *Gate) stillCheck(who Actor, m store.Machine) error {
	current, err := g.machineFor(who, m.Name)
	if err != nil {
		return err
	}
	if !sameMachine(m, current) {
		return ErrMachineChanged
	}
	return g.requireLeader()
}

func (g *Gate) checkMachine(ctx context.Context, m store.Machine, who Actor, id string) (SSHCheckStatus, error) {
	var relay *connector
	if m.Connector() {
		relay = g.connectors.get(m.Name)
		if relay == nil {
			return SSHCheckDisconnected, nil
		}
	}
	key, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(m.HostKey))
	if key == nil {
		return SSHCheckHostKeyUnavailable, nil
	}
	signer, err := g.cert(m, who, id)
	if err != nil {
		return "", ErrSSHCheckUnavailable
	}
	if relay == nil {
		conn, err := g.d.Dial(ctx, "tcp", m.Address)
		if err != nil {
			return checkTimeout(ctx, err, SSHCheckUnreachable), nil
		}
		return checkHandshake(ctx, m, conn, key, signer, false), nil
	}
	return relay.checkSSH(ctx, m, key, signer), nil
}

// The SSH mux waits for a channel-open reply and does not accept a context. Allow just one
// outstanding check per connector so a slow peer cannot accumulate requests, and leave the
// shared connection alone when the check's caller times out.
func (c *connector) checkSSH(ctx context.Context, m store.Machine, key ssh.PublicKey, signer ssh.Signer) SSHCheckStatus {
	if !c.checking.CompareAndSwap(false, true) {
		return SSHCheckConnectorBusy
	}
	done := make(chan SSHCheckStatus, 1)
	go func() {
		status := SSHCheckTimedOut
		if ctx.Err() == nil {
			status = SSHCheckForwardFailed
			if conn, err := c.open(); err == nil {
				status = checkHandshake(ctx, m, conn, key, signer, true)
			}
		}
		c.checking.Store(false)
		done <- status
	}()
	select {
	case <-ctx.Done():
		return SSHCheckTimedOut
	case status := <-done:
		return status
	}
}

func checkHandshake(ctx context.Context, m store.Machine, conn net.Conn, key ssh.PublicKey, signer ssh.Signer, shared bool) SSHCheckStatus {
	defer func() { _ = conn.Close() }()
	if ctx.Err() != nil {
		return SSHCheckTimedOut
	}
	// Closing a channel on the shared connector can itself wait for the transport. The outer
	// connector check remains bounded even in that case. Close a direct socket on cancellation.
	if !shared {
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	verifyKey := ssh.FixedHostKey(key)
	authAttempted := false
	auth := ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		authAttempted = true
		return []ssh.Signer{signer}, nil
	})
	cfg := machineClientConfig(m, key, auth)
	cfg.HostKeyCallback = func(host string, addr net.Addr, presented ssh.PublicKey) error {
		if err := verifyKey(host, addr, presented); err != nil {
			return errCheckHostKey
		}
		return nil
	}
	client, _, _, err := ssh.NewClientConn(conn, m.Address, cfg)
	if err == nil {
		_ = client.Close()
		if authAttempted {
			return SSHCheckReady
		}
		return SSHCheckAuthenticationSkipped
	}
	if errors.Is(err, errCheckHostKey) {
		return SSHCheckHostKeyMismatch
	}
	if authAttempted {
		return checkTimeout(ctx, err, SSHCheckAuthenticationIncomplete)
	}
	return checkTimeout(ctx, err, SSHCheckHandshakeFailed)
}

func checkTimeout(ctx context.Context, err error, fallback SSHCheckStatus) SSHCheckStatus {
	var netErr net.Error
	if ctx.Err() != nil || errors.As(err, &netErr) && netErr.Timeout() {
		return SSHCheckTimedOut
	}
	return fallback
}
