package secrets

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // G505: RFC 6238's codes are HMAC-SHA1; the seeds are made for it
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"strings"
	"time"
)

// One-time passcodes, RFC 6238: a time-based code an otp field's seed gives (the seed is a base32
// string, as every authenticator exports it). The vault hands out the current code and the seconds
// it has left, never the seed, unless a later package's settings say otherwise.

// TOTPPeriod is the code's lifetime; every authenticator uses thirty seconds.
const TOTPPeriod = 30 * time.Second

// TOTPDigits is the code's length; six, as every authenticator shows.
const TOTPDigits = 6

// TOTP computes the code for a base32 seed at t, with the digits given (six when 0). The seed's
// padding is tolerated, as the exports write it.
func TOTP(seed string, t time.Time, digits int) (string, error) {
	if digits <= 0 {
		digits = TOTPDigits
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(strings.ReplaceAll(seed, " ", ""), "=")))
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("%w: the seed is not base32", ErrInvalid)
	}
	counter := uint64(t.Unix()) / uint64(TOTPPeriod.Seconds())
	return hotp(key, counter, digits, sha1.New), nil
}

// TOTPNow is the code at t and the time it has left.
func TOTPNow(seed string, t time.Time) (string, time.Duration, error) {
	code, err := TOTP(seed, t, TOTPDigits)
	if err != nil {
		return "", 0, err
	}
	left := TOTPPeriod - time.Duration(t.Unix()%int64(TOTPPeriod.Seconds()))*time.Second
	return code, left, nil
}

// hotp is RFC 4226's algorithm; SHA-1 is what the seeds everywhere are made for, and SHA-256 is
// kept for the seeds that say so (steam and the rest stay out).
func hotp(key []byte, counter uint64, digits int, h func() hash.Hash) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(h, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff) % uint32(pow10(digits))
	return fmt.Sprintf("%0*d", digits, code)
}

func pow10(n int) int {
	out := 1
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}
