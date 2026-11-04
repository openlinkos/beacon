//go:build race

package sshgate

import "time"

// testWait is how long the tests give something asynchronous (a handshake through the channel, the
// chat hearing of it). The race detector slows everything several times over, so the budget grows
// with it; without it, five seconds have been too little on a busy runner.
const testWait = 30 * time.Second
