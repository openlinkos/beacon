package bridge

import (
	"errors"
	"fmt"

	"github.com/openlinkos/beacon/internal/store"
)

// refusedBeforeLookup marks a refusal made before any token is looked up (mayRevokeAny): the caller
// may delete no token at all, which says nothing about which tokens exist, so TokenRefusal keeps it.
type refusedBeforeLookup struct{ error }

func (e refusedBeforeLookup) Unwrap() error { return e.error }

// TokenRefusal is what the API and MCP tell a caller that may not revoke the token called name: one
// answer, store.ErrNotFound, whether no token has that name, the token is one only the host revokes,
// or the caller lacks the right to this token. So a refusal says nothing about which tokens exist. A
// caller that may delete no token at all keeps that answer (ErrForbidden). Other errors, and nil, pass
// through.
func TokenRefusal(err error, name string) error {
	var before refusedBeforeLookup
	if errors.As(err, &before) {
		return err
	}
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrBadInput) || errors.Is(err, ErrForbidden) {
		return fmt.Errorf("%w: no token %q that you may revoke here; the tokens of machines and services are revoked on the host with beacon token revoke", store.ErrNotFound, name)
	}
	return err
}
