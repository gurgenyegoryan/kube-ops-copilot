package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/clusterhealth"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/clusterinfo"
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
	PlanOut                 string

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
				&clusterinfo.Analyzer{Client: kclient},
				&clusterhealth.Analyzer{Client: kclient, IncludeSystemNamespaces: f.IncludeSystemNamespaces, CollectPodLogHints: true, MaxPodLogHints: 3, PodLogTailLines: 200},
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
You must never apply changes.
When you propose kubectl commands, keep them read-only by default.

If asked to propose a remediation, propose ONLY one best remediation for the current evidence.
If the remediation is executable by this tool, include EXACTLY ONE JSON ExecutionPlan in a fenced code block.
Allowed operation types:
- rollout_restart_deployment
- scale_deployment

If no safe executable plan can be proposed from the evidence, output a JSON fenced block with: null
Output professional, concise Markdown.`)

			user := fmt.Sprintf("Here is the deterministic diagnosis report as JSON:\n\n%s\n\nTask:\n1) Provide triage order, likely root causes, and next read-only verification commands.\n2) Provide the single best production remediation (if any).\n\nOutput format requirements:\n- First, Markdown.\n- Then a fenced code block: ```json ...``` containing either an ExecutionPlan object or null.\n\nExecutionPlan JSON schema (must match exactly):\n{\n  \"apiVersion\": \"kube-ops-copilot/v1alpha1\",\n  \"kind\": \"ExecutionPlan\",\n  \"createdAt\": \"RFC3339\",\n  \"approvalId\": \"\",\n  \"operation\": {\n    \"type\": \"rollout_restart_deployment|scale_deployment\",\n    \"namespace\": \"...\",\n    \"name\": \"...\",\n    \"replicas\": 3,\n    \"reason\": \"...\"\n  },\n  \"verify\": { \"timeoutSeconds\": 180 }\n}\n\nNotes:\n- For rollout_restart_deployment, omit replicas.\n- For scale_deployment, replicas is required.\n- approvalId must be empty string.\n", string(repJSON))
			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				return err
			}

			if strings.TrimSpace(f.PlanOut) != "" {
				p, err := extractAndValidatePlan(resp.Text)
				if err != nil {
					return err
				}
				if p != nil {
					b, err := json.MarshalIndent(p, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(f.PlanOut, b, 0o600); err != nil {
						return err
					}
				}
				text := stripJSONPlanBlock(resp.Text)
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(text))
				if p == nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n(no executable plan emitted)\n")
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n(wrote executable plan to %s)\n", f.PlanOut)
				}
				return nil
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(resp.Text))
			return nil
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster; else $KUBECONFIG; else ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context override (default: current-context)")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 60*time.Second, "Overall suggest timeout")
	cmd.Flags().DurationVar(&f.EventsSince, "events-since", 60*time.Minute, "How far back to analyze Warning events")
	cmd.Flags().BoolVar(&f.IncludeSystemNamespaces, "include-system-namespaces", false, "Include kube-system and other system namespaces in workload/resource/policy checks")
	cmd.Flags().StringVar(&f.PlanOut, "plan-out", "", "Write an executable ExecutionPlan JSON (from LLM output) to this path")

	cmd.Flags().StringVar(&f.Provider, "llm-provider", "", "LLM provider: openai|anthropic|ollama")
	cmd.Flags().StringVar(&f.Model, "llm-model", "", "LLM model name (provider-specific)")
	cmd.Flags().StringVar(&f.BaseURL, "llm-base-url", "", "LLM base URL (optional; e.g., for self-hosted endpoints)")
	cmd.Flags().StringVar(&f.APIKey, "llm-api-key", "", "LLM API key (prefer env vars; avoid shell history)")
	cmd.Flags().Float64Var(&f.Temperature, "llm-temperature", 0, "LLM temperature (optional)")

	return cmd
}
