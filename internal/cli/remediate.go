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
	ApprovalOnNull   bool

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
			progress := newLiveProgress(cmd.OutOrStdout(), "remediate")
			defer progress.Close()
			provider := llm.Provider(strings.ToLower(strings.TrimSpace(f.Provider)))
			if provider == "" {
				provider = llm.ProviderNone
			}
			progress.Updatef("initializing LLM client")
			client, err := cliNewLLMClient(llm.Config{Provider: provider, Model: f.Model, BaseURL: f.BaseURL, APIKey: f.APIKey})
			if err != nil {
				progress.Failf("initializing LLM client")
				return err
			}
			if client == nil {
				progress.Failf("initializing LLM client")
				return errors.New("no LLM provider configured (use --llm-provider or set KUBE_OPS_COPILOT_*_API_KEY)")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), f.Timeout)
			defer cancel()

			progress.Updatef("connecting to cluster")
			kclient, err := cliNewKubeClient(kube.Config{Kubeconfig: f.Kubeconfig, Context: f.Context})
			if err != nil {
				progress.Failf("connecting to cluster")
				return err
			}

			progress.Updatef("running analyzers")
			e := engine.Engine{Analyzers: cliDefaultAnalyzers(ctx, kclient, f.IncludeSystemNamespaces, f.EventsSince), Progress: progress.Eventf}
			results, err := e.Run(ctx)
			if err != nil {
				progress.Failf("running analyzers")
				return err
			}
			if wr := warningResult(kclient.WarningCollector.Snapshot()); len(wr.Findings) > 0 || len(wr.Evidence) > 0 || len(wr.HiddenRisks) > 0 || len(wr.Recommended.ShortTerm) > 0 {
				results = append(results, wr)
			}
			progress.Updatef("building diagnosis report")
			rep := report.Build(results)
			repJSON, err := json.Marshal(rep)
			if err != nil {
				return err
			}
			progress.Eventf("prepared diagnosis payload bytes=%d", len(repJSON))

			system := strings.TrimSpace(`You are Kube Ops Copilot, an approval-driven Kubernetes SRE assistant.
You must be evidence-first. Use the report as truth; do not invent cluster facts.

Operating principle: investigation-first, then the smallest safe change.

Constraints:
- You must never apply changes.
- Prefer read-only verification steps before recommending a change.
- If evidence is insufficient to justify an executable remediation, output a null plan.
- Treat restarts as a last resort: do NOT propose a restart/rollout restart unless you can explain why it is likely to reduce user impact and does not just hide an underlying issue.

Allowed executable operation types (and ONLY these):
- rollout_restart_deployment
- scale_deployment

Output format requirements:
- First, professional concise Markdown with: triage order, top hypotheses, and next read-only verification commands.
- Then EXACTLY ONE fenced code block labeled json containing either an ExecutionPlan object or null.
`)

			user := fmt.Sprintf("Here is the deterministic diagnosis report as JSON:\n\n%s\n\nTask:\n1) Provide triage order, likely root causes, and the next read-only verification commands.\n2) If telemetry is explicitly confirmed in the report, you may suggest backend-specific verification queries. If not, stay backend-agnostic.\n3) Provide the single best production remediation (if any).\n\nRules for remediation choice:\n- Only emit an executable plan if evidence supports it in THIS snapshot.\n- If root cause is unclear, emit null and focus on what to verify next.\n- If you propose a restart, justify it with evidence and include post-change verification.\n\nOutput format requirements:\n- First, Markdown.\n- Then a fenced code block: ```json ...``` containing either an ExecutionPlan object or null.\n\nExecutionPlan JSON schema (must match exactly):\n{\n  \"apiVersion\": \"kube-ops-copilot/v1alpha1\",\n  \"kind\": \"ExecutionPlan\",\n  \"createdAt\": \"RFC3339\",\n  \"approvalId\": \"\",\n  \"operation\": {\n    \"type\": \"rollout_restart_deployment|scale_deployment\",\n    \"namespace\": \"...\",\n    \"name\": \"...\",\n    \"replicas\": 3,\n    \"reason\": \"...\"\n  },\n  \"verify\": { \"timeoutSeconds\": 180 }\n}\n\nNotes:\n- For rollout_restart_deployment, omit replicas.\n- For scale_deployment, replicas is required.\n- approvalId must be empty string.\n", string(repJSON))

			progress.Updatef("asking LLM for remediation recommendation")
			progress.Eventf("submitting LLM request provider=%s model=%s", provider, strings.TrimSpace(f.Model))
			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				progress.Failf("asking LLM for remediation recommendation")
				return withLLMTimeoutHint(err, "remediate", f.Timeout)
			}

			plan, err := extractAndValidatePlan(resp.Text)
			if err != nil {
				progress.Failf("validating execution plan")
				return err
			}

			md := stripJSONPlanBlock(resp.Text)
			md = strings.TrimSpace(md)
			if md != "" {
				progress.Printf("%s", md)
			}

			if plan == nil {
				progress.Printf("(no executable plan proposed for this snapshot)")
				if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot remediate", truncateForTelegram(md, 3500)); err != nil {
					progress.Failf("sending notification")
					return err
				}
				if !f.ApprovalOnNull {
					progress.Donef("advisory-only result; no executable remediation plan proposed")
					return nil
				}

				approvalProvider := strings.ToLower(strings.TrimSpace(f.ApprovalProvider))
				if approvalProvider == "" {
					approvalProvider = string(approval.ProviderManual)
				}
				approvalID := strings.TrimSpace(f.ApprovalID)
				if approvalID == "" {
					approvalID = uuid.NewString()
				}

				cfg := approval.FromEnv()
				cfg.Provider = approval.Provider(approvalProvider)
				ap, err := approval.New(cfg)
				if err != nil {
					return err
				}

				target := strings.TrimSpace(f.Context)
				if target == "" {
					target = "current-context"
				}
				details := strings.TrimSpace(md)
				if len(details) > 900 {
					details = strings.TrimSpace(details[:900]) + "…"
				}

				progress.Updatef("requesting approval for review-only report")
				_, err = ap.Request(ctx, approval.Request{
					ApprovalID: approvalID,
					Summary:    "review triage (no executable plan)",
					Operation:  "review_report",
					Target:     target,
					Details:    details,
				})
				if err != nil {
					progress.Failf("requesting approval for review-only report")
					return err
				}

				progress.Printf("approval requested (no plan): provider=%s approval-id=%s", approvalProvider, approvalID)
				if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot remediate (approval requested)", truncateForTelegram(fmt.Sprintf("approvalId=%s provider=%s operation=review_report target=%s\n\n%s", approvalID, approvalProvider, target, details), 3500)); err != nil {
					progress.Failf("sending notification")
					return err
				}

				if approvalProvider != string(approval.ProviderManual) {
					if f.WaitApproval {
						progress.Updatef("waiting for approval decision (%s)", f.ApprovalTimeout)
					}
					if err := ensureApproved(cmd.Context(), approvalProvider, approvalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
						progress.Failf("approval was not granted")
						return err
					}
				}

				progress.Donef("approval recorded; nothing to apply because plan is null")
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
			progress.Updatef("writing plan file")
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
			progress.Updatef("requesting approval")
			_, err = ap.Request(ctx, approval.Request{
				ApprovalID: approvalID,
				Summary:    summary,
				PlanPath:   planPath,
				Operation:  string(plan.Operation.Type),
				Target:     fmt.Sprintf("%s/%s", plan.Operation.Namespace, plan.Operation.Name),
				Details:    approvalDetails,
			})
			if err != nil {
				progress.Failf("requesting approval")
				return err
			}

			progress.Printf("approval requested: provider=%s approval-id=%s", approvalProvider, approvalID)
			progress.Printf("plan written: %s", planPath)
			if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot remediate (approval requested)", truncateForTelegram(fmt.Sprintf("approvalId=%s provider=%s op=%s target=%s/%s\nplan=%s", approvalID, approvalProvider, plan.Operation.Type, plan.Operation.Namespace, plan.Operation.Name, planPath), 3500)); err != nil {
				progress.Failf("sending notification")
				return err
			}

			if approvalProvider != string(approval.ProviderManual) {
				if f.WaitApproval {
					progress.Updatef("waiting for approval decision (%s)", f.ApprovalTimeout)
				}
				if err := ensureApproved(cmd.Context(), approvalProvider, approvalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
					progress.Failf("approval was not granted")
					return err
				}
			}

			if !f.Apply {
				progress.Printf("next: kube-ops-copilot execute --plan %s --approval-provider %s --approval-id %s --approve --dry-run=false", planPath, approvalProvider, approvalID)
				progress.Donef("approval recorded; changes not applied because --apply was not requested")
				return nil
			}

			// Extra safety: manual provider requires explicit --approve flag via execute; here we still keep apply gated by provider approval.
			progress.Updatef("executing approved remediation")
			ex := exec.Executor{Client: kclient.Kubernetes, Progress: progress.Eventf}
			res, err := ex.Apply(ctx, *plan)
			if err != nil {
				progress.Failf("executing approved remediation")
				return err
			}
			progress.Printf("applied: op=%s target=%s verified=%t duration=%s", res.Operation, res.Target, res.Verified, res.EndedAt.Sub(res.StartedAt))

			if f.Notify {
				progress.Updatef("sending notification")
				n := cliNewNotifierFromEnv()
				if n == nil {
					progress.Failf("sending notification")
					return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
				}
				_ = n.Send(ctx, notify.Message{Title: "kube-ops-copilot remediate (applied)", Body: fmt.Sprintf("approvalId=%s op=%s target=%s verified=%t", approvalID, res.Operation, res.Target, res.Verified)})
			}

			progress.Donef("approved remediation executed")
			return nil
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster; else $KUBECONFIG; else ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context override (default: current-context)")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 10*time.Minute, "Overall remediate timeout")
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
	cmd.Flags().BoolVar(&f.ApprovalOnNull, "approval-on-null", false, "Request approval even if the LLM returns a null plan (useful for review/ack workflows)")

	cmd.Flags().StringVar(&f.PlanOut, "plan-out", "", "Where to write the generated plan JSON (defaults to a temp file)")
	cmd.Flags().BoolVar(&f.Apply, "apply", false, "Apply the plan after approval")
	cmd.Flags().BoolVar(&f.Notify, "notify", false, "Send a notification (Slack/Telegram via env vars)")

	return cmd
}

func sendRemediateNotification(ctx context.Context, enabled bool, title, body string) error {
	if !enabled {
		return nil
	}
	n := cliNewNotifierFromEnv()
	if n == nil {
		return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_N8N_WEBHOOK_URL and/or KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
	}
	return n.Send(ctx, notify.Message{Title: title, Body: strings.TrimSpace(body)})
}
