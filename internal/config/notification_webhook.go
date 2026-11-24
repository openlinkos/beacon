package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/openlinkos/beacon/internal/notify"
)

// NotificationWebhookChannel selects the team's signed notification webhook.
const NotificationWebhookChannel = "notification_webhook"

// NotificationWebhook names the endpoint and signing secret files for team notifications.
type NotificationWebhook struct {
	URLFile    string `yaml:"url_file"`
	SecretFile string `yaml:"secret_file"`
}

func (w NotificationWebhook) configured() bool {
	return w.URLFile != "" && w.SecretFile != ""
}

func (c *Config) readNotificationWebhookSecrets() error {
	w := c.NotificationWebhook
	if !w.configured() {
		return nil
	}
	endpoint, err := readWebhookSecretFile(w.URLFile)
	if err != nil {
		return fmt.Errorf("notification_webhook.url_file: %w", err)
	}
	if !notify.ValidWebhookURL(endpoint) {
		return errors.New("notification_webhook.url_file: must contain an absolute HTTP(S) URL without userinfo or a fragment")
	}
	secret, err := readWebhookSecretFile(w.SecretFile)
	if err != nil {
		return fmt.Errorf("notification_webhook.secret_file: %w", err)
	}
	c.Secrets.NotificationWebhookURL = endpoint
	c.Secrets.NotificationWebhookSecret = secret
	return nil
}

func readWebhookSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return "", errors.New("cannot read file")
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("file is empty")
	}
	return value, nil
}
