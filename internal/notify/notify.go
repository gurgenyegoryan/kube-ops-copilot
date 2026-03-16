package notify

import (
	"context"
	"errors"
	"os"
	"strings"
)

type Notifier interface {
	Send(ctx context.Context, msg Message) error
}

type Message struct {
	Title string
	Body  string
}

type Multi struct {
	Notifiers []Notifier
}

func (m Multi) Send(ctx context.Context, msg Message) error {
	var errs []string
	for _, n := range m.Notifiers {
		if n == nil {
			continue
		}
		if err := n.Send(ctx, msg); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

type Config struct {
	SlackWebhookURL  string
	TelegramBotToken string
	TelegramChatID   string
	DefaultTitle     string
}

func FromEnv() Config {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	return Config{
		SlackWebhookURL:  get("KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL", "SLACK_WEBHOOK_URL"),
		TelegramBotToken: get("KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   get("KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID", "TELEGRAM_CHAT_ID"),
		DefaultTitle:     get("KUBE_OPS_COPILOT_NOTIFY_TITLE"),
	}
}

func NewFromConfig(cfg Config) Notifier {
	var notifiers []Notifier
	if strings.TrimSpace(cfg.SlackWebhookURL) != "" {
		notifiers = append(notifiers, SlackWebhook{URL: cfg.SlackWebhookURL})
	}
	if strings.TrimSpace(cfg.TelegramBotToken) != "" && strings.TrimSpace(cfg.TelegramChatID) != "" {
		notifiers = append(notifiers, TelegramBot{Token: cfg.TelegramBotToken, ChatID: cfg.TelegramChatID})
	}
	if len(notifiers) == 0 {
		return nil
	}
	return Multi{Notifiers: notifiers}
}
