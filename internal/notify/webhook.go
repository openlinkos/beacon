package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/openlinkos/beacon/internal/version"
)

const webhookRequestLimit = 1 << 20

var webhookHTTP = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: noRedirect,
}

// Webhook delivers signed team notifications to a fixed HTTP(S) endpoint.
type Webhook struct {
	URL    string
	Secret string
}

// Send posts a raw JSON notification body. The receiver's HTTP status determines delivery success.
func (w Webhook) Send(ctx context.Context, body []byte) error {
	if len(body) > webhookRequestLimit {
		return errors.New("notification webhook: request body exceeds 1 MiB")
	}
	if w.Secret == "" {
		return errors.New("notification webhook: signing secret is missing")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil || !validWebhookURL(w.URL, req.URL) {
		return errors.New("notification webhook: invalid endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("X-Beacon-Signature", SignWebhook(w.Secret, body))
	resp, err := webhookHTTP.Do(req)
	if err != nil {
		// HTTP client errors can contain credentials in the URL or arbitrary response data.
		switch {
		case errors.Is(err, context.Canceled):
			return fmt.Errorf("notification webhook: %w", context.Canceled)
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("notification webhook: %w", context.DeadlineExceeded)
		default:
			return errors.New("notification webhook: request failed")
		}
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("notification webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}

// SignWebhook returns the Beacon signature of the exact bytes sent to a webhook.
func SignWebhook(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ValidWebhookURL reports whether an endpoint is an absolute HTTP(S) URL without userinfo or a fragment.
func ValidWebhookURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && validWebhookURL(raw, u)
}

func validWebhookURL(raw string, u *url.URL) bool {
	host := u.Hostname()
	if (u.Scheme != "http" && u.Scheme != "https") || host == "" || u.User != nil ||
		strings.Contains(raw, "#") || strings.ContainsFunc(raw, unicode.IsSpace) || strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return false
		}
	}
	if strings.HasPrefix(u.Host, "[") {
		addr, err := netip.ParseAddr(host)
		return err == nil && addr.Is6()
	}
	return !strings.ContainsAny(host, ":[]")
}
