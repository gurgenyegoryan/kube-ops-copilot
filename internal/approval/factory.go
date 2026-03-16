package approval

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Provider Provider

	N8NWebhookURL  string
	N8NBearerToken string

	TelegramBotToken string
	TelegramChatID   string

	SlackSigningSecret string
	SlackWebhookURL    string
	SlackAppToken      string
	SlackChannelID     string
	SlackBaseURL       string

	StorePath     string
	ListenAddr    string
	PublicBaseURL string
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
		N8NWebhookURL:  get("KUBE_OPS_COPILOT_N8N_WEBHOOK_URL"),
		N8NBearerToken: get("KUBE_OPS_COPILOT_N8N_BEARER_TOKEN"),

		TelegramBotToken: get("KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN", "TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   get("KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID", "TELEGRAM_CHAT_ID"),

		SlackSigningSecret: get("KUBE_OPS_COPILOT_SLACK_SIGNING_SECRET"),
		SlackWebhookURL:    get("KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL", "SLACK_WEBHOOK_URL"),
		SlackAppToken:      get("KUBE_OPS_COPILOT_SLACK_BOT_TOKEN", "SLACK_BOT_TOKEN"),
		SlackChannelID:     get("KUBE_OPS_COPILOT_SLACK_CHANNEL_ID"),
		SlackBaseURL:       get("KUBE_OPS_COPILOT_SLACK_BASE_URL"),

		StorePath:     get("KUBE_OPS_COPILOT_APPROVAL_STORE"),
		ListenAddr:    get("KUBE_OPS_COPILOT_APPROVAL_LISTEN", "APPROVAL_LISTEN"),
		PublicBaseURL: get("KUBE_OPS_COPILOT_PUBLIC_BASE_URL"),
	}
}

func New(cfg Config) (Approver, error) {
	p := Provider(strings.ToLower(strings.TrimSpace(string(cfg.Provider))))
	switch p {
	case "", ProviderManual:
		return Manual{}, nil
	case ProviderN8N:
		return N8N{WebhookURL: cfg.N8NWebhookURL, BearerToken: cfg.N8NBearerToken}, nil
	case ProviderTelegram:
		return Telegram{Token: cfg.TelegramBotToken, ChatID: cfg.TelegramChatID}, nil
	case ProviderSlack:
		store := cfg.StorePath
		if store == "" {
			store = defaultStorePath()
		}
		return Slack{SigningSecret: cfg.SlackSigningSecret, WebhookURL: cfg.SlackWebhookURL, BotToken: cfg.SlackAppToken, ChannelID: cfg.SlackChannelID, BaseURL: cfg.SlackBaseURL, StorePath: store, PublicBaseURL: cfg.PublicBaseURL}, nil
	default:
		return nil, fmt.Errorf("unsupported approval provider: %q", p)
	}
}
