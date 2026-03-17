package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/engine"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/notify"
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
	Notify                  bool

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

			e := engine.Engine{Analyzers: defaultAnalyzers(ctx, kclient.Kubernetes, f.IncludeSystemNamespaces, f.EventsSince)}
			results, err := e.Run(ctx)
			if err != nil {
				return err
			}
			if wr := warningResult(kclient.WarningCollector.Snapshot()); len(wr.Findings) > 0 || len(wr.Evidence) > 0 || len(wr.HiddenRisks) > 0 || len(wr.Recommended.ShortTerm) > 0 {
				results = append(results, wr)
			}
			rep := report.Build(results)

			repJSON, err := json.Marshal(rep)
			if err != nil {
				return err
			}

			system := strings.TrimSpace(`You are Kube Ops Copilot, an approval-driven Kubernetes SRE investigator.
You must be evidence-first. Use the report as truth; do not invent cluster facts.
You must reason from discovered cluster capabilities, gaps, and workload signals instead of assuming Prometheus, Loki, tracing, service mesh, or autoscaling are present.
If a capability is not explicitly observed, say "not confirmed" instead of assuming it exists.
You must never apply changes.
When you propose kubectl commands, keep them read-only by default.

Prioritize production-grade analysis:
- identify what the cluster actually has
- identify what is missing or not confirmed
- distinguish platform blind spots from active incidents
- recommend the smallest high-leverage improvement first

If asked to propose a remediation, propose ONLY one best remediation for the current evidence.
Choose an executable remediation only if the evidence clearly supports it and it fits the allowed plan types.
Allowed operation types:
- rollout_restart_deployment
- scale_deployment

If no safe executable plan can be proposed from the evidence, output a JSON fenced block with: null
Output professional, concise Markdown.`)

			user := fmt.Sprintf("Here is the deterministic diagnosis report as JSON:\n\n%s\n\nTask:\n1) Act as a production-readiness reviewer for this specific cluster snapshot.\n2) Explain what platform capabilities are explicitly observed, what is not confirmed, and which gaps most limit reliable production suggestions.\n3) Provide triage order, likely root causes, and next read-only verification commands.\n4) Provide the single best production remediation or production-readiness improvement for the current evidence.\n5) Do not recommend Prometheus/Loki/HPA/etc. as if they already exist unless the report explicitly shows them.\n\nOutput format requirements:\n- First, Markdown.\n- Then a fenced code block: ```json ...``` containing either an ExecutionPlan object or null.\n\nExecutionPlan JSON schema (must match exactly):\n{\n  \"apiVersion\": \"kube-ops-copilot/v1alpha1\",\n  \"kind\": \"ExecutionPlan\",\n  \"createdAt\": \"RFC3339\",\n  \"approvalId\": \"\",\n  \"operation\": {\n    \"type\": \"rollout_restart_deployment|scale_deployment\",\n    \"namespace\": \"...\",\n    \"name\": \"...\",\n    \"replicas\": 3,\n    \"reason\": \"...\"\n  },\n  \"verify\": { \"timeoutSeconds\": 180 }\n}\n\nNotes:\n- For rollout_restart_deployment, omit replicas.\n- For scale_deployment, replicas is required.\n- approvalId must be empty string.\n- If the best recommendation is not one of the allowed plan types, emit null in the JSON block and keep the recommendation in Markdown only.\n", string(repJSON))
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
				text = strings.TrimSpace(text)
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), text)
				if p == nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n(no executable plan emitted)\n")
				} else {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\n(wrote executable plan to %s)\n", f.PlanOut)
				}

				if f.Notify {
					n := notify.NewFromConfig(notify.FromEnv())
					if n == nil {
						return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_N8N_WEBHOOK_URL and/or KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
					}
					body := truncateForTelegram(text, 3500)
					if p != nil {
						body = strings.TrimSpace(body + "\n\nplan: " + strings.TrimSpace(f.PlanOut))
					}
					_ = n.Send(ctx, notify.Message{Title: "kube-ops-copilot suggest", Body: body})
				}
				return nil
			}

			out := strings.TrimSpace(resp.Text)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), out)
			if f.Notify {
				n := notify.NewFromConfig(notify.FromEnv())
				if n == nil {
					return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_N8N_WEBHOOK_URL and/or KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
				}
				text := strings.TrimSpace(stripJSONPlanBlock(out))
				_ = n.Send(ctx, notify.Message{Title: "kube-ops-copilot suggest", Body: truncateForTelegram(text, 3500)})
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster; else $KUBECONFIG; else ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context override (default: current-context)")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 60*time.Second, "Overall suggest timeout")
	cmd.Flags().DurationVar(&f.EventsSince, "events-since", 60*time.Minute, "How far back to analyze Warning events")
	cmd.Flags().BoolVar(&f.IncludeSystemNamespaces, "include-system-namespaces", false, "Include kube-system and other system namespaces in workload/resource/policy checks")
	cmd.Flags().StringVar(&f.PlanOut, "plan-out", "", "Write an executable ExecutionPlan JSON (from LLM output) to this path")
	cmd.Flags().BoolVar(&f.Notify, "notify", false, "Send a notification (via n8n/Slack/Telegram env vars)")

	cmd.Flags().StringVar(&f.Provider, "llm-provider", "", "LLM provider: openai|anthropic|ollama")
	cmd.Flags().StringVar(&f.Model, "llm-model", "", "LLM model name (provider-specific)")
	cmd.Flags().StringVar(&f.BaseURL, "llm-base-url", "", "LLM base URL (optional; e.g., for self-hosted endpoints)")
	cmd.Flags().StringVar(&f.APIKey, "llm-api-key", "", "LLM API key (prefer env vars; avoid shell history)")
	cmd.Flags().Float64Var(&f.Temperature, "llm-temperature", 0, "LLM temperature (optional)")

	return cmd
}

func truncateForTelegram(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	// Keep it simple and safe for multi-byte UTF-8: truncate by runes.
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}
