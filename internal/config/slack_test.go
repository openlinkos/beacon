package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestSlackConfigAndDefaultRoute(t *testing.T) {
	d := t.TempDir()
	base := notificationTestConfig(t, d)
	const endpoint = "https://hooks.slack.com/services/T_DEMO/B_DEMO/another-secret"
	slack := fmt.Sprintf("slack: {webhook_url_file: %q}\n", secret(t, d, "slack-url", endpoint))
	webhook := fmt.Sprintf("notification_webhook: {url_file: %q, secret_file: %q}\n",
		secret(t, d, "webhook-url", "https://relay.example.com/alerts"), secret(t, d, "webhook-sign", "key"))
	for _, tc := range []struct{ name, config, critical, warning string }{
		{"only Slack", slack, SlackChannel, SlackChannel},
		{"preserve existing route", slack + webhook, NotificationWebhookChannel, NotificationWebhookChannel},
		{"route selected by severity", slack + webhook + "escalation: {critical: {channel: slack}, warning: {channel: notification_webhook}}\n", SlackChannel, NotificationWebhookChannel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(base + tc.config))
			if err != nil {
				t.Fatal(err)
			}
			if c.Secrets.SlackWebhookURL != endpoint || c.Policy("critical").Channel != tc.critical || c.Policy("warning").Channel != tc.warning {
				t.Fatal("the Slack endpoint or routes were not loaded")
			}
		})
	}
	badEndpoint := secret(t, d, "invalid-slack-url", "https://unrelated.example.com/another-secret")
	for _, wrong := range []string{
		"escalation: {critical: {channel: slack}}\n",
		fmt.Sprintf("slack: {webhook_url_file: %q}\n", badEndpoint),
	} {
		_, err := Parse([]byte(base + wrong))
		if err == nil || !strings.Contains(err.Error(), "slack.webhook_url_file") || strings.Contains(err.Error(), "another-secret") {
			t.Fatalf("wrong Slack configuration result: %v", err)
		}
	}
}
