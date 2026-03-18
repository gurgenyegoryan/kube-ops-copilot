package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/approval"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
	"github.com/spf13/cobra"
)

type infraFlags struct {
	RepoPath         string
	PlanPath         string
	ApprovalProvider string
	ApprovalID       string
	WaitApproval     bool
	ApprovalTimeout  time.Duration
	Apply            bool
	GitPush          bool
	OpenPR           bool
	BaseBranch       string
	SkipFmt          bool
	RunValidate      bool
	RequireClean     bool
}

func NewInfraCmd() *cobra.Command {
	f := infraFlags{}
	cmd := &cobra.Command{
		Use:   "infra",
		Short: "Infrastructure-as-code remediation workflows",
	}
	cmd.AddCommand(newInfraExecuteCmd(&f))
	cmd.AddCommand(newInfraStatusCmd(&f))
	return cmd
}

func newInfraExecuteCmd(f *infraFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "execute",
		Short: "Execute an approved infrastructure PR plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			progress := newLiveProgress(cmd.OutOrStdout(), "infra-execute")
			defer progress.Close()
			if strings.TrimSpace(f.PlanPath) == "" {
				progress.Failf("missing plan path")
				return fmt.Errorf("missing --plan")
			}
			progress.Updatef("loading infrastructure plan")
			planPath, err := resolvePlanPath(f.PlanPath)
			if err != nil {
				progress.Failf("loading infrastructure plan")
				return err
			}
			plan, err := infra.LoadPlan(planPath)
			if err != nil {
				progress.Failf("loading infrastructure plan")
				return err
			}
			if err := plan.Validate(); err != nil {
				progress.Failf("validating infrastructure plan")
				return err
			}
			if !f.Apply {
				progress.Failf("apply flag missing")
				return fmt.Errorf("refusing to execute infra plan without --apply")
			}
			if strings.TrimSpace(f.ApprovalID) == "" {
				progress.Failf("approval id missing")
				return fmt.Errorf("missing --approval-id")
			}
			if strings.TrimSpace(plan.ApprovalID) != "" && plan.ApprovalID != f.ApprovalID {
				return fmt.Errorf("approval id mismatch (plan=%q flag=%q)", plan.ApprovalID, f.ApprovalID)
			}

			provider := strings.ToLower(strings.TrimSpace(f.ApprovalProvider))
			if provider == "" {
				provider = string(approval.ProviderManual)
			}
			if provider != string(approval.ProviderManual) {
				progress.Updatef("checking external approval status")
				if err := ensureApproved(cmd.Context(), provider, f.ApprovalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
					progress.Failf("approval was not granted")
					return err
				}
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Minute)
			defer cancel()
			progress.Updatef("executing infrastructure plan")
			ex := infra.Executor{
				RepoPath:     f.RepoPath,
				SkipFmt:      f.SkipFmt,
				RunValidate:  f.RunValidate,
				Push:         f.GitPush,
				OpenPR:       f.OpenPR,
				BaseBranch:   f.BaseBranch,
				RequireClean: f.RequireClean,
				Progress:     progress.Eventf,
			}
			result, err := ex.Apply(ctx, plan)
			if err != nil {
				progress.Failf("executing infrastructure plan")
				return err
			}
			progress.Updatef("writing execution result")
			if err := infra.WriteResult(infra.ResultPathForPlan(planPath), result); err != nil {
				return err
			}
			progress.Donef("infrastructure plan executed")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "infra execute: branch=%s commit=%s pushed=%t pr=%s\n", result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
			return nil
		},
	}
	addInfraFlags(cmd, f)
	return cmd
}

func newInfraStatusCmd(f *infraFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local status of an infrastructure PR plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(f.PlanPath) == "" {
				return fmt.Errorf("missing --plan")
			}
			planPath, err := resolvePlanPath(f.PlanPath)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			st, err := infra.Inspect(ctx, f.RepoPath, planPath)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), st.Summary())
			return nil
		},
	}
	cmd.Flags().StringVar(&f.PlanPath, "plan", "", "Path to an InfraPRPlan JSON")
	cmd.Flags().StringVar(&f.RepoPath, "infra-repo-path", "", "Path to the infrastructure repo root")
	return cmd
}

func addInfraFlags(cmd *cobra.Command, f *infraFlags) {
	cmd.Flags().StringVar(&f.PlanPath, "plan", "", "Path to an InfraPRPlan JSON")
	cmd.Flags().StringVar(&f.RepoPath, "infra-repo-path", "", "Path to the infrastructure repo root")
	cmd.Flags().StringVar(&f.ApprovalProvider, "approval-provider", "manual", "Approval provider: manual|n8n|telegram|slack")
	cmd.Flags().StringVar(&f.ApprovalID, "approval-id", "", "Approval identifier")
	cmd.Flags().BoolVar(&f.WaitApproval, "wait-approval", false, "Wait/poll for approval decision")
	cmd.Flags().DurationVar(&f.ApprovalTimeout, "approval-timeout", 10*time.Minute, "How long to wait for approval")
	cmd.Flags().BoolVar(&f.Apply, "apply", false, "Apply the infrastructure plan")
	cmd.Flags().BoolVar(&f.GitPush, "git-push", false, "Push the new branch to origin after commit")
	cmd.Flags().BoolVar(&f.OpenPR, "open-pr", false, "Open a GitHub PR with gh after push")
	cmd.Flags().StringVar(&f.BaseBranch, "base-branch", "", "Base branch for gh pr create")
	cmd.Flags().BoolVar(&f.SkipFmt, "skip-fmt", false, "Skip backend formatting step")
	cmd.Flags().BoolVar(&f.RunValidate, "validate", false, "Run backend validate step")
	cmd.Flags().BoolVar(&f.RequireClean, "require-clean-repo", true, "Refuse to edit a dirty git repo")
}
