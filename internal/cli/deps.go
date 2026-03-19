package cli

import (
	"context"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/notify"
)

var cliNewKubeClient = kube.NewClient

var cliNewNotifierFromEnv = func() notify.Notifier {
	return notify.NewFromConfig(notify.FromEnv())
}

var cliNewLLMClient = llm.New

var cliDefaultAnalyzers = func(ctx context.Context, client *kube.Client, includeSystemNamespaces bool, eventsSince time.Duration) []analyzer.Analyzer {
	return defaultAnalyzers(ctx, client, includeSystemNamespaces, eventsSince)
}

var cliBuildTerraformRepoInventory = buildTerraformRepoInventory
