package ctl

import (
	"context"
	"net"
	"net/http"
	"os/user"
	"strconv"
)

// peerConn is a connection whose peer's user id the listener learnt from the operating system.
type peerConn struct {
	net.Conn
	uid int
}

type peerKey struct{}

// ConnContext gives each request on the socket the peer's user id, where the listener learnt it, so
// that the audit log names whoever runs a command by who they are rather than by what the client says.
// The socket's http.Server takes it as its ConnContext.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	if pc, ok := c.(*peerConn); ok {
		return context.WithValue(ctx, peerKey{}, pc.uid)
	}
	return ctx
}

// cliActor is who runs a command, for the audit log. Where the platform tells the socket the peer's
// user id, it is that user: "cli:root", or "cli:uid-1000" for an id without a name. The name the
// client sends (UserHeader) is kept only as a claim beside it when it differs: "cli:root (claims
// alice)". Where the platform cannot tell, the client's name is all there is: "cli:alice".
func cliActor(r *http.Request) string {
	claim := r.Header.Get(UserHeader)
	if !userRe.MatchString(claim) {
		claim = ""
	}
	uid, known := r.Context().Value(peerKey{}).(int)
	if !known {
		if claim == "" {
			claim = "unknown"
		}
		return "cli:" + claim
	}
	name := "uid-" + strconv.Itoa(uid)
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil && userRe.MatchString(u.Username) {
		name = u.Username
	}
	if claim == "" || claim == name {
		return "cli:" + name
	}
	return "cli:" + name + " (claims " + claim + ")"
}
