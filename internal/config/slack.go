package config

import (
	"errors"
	"fmt"

	"github.com/openlinkos/beacon/internal/notify"
)

// SlackChannel routes alerts to Slack's incoming webhook.
const SlackChannel = "slack"

// Slack names the file holding an incoming webhook URL, which is itself a credential.
type Slack struct {
	WebhookURLFile string `yaml:"webhook_url_file"`
}

func (c *Config) readSlackSecret() error {
	if c.Slack.WebhookURLFile == "" {
		return nil
	}
	endpoint, err := readWebhookSecretFile(c.Slack.WebhookURLFile)
	if err != nil {
		return fmt.Errorf("slack.webhook_url_file: %w", err)
	}
	if !notify.ValidSlackWebhookURL(endpoint) {
		return errors.New("slack.webhook_url_file: must contain a Slack or GovSlack incoming webhook URL")
	}
	c.Secrets.SlackWebhookURL = endpoint
	return nil
}
