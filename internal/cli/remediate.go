package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/clusterhealth"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/events"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/pdb"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/resources"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/workloads"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/approval"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/engine"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/notify"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/report"
	"github.com/spf13/cobra"
)

type remediateFlags struct {
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

	ApprovalProvider string
	ApprovalID       string
	WaitApproval     bool
	ApprovalTimeout  time.Duration

	PlanOut string
	Apply   bool
	Notify  bool
}

func NewRemediateCmd() *cobra.Command {
	f := remediateFlags{}
	cmd := &cobra.Command{
		Use:   "remediate",
		Short: "One-command workflow: diagnose → propose 1 plan → request approval → execute",
		Long:  "Runs deterministic diagnosis, asks an LLM for exactly one best executable plan, requests approval (e.g. Telegram), optionally waits, then applies the plan if approved.",
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

Goal: choose the single best production remediation for the current evidence.
Constraints:
- You must never apply changes.
- Prefer the smallest safe change with clear verification.
- Only propose a plan if evidence supports it.
- Allowed executable operation types:
	- rollout_restart_deployment
	- scale_deployment

Output format requirements:
- First, concise Markdown explaining the one chosen remediation and why it's the best.
- Then EXACTLY ONE fenced code block labeled json containing either an ExecutionPlan object or null.
`)

			user := fmt.Sprintf("Here is the deterministic diagnosis report as JSON:\n\n%s\n\nReturn the best single remediation (or null plan if none safe). ExecutionPlan schema:\n{\n  \"apiVersion\": \"kube-ops-copilot/v1alpha1\",\n  \"kind\": \"ExecutionPlan\",\n  \"createdAt\": \"RFC3339\",\n  \"approvalId\": \"\",\n  \"operation\": {\n    \"type\": \"rollout_restart_deployment|scale_deployment\",\n    \"namespace\": \"...\",\n    \"name\": \"...\",\n    \"replicas\": 3,\n    \"reason\": \"...\"\n  },\n  \"verify\": { \"timeoutSeconds\": 180 }\n}\nNotes: approvalId must be empty string; omit replicas for rollout_restart_deployment; replicas required for scale_deployment.", string(repJSON))

			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				return err
			}

			plan, err := extractAndValidatePlan(resp.Text)
			if err != nil {
				return err
			}

			md := stripJSONPlanBlock(resp.Text)
			md = strings.TrimSpace(md)
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), md)

			if plan == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\n(no executable plan proposed for this snapshot)")
				return nil
			}

			plan.CreatedAt = time.Now().UTC()

			approvalProvider := strings.ToLower(strings.TrimSpace(f.ApprovalProvider))
			if approvalProvider == "" {
				approvalProvider = string(approval.ProviderManual)
			}
			approvalID := strings.TrimSpace(f.ApprovalID)
			if approvalID == "" {
				approvalID = uuid.NewString()
			}
			plan.ApprovalID = approvalID

			planPath := strings.TrimSpace(f.PlanOut)
			if planPath == "" {
				planPath = filepath.Join(os.TempDir(), "kube-ops-copilot-plan-"+approvalID+".json")
			}
			b, err := json.MarshalIndent(plan, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(planPath, b, 0o600); err != nil {
				return err
			}

			cfg := approval.FromEnv()
			cfg.Provider = approval.Provider(approvalProvider)
			ap, err := approval.New(cfg)
			if err != nil {
				return err
			}

			summary := fmt.Sprintf("%s %s/%s", plan.Operation.Type, plan.Operation.Namespace, plan.Operation.Name)
			details := md
			if strings.TrimSpace(plan.Operation.Reason) != "" && !strings.Contains(details, plan.Operation.Reason) {
				details = strings.TrimSpace(details + "\n\nPlan reason: " + plan.Operation.Reason)
			}
			// Keep approval messages short; full rationale is already printed to stdout above.
			approvalDetails := strings.TrimSpace(details)
			if len(approvalDetails) > 900 {
				approvalDetails = strings.TrimSpace(approvalDetails[:900]) + "…"
			}
			_, err = ap.Request(ctx, approval.Request{
				ApprovalID: approvalID,
				Summary:    summary,
				PlanPath:   planPath,
				Operation:  string(plan.Operation.Type),
				Target:     fmt.Sprintf("%s/%s", plan.Operation.Namespace, plan.Operation.Name),
				Details:    approvalDetails,
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\napproval requested: provider=%s approval-id=%s\n", approvalProvider, approvalID)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "plan written: %s\n", planPath)

			if approvalProvider != string(approval.ProviderManual) {
				if f.WaitApproval {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "waiting for approval decision (timeout=%s)…\n", f.ApprovalTimeout)
				}
				if err := ensureApproved(cmd.Context(), approvalProvider, approvalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
					return err
				}
			}

			if !f.Apply {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nnot applying changes (pass --apply to execute after approval)")
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "next: kube-ops-copilot execute --plan %s --approval-provider %s --approval-id %s --approve --dry-run=false\n", planPath, approvalProvider, approvalID)
				return nil
			}

			// Extra safety: manual provider requires explicit --approve flag via execute; here we still keep apply gated by provider approval.
			ex := exec.Executor{Client: kclient}
			res, err := ex.Apply(ctx, *plan)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "applied: op=%s target=%s verified=%t duration=%s\n", res.Operation, res.Target, res.Verified, res.EndedAt.Sub(res.StartedAt))

			if f.Notify {
				n := notify.NewFromConfig(notify.FromEnv())
				if n == nil {
					return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
				}
				_ = n.Send(ctx, notify.Message{Title: "kube-ops-copilot remediate (applied)", Body: fmt.Sprintf("approvalId=%s op=%s target=%s verified=%t", approvalID, res.Operation, res.Target, res.Verified)})
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (defaults to in-cluster or ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context name")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 2*time.Minute, "Overall remediate timeout")
	cmd.Flags().DurationVar(&f.EventsSince, "events-since", 60*time.Minute, "How far back to analyze Warning events")
	cmd.Flags().BoolVar(&f.IncludeSystemNamespaces, "include-system-namespaces", false, "Include kube-system and other system namespaces in workload/resource/policy checks")

	cmd.Flags().StringVar(&f.Provider, "llm-provider", "", "LLM provider: openai|anthropic|ollama")
	cmd.Flags().StringVar(&f.Model, "llm-model", "", "LLM model name (provider-specific)")
	cmd.Flags().StringVar(&f.BaseURL, "llm-base-url", "", "LLM base URL (optional; e.g., for self-hosted endpoints)")
	cmd.Flags().StringVar(&f.APIKey, "llm-api-key", "", "LLM API key (prefer env vars; avoid shell history)")
	cmd.Flags().Float64Var(&f.Temperature, "llm-temperature", 0, "LLM temperature (optional)")

	cmd.Flags().StringVar(&f.ApprovalProvider, "approval-provider", "telegram", "Approval provider: manual|n8n|telegram|slack")
	cmd.Flags().StringVar(&f.ApprovalID, "approval-id", "", "Optional approval id (generated if empty)")
	cmd.Flags().BoolVar(&f.WaitApproval, "wait-approval", true, "Wait/poll for approval decision")
	cmd.Flags().DurationVar(&f.ApprovalTimeout, "approval-timeout", 10*time.Minute, "How long to wait for approval when --wait-approval is set")

	cmd.Flags().StringVar(&f.PlanOut, "plan-out", "", "Where to write the generated plan JSON (defaults to a temp file)")
	cmd.Flags().BoolVar(&f.Apply, "apply", false, "Apply the plan after approval")
	cmd.Flags().BoolVar(&f.Notify, "notify", false, "Send a notification (Slack/Telegram via env vars)")

	return cmd
}
