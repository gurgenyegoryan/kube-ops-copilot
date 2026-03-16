package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/clusterhealth"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/events"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/pdb"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/resources"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/workloads"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/engine"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/report"
	"github.com/spf13/cobra"
)

type suggestFlags struct {
	Kubeconfig              string
	Context                 string
	Timeout                 time.Duration
	EventsSince             time.Duration
	IncludeSystemNamespaces bool

	Provider    string
	Model       string
	BaseURL     string
	APIKey      string
	Temperature float64
}

func NewSuggestCmd() *cobra.Command {
	f := suggestFlags{}
	cmd := &cobra.Command{
		Use:   "suggest",
		Short: "Generate human-friendly suggestions (LLM-assisted, read-only)",
		Long:  "Uses an LLM to turn the deterministic diagnose report into operator-friendly narrative, triage order, and next-step commands. Does not apply changes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := llm.Provider(strings.ToLower(strings.TrimSpace(f.Provider)))
			if provider == "" {
				provider = llm.ProviderNone
			}
			client, err := llm.New(llm.Config{Provider: provider, Model: f.Model, BaseURL: f.BaseURL, APIKey: f.APIKey})
			if err != nil {
				return err
			}
			if client == nil {
				return errors.New("no LLM provider configured (use --llm-provider or set KUBE_OPS_COPILOT_*_API_KEY)")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), f.Timeout)
			defer cancel()

			kclient, err := kube.NewClient(kube.Config{Kubeconfig: f.Kubeconfig, Context: f.Context})
			if err != nil {
				return err
			}

			e := engine.Engine{Analyzers: []analyzer.Analyzer{
				&clusterhealth.Analyzer{Client: kclient},
				&events.Analyzer{Client: kclient, Since: f.EventsSince},
				&workloads.Analyzer{Client: kclient, IncludeSystemNamespaces: f.IncludeSystemNamespaces},
				&resources.Analyzer{Client: kclient, IncludeSystemNamespaces: f.IncludeSystemNamespaces},
				&pdb.Analyzer{Client: kclient, IncludeSystemNamespaces: f.IncludeSystemNamespaces},
			}}
			results, err := e.Run(ctx)
			if err != nil {
				return err
			}
			rep := report.Build(results)

			repJSON, err := json.Marshal(rep)
			if err != nil {
				return err
			}

			system := strings.TrimSpace(`You are Kube Ops Copilot, an approval-driven Kubernetes SRE assistant.
You must be evidence-first. Use the report as truth; do not invent cluster facts.
You must never suggest executing changes without operator approval.
When you propose kubectl commands, keep them read-only by default.
Output concise Markdown.`)

			user := fmt.Sprintf("Here is the deterministic diagnosis report as JSON:\n\n%s\n\nTask: Provide triage order, likely root causes, and next read-only verification commands. If you suggest remediation, describe it as an approval-gated plan (do not apply).", string(repJSON))
			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(resp.Text))
			return nil
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (defaults to in-cluster or ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context name")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 60*time.Second, "Overall suggest timeout")
	cmd.Flags().DurationVar(&f.EventsSince, "events-since", 60*time.Minute, "How far back to analyze Warning events")
	cmd.Flags().BoolVar(&f.IncludeSystemNamespaces, "include-system-namespaces", false, "Include kube-system and other system namespaces in workload/resource/policy checks")

	cmd.Flags().StringVar(&f.Provider, "llm-provider", "", "LLM provider: openai|anthropic|ollama")
	cmd.Flags().StringVar(&f.Model, "llm-model", "", "LLM model name (provider-specific)")
	cmd.Flags().StringVar(&f.BaseURL, "llm-base-url", "", "LLM base URL (optional; e.g., for self-hosted endpoints)")
	cmd.Flags().StringVar(&f.APIKey, "llm-api-key", "", "LLM API key (prefer env vars; avoid shell history)")
	cmd.Flags().Float64Var(&f.Temperature, "llm-temperature", 0, "LLM temperature (optional)")

	return cmd
}
