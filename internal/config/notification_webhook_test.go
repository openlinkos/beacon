package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotificationWebhookRouting(t *testing.T) {
	d := t.TempDir()
	const endpoint = "http://notifications:8080/receiver?token=fixture"
	const key = "webhook-signing-fixture"
	yml := notificationTestConfig(t, d) + fmt.Sprintf("notification_webhook: {url_file: %q, secret_file: %q}\n",
		secret(t, d, "endpoint", endpoint), secret(t, d, "signing", key))
	app := fmt.Sprintf("  app_id_file: %q\n  app_secret_file: %q\n  chat_id: oc_1\n  on_call: [{open_id: ou_a, name: A}]\n",
		secret(t, d, "app-id", "cli_x"), secret(t, d, "app-secret", "feishu-app-fixture"))
	bot := fmt.Sprintf("  webhook_url_file: %q\n", secret(t, d, "group-bot", "https://open.feishu.cn/example"))
	push := fmt.Sprintf("pushover: {token_file: %q, user_key_file: %q}\n",
		secret(t, d, "push-token", "token"), secret(t, d, "push-user", "user"))

	tests := []struct {
		name, config, critical, warning, info string
		buzz, push                            time.Duration
	}{
		{"only channel", "", NotificationWebhookChannel, NotificationWebhookChannel, NotificationWebhookChannel, 0, 0},
		{"with Pushover", push, NotificationWebhookChannel, NotificationWebhookChannel, NotificationWebhookChannel, 0, 15 * time.Minute},
		{"app takes priority", "feishu:\n" + app, "app", "app", "app", 10 * time.Minute, 0},
		{"bot takes priority", "feishu:\n" + bot, "webhook", "webhook", "webhook", 0, 0},
		{"app and bot keep priority", "feishu:\n" + app + bot, "app", "app", "webhook", 10 * time.Minute, 0},
		{
			"explicit override", "feishu:\n" + app +
				"escalation: {critical: {channel: app}, warning: {channel: notification_webhook}, info: {channel: none}}\n",
			"app", NotificationWebhookChannel, "none", 0, 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(yml + tc.config))
			if err != nil {
				t.Fatal(err)
			}
			if c.Secrets.NotificationWebhookURL != endpoint || c.Secrets.NotificationWebhookSecret != key {
				t.Fatal("webhook secrets were not loaded")
			}
			wantCritical := Policy{Channel: tc.critical, Buzz: Duration(tc.buzz), Pushover: Duration(tc.push)}
			if c.Policy("critical") != wantCritical || c.Policy("warning").Channel != tc.warning || c.Policy("info").Channel != tc.info {
				t.Errorf("wrong escalation policy: %+v", c.Escalation)
			}
		})
	}
}

func TestNotificationWebhookInvalidConfiguration(t *testing.T) {
	d := t.TempDir()
	const marker = "do-not-print-this-marker"
	key := secret(t, d, "signing", marker)
	endpoint := secret(t, d, "endpoint", "https://notify.example.com/receiver?token="+marker)
	empty := secret(t, d, "empty", "  ")
	missing := filepath.ToSlash(filepath.Join(d, marker))
	base := notificationTestConfig(t, d)
	tests := []struct {
		name, config, field string
	}{
		{"url only", fmt.Sprintf("notification_webhook: {url_file: %q}", endpoint), "go together"},
		{"secret only", fmt.Sprintf("notification_webhook: {secret_file: %q}", key), "go together"},
		{"unconfigured escalation", "escalation: {warning: {channel: notification_webhook}}", "escalation.warning"},
		{"missing URL file", fmt.Sprintf("notification_webhook: {url_file: %q, secret_file: %q}", missing, key), "notification_webhook.url_file"},
		{"empty URL file", fmt.Sprintf("notification_webhook: {url_file: %q, secret_file: %q}", empty, key), "notification_webhook.url_file"},
		{"invalid URL content", fmt.Sprintf("notification_webhook: {url_file: %q, secret_file: %q}", key, key), "notification_webhook.url_file"},
		{"missing secret file", fmt.Sprintf("notification_webhook: {url_file: %q, secret_file: %q}", endpoint, missing), "notification_webhook.secret_file"},
		{"empty secret file", fmt.Sprintf("notification_webhook: {url_file: %q, secret_file: %q}", endpoint, empty), "notification_webhook.secret_file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(base + tc.config))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("expected a configuration error mentioning %s, got %v", tc.field, err)
			}
			if strings.Contains(err.Error(), marker) {
				t.Error("configuration error contains a secret value")
			}
		})
	}
}

func notificationTestConfig(t *testing.T, dir string) string {
	t.Helper()
	return fmt.Sprintf("rules: {token_file: %q}\nvictoriametrics: {url: http://vm:8428}\n",
		secret(t, dir, "rules", "0123456789abcdef0123456789"))
}
