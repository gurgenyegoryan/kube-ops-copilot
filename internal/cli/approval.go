package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/approval"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
	"github.com/spf13/cobra"
)

type approvalFlags struct {
	Provider string
	Timeout  time.Duration
}

type approvalRequestFlags struct {
	PlanPath   string
	ApprovalID string
	Summary    string
	WritePlan  bool
}

type approvalStatusFlags struct {
	ApprovalID string
}

type approvalServeFlags struct {
	ListenAddr    string
	StorePath     string
	PublicBaseURL string
}

func NewApprovalCmd() *cobra.Command {
	f := approvalFlags{Timeout: 60 * time.Second}
	cmd := &cobra.Command{
		Use:   "approval",
		Short: "Approval workflows (Slack/Telegram/n8n/manual)",
	}
	cmd.PersistentFlags().StringVar(&f.Provider, "provider", "manual", "Approval provider: manual|n8n|telegram|slack")
	cmd.PersistentFlags().DurationVar(&f.Timeout, "timeout", 60*time.Second, "Timeout for approval operations")

	cmd.AddCommand(newApprovalRequestCmd(&f))
	cmd.AddCommand(newApprovalStatusCmd(&f))
	cmd.AddCommand(newApprovalServeCmd(&f))
	return cmd
}

func newApprovalRequestCmd(parent *approvalFlags) *cobra.Command {
	rf := approvalRequestFlags{}
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Create an approval request for a plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), parent.Timeout)
			defer cancel()

			if strings.TrimSpace(rf.PlanPath) == "" {
				return fmt.Errorf("missing --plan")
			}
			plan, err := exec.LoadPlan(rf.PlanPath)
			if err != nil {
				return fmt.Errorf("load plan: %w", err)
			}
			if err := plan.Validate(); err != nil {
				return fmt.Errorf("invalid plan: %w", err)
			}

			approvalID := strings.TrimSpace(rf.ApprovalID)
			if approvalID == "" {
				approvalID = uuid.NewString()
			}

			summary := strings.TrimSpace(rf.Summary)
			if summary == "" {
				summary = fmt.Sprintf("%s %s/%s", plan.Operation.Type, plan.Operation.Namespace, plan.Operation.Name)
			}

			cfg := approval.FromEnv()
			cfg.Provider = approval.Provider(strings.ToLower(parent.Provider))
			ap, err := approval.New(cfg)
			if err != nil {
				return err
			}

			id, err := ap.Request(ctx, approval.Request{
				ApprovalID: approvalID,
				Summary:    summary,
				PlanPath:   rf.PlanPath,
				Operation:  string(plan.Operation.Type),
				Target:     fmt.Sprintf("%s/%s", plan.Operation.Namespace, plan.Operation.Name),
			})
			if err != nil {
				return err
			}

			if rf.WritePlan {
				plan.ApprovalID = id
				b, err := json.MarshalIndent(plan, "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(rf.PlanPath, b, 0o600); err != nil {
					return err
				}
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "approval requested: provider=%s approval-id=%s\n", parent.Provider, id)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "next: kube-ops-copilot approval status --provider %s --approval-id %s\n", parent.Provider, id)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "then: kube-ops-copilot execute --plan %s --approval-provider %s --approval-id %s --approve\n", rf.PlanPath, parent.Provider, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&rf.PlanPath, "plan", "", "Path to execution plan JSON (required)")
	cmd.Flags().StringVar(&rf.ApprovalID, "approval-id", "", "Optional approval id (generated if empty)")
	cmd.Flags().StringVar(&rf.Summary, "summary", "", "Short human summary for the approval request")
	cmd.Flags().BoolVar(&rf.WritePlan, "write-plan", false, "Write approvalId back into the plan JSON")
	return cmd
}

func newApprovalStatusCmd(parent *approvalFlags) *cobra.Command {
	sf := approvalStatusFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check approval decision",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), parent.Timeout)
			defer cancel()

			if strings.TrimSpace(sf.ApprovalID) == "" {
				return fmt.Errorf("missing --approval-id")
			}
			cfg := approval.FromEnv()
			cfg.Provider = approval.Provider(strings.ToLower(parent.Provider))
			ap, err := approval.New(cfg)
			if err != nil {
				return err
			}
			st, err := ap.Status(ctx, sf.ApprovalID)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "approval status: provider=%s approval-id=%s decision=%s approver=%s reason=%s\n", parent.Provider, sf.ApprovalID, st.Decision, st.Approver, st.Reason)
			return nil
		},
	}
	cmd.Flags().StringVar(&sf.ApprovalID, "approval-id", "", "Approval identifier")
	return cmd
}

func newApprovalServeCmd(parent *approvalFlags) *cobra.Command {
	sv := approvalServeFlags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run an approval callback server (Slack interactivity)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.ToLower(parent.Provider) != string(approval.ProviderSlack) {
				return fmt.Errorf("approval serve currently supports --provider slack")
			}
			cfg := approval.FromEnv()
			if sv.ListenAddr != "" {
				cfg.ListenAddr = sv.ListenAddr
			}
			if sv.StorePath != "" {
				cfg.StorePath = sv.StorePath
			}
			if sv.PublicBaseURL != "" {
				cfg.PublicBaseURL = sv.PublicBaseURL
			}
			cfg.Provider = approval.ProviderSlack

			a, err := approval.New(cfg)
			if err != nil {
				return err
			}
			slack, ok := a.(approval.Slack)
			if !ok {
				return fmt.Errorf("internal error: expected slack approver")
			}
			addr := cfg.ListenAddr
			if strings.TrimSpace(addr) == "" {
				addr = ":8088"
			}

			srv := newApprovalHTTPServer(addr, slack)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "approval server listening on %s (endpoint: /slack/actions)\n", addr)
			return srv.ListenAndServe()
		},
	}
	cmd.Flags().StringVar(&sv.ListenAddr, "listen", ":8088", "Listen address")
	cmd.Flags().StringVar(&sv.StorePath, "store", "", "Store path for approvals (optional)")
	cmd.Flags().StringVar(&sv.PublicBaseURL, "public-base-url", "", "Public base URL for Slack interactivity request URL")
	return cmd
}
