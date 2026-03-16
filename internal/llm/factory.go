package llm

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Provider Provider
	Model    string
	BaseURL  string
	APIKey   string
}

func New(cfg Config) (Client, error) {
	switch cfg.Provider {
	case ProviderNone, "":
		return nil, nil
	case ProviderOpenAI:
		key := cfg.APIKey
		if key == "" {
			key = os.Getenv("KUBE_OPS_COPILOT_OPENAI_API_KEY")
			if key == "" {
				key = os.Getenv("OPENAI_API_KEY")
			}
		}
		return &OpenAIClient{APIKey: key, BaseURL: cfg.BaseURL}, nil
	case ProviderAnthropic:
		key := cfg.APIKey
		if key == "" {
			key = os.Getenv("KUBE_OPS_COPILOT_ANTHROPIC_API_KEY")
			if key == "" {
				key = os.Getenv("ANTHROPIC_API_KEY")
			}
		}
		return &AnthropicClient{APIKey: key, BaseURL: cfg.BaseURL}, nil
	case ProviderOllama:
		return &OllamaClient{BaseURL: cfg.BaseURL}, nil
	default:
		return nil, fmt.Errorf("unsupported llm provider: %q", strings.ToLower(string(cfg.Provider)))
	}
}
