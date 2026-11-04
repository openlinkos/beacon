//go:build !race

package sshgate

import "time"

// testWait is how long the tests give something asynchronous (a handshake through the channel, the
// chat hearing of it); with the race detector, see wait_race_test.go.
const testWait = 5 * time.Second
