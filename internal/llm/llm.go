package llm

import (
	"context"
)

type Provider string

const (
	ProviderNone     Provider = "none"
	ProviderOpenAI   Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderOllama   Provider = "ollama"
)

type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	System string
	User   string
	Model  string
	// Temperature is optional; providers may ignore.
	Temperature float64
}

type Response struct {
	Text string
}
