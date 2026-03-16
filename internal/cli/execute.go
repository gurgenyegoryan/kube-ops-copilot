package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/approval"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/notify"
	"github.com/spf13/cobra"
)

type executeFlags struct {
	PlanPath   string
	Kubeconfig string
	Context    string
	ApprovalID string
	ApprovalProvider string
	Approve    bool
	WaitApproval bool
	ApprovalTimeout time.Duration
	DryRun     bool
	Timeout    time.Duration
	Notify     bool
}

func NewExecuteCmd() *cobra.Command {
	f := executeFlags{}
	cmd := &cobra.Command{
		Use:   "execute",
		Short: "Execute an approved remediation (approval-gated)",
		Long:  "Execution is disabled by default and requires explicit operator approval flags.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.PlanPath == "" {
				return errors.New("refusing to execute: missing --plan")
			}
			plan, err := exec.LoadPlan(f.PlanPath)
			if err != nil {
				return fmt.Errorf("load plan: %w", err)
			}
			if err := plan.Validate(); err != nil {
				return fmt.Errorf("invalid plan: %w", err)
			}

			if !f.Approve {
				return errors.New("refusing to execute: missing explicit approval (pass --approve)")
			}
			if f.ApprovalID == "" {
				return errors.New("refusing to execute: missing approval id (pass --approval-id)")
			}
			if strings.TrimSpace(plan.ApprovalID) != "" && plan.ApprovalID != f.ApprovalID {
				return fmt.Errorf("refusing to execute: approval id mismatch (plan=%q flag=%q)", plan.ApprovalID, f.ApprovalID)
			}

			provider := strings.ToLower(strings.TrimSpace(f.ApprovalProvider))
			if provider == "" {
				provider = string(approval.ProviderManual)
			}
			if provider != string(approval.ProviderManual) {
				if err := ensureApproved(cmd.Context(), provider, f.ApprovalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
					return err
				}
			}

			if f.DryRun {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "dry-run: no changes executed; approval validated (approval-id=%s)\n", f.ApprovalID)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "plan: op=%s target=%s/%s approval-provider=%s\n", plan.Operation.Type, plan.Operation.Namespace, plan.Operation.Name, provider)
				if f.Notify {
					n := notify.NewFromConfig(notify.FromEnv())
					if n == nil {
						return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
					}
					_ = n.Send(cmd.Context(), notify.Message{Title: "kube-ops-copilot execute (dry-run)", Body: fmt.Sprintf("approvalId=%s op=%s target=%s/%s", f.ApprovalID, plan.Operation.Type, plan.Operation.Namespace, plan.Operation.Name)})
				}
				return nil
			}

			ctx, cancel := contextWithTimeout(cmd, f.Timeout)
			defer cancel()

			client, err := kube.NewClient(kube.Config{Kubeconfig: f.Kubeconfig, Context: f.Context})
			if err != nil {
				return err
			}
			ex := exec.Executor{Client: client}
			result, err := ex.Apply(ctx, plan)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "executed: op=%s target=%s verified=%t duration=%s\n", result.Operation, result.Target, result.Verified, result.EndedAt.Sub(result.StartedAt))
			if f.Notify {
				n := notify.NewFromConfig(notify.FromEnv())
				if n == nil {
					return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
				}
				_ = n.Send(ctx, notify.Message{Title: "kube-ops-copilot execute (applied)", Body: fmt.Sprintf("approvalId=%s op=%s target=%s verified=%t", f.ApprovalID, result.Operation, result.Target, result.Verified)})
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&f.PlanPath, "plan", "", "Path to execution plan JSON (required)")
	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (defaults to in-cluster or ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context name")
	cmd.Flags().StringVar(&f.ApprovalID, "approval-id", "", "Approval identifier (ticket, Slack thread, n8n approval id)")
	cmd.Flags().StringVar(&f.ApprovalProvider, "approval-provider", "manual", "Approval provider: manual|n8n|telegram|slack")
	cmd.Flags().BoolVar(&f.Approve, "approve", false, "Explicitly approve execution (required)")
	cmd.Flags().BoolVar(&f.WaitApproval, "wait-approval", false, "Wait/poll for approval decision (non-manual providers)")
	cmd.Flags().DurationVar(&f.ApprovalTimeout, "approval-timeout", 5*time.Minute, "How long to wait for approval when --wait-approval is set")
	cmd.Flags().BoolVar(&f.DryRun, "dry-run", true, "Validate approval gate without applying changes")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 5*time.Minute, "Overall execute timeout")
	cmd.Flags().BoolVar(&f.Notify, "notify", false, "Send a notification (Slack/Telegram via env vars)")

	return cmd
}

func contextWithTimeout(cmd *cobra.Command, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = 5 * time.Minute
	}
	return context.WithTimeout(cmd.Context(), d)
}
