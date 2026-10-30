package ctl

import (
	"errors"
	"net"
)

// Listen is not supported on Windows: there are no file modes or peer credentials to restrict the
// socket with. The service runs on Linux; on Windows the commands that need it are unavailable.
func Listen(string) (net.Listener, error) { return nil, errors.ErrUnsupported }
