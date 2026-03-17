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
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/report"
	"github.com/spf13/cobra"
)

type terraformPRFlags struct {
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

	InfraRepoPath string
	PlanOut       string
	Apply         bool
	Notify        bool

	ApprovalProvider string
	ApprovalID       string
	WaitApproval     bool
	ApprovalTimeout  time.Duration

	GitPush      bool
	OpenPR       bool
	BaseBranch   string
	SkipFmt      bool
	RunValidate  bool
	RequireClean bool
}

func NewTerraformPRCmd() *cobra.Command {
	f := terraformPRFlags{}
	cmd := &cobra.Command{
		Use:   "terraform-pr",
		Short: "Create a Terraform branch/commit/PR for an approved infrastructure remediation",
		Long:  "Runs cluster diagnosis, asks the LLM for one Terraform PR plan, requests approval, then edits the Terraform repo, commits on a new branch, and optionally pushes and opens a PR.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(f.InfraRepoPath) == "" {
				return errors.New("missing --infra-repo-path")
			}

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

			repoInventory, err := buildTerraformRepoInventory(f.InfraRepoPath, 25, 70000)
			if err != nil {
				return err
			}

			system := strings.TrimSpace(`You are Kube Ops Copilot in Terraform PR mode.
You are given:
- a deterministic Kubernetes diagnosis report
- a sampled Terraform repository inventory and file contents

Your job is to propose exactly one Terraform code remediation when the durable fix should live in infrastructure-as-code.

Rules:
- Be evidence-first. Do not invent cluster facts or repo files.
- Prefer durable Terraform changes over live kubectl actions in this mode.
- Modify only files that are explicitly present in the repo inventory.
- Produce a conservative patch that is realistic for Terraform.
- Use exact string replacements that can be applied safely.
- If you cannot identify a safe Terraform change from the provided evidence and files, output null.

Output format:
- First, concise Markdown explaining the problem, why Terraform is the right fix path, and what will change.
- Then EXACTLY ONE fenced code block labeled json containing either a TerraformPRPlan object or null.`)

			user := fmt.Sprintf("Kubernetes diagnosis report JSON:\n\n%s\n\nTerraform repository inventory:\n\n%s\n\nTask:\n1) Decide whether the best remediation should be implemented in Terraform code.\n2) If yes, emit exactly one TerraformPRPlan.\n3) The plan must only touch files present in the inventory and use exact search/replace edits.\n4) Keep the change minimal, safe, and production-realistic.\n\nTerraformPRPlan schema:\n{\n  \"apiVersion\": \"kube-ops-copilot/v1alpha1\",\n  \"kind\": \"TerraformPRPlan\",\n  \"createdAt\": \"RFC3339\",\n  \"approvalId\": \"\",\n  \"summary\": \"...\",\n  \"branchName\": \"...\",\n  \"commitMessage\": \"...\",\n  \"prTitle\": \"...\",\n  \"prBody\": \"...\",\n  \"edits\": [\n    {\n      \"path\": \"relative/path.tf\",\n      \"search\": \"exact existing snippet\",\n      \"replace\": \"replacement snippet\"\n    }\n  ],\n  \"verify\": {\n    \"commands\": [\"terraform fmt -recursive\", \"terraform validate\"],\n    \"notes\": [\"...\"]\n  },\n  \"metadata\": {\n    \"resource\": \"optional\"\n  }\n}\n\nRequirements:\n- branchName, commitMessage, prTitle, summary are required.\n- approvalId must be empty string.\n- edits must be safe literal replacements.\n- If there is not enough evidence or repo context, output null.\n", string(repJSON), repoInventory)

			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				return err
			}
			md := strings.TrimSpace(stripJSONPlanBlock(resp.Text))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), md)

			plan, err := extractAndValidateTerraformPRPlan(resp.Text)
			if err != nil {
				return err
			}
			if plan == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\n(no Terraform PR plan proposed for this snapshot)")
				if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot terraform-pr", truncateForTelegram(md, 3500)); err != nil {
					return err
				}
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
			plan.ApprovalID = approvalID
			plan.CreatedAt = time.Now().UTC()

			planPath := strings.TrimSpace(f.PlanOut)
			if planPath == "" {
				planPath = filepath.Join(os.TempDir(), "kube-ops-copilot-terraform-plan-"+approvalID+".json")
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
			details := strings.TrimSpace(md)
			if len(details) > 900 {
				details = strings.TrimSpace(details[:900]) + "…"
			}
			_, err = ap.Request(ctx, approval.Request{
				ApprovalID: approvalID,
				Summary:    "terraform-pr " + strings.TrimSpace(plan.Summary),
				PlanPath:   planPath,
				Operation:  "terraform_pr",
				Target:     f.InfraRepoPath,
				Details:    details,
			})
			if err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\napproval requested: provider=%s approval-id=%s\n", approvalProvider, approvalID)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "plan written: %s\n", planPath)
			if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot terraform-pr (approval requested)", truncateForTelegram(fmt.Sprintf("approvalId=%s provider=%s branch=%s repo=%s\nplan=%s", approvalID, approvalProvider, plan.BranchName, f.InfraRepoPath, planPath), 3500)); err != nil {
				return err
			}

			if approvalProvider != string(approval.ProviderManual) {
				if f.WaitApproval {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "waiting for approval decision (timeout=%s)…\n", f.ApprovalTimeout)
				}
				if err := ensureApproved(cmd.Context(), approvalProvider, approvalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
					return err
				}
			}

			if !f.Apply {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\nnot applying repo changes (pass --apply to edit repo after approval)")
				return nil
			}

			ex := infra.Executor{
				RepoPath:     f.InfraRepoPath,
				SkipFmt:      f.SkipFmt,
				RunValidate:  f.RunValidate,
				Push:         f.GitPush,
				OpenPR:       f.OpenPR,
				BaseBranch:   f.BaseBranch,
				RequireClean: f.RequireClean,
			}
			result, err := ex.Apply(ctx, *plan)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "applied terraform repo changes: branch=%s commit=%s pushed=%t pr=%s\n", result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
			if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot terraform-pr (applied)", truncateForTelegram(fmt.Sprintf("approvalId=%s branch=%s commit=%s pushed=%t pr=%s", approvalID, result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL), 3500)); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster; else $KUBECONFIG; else ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context override (default: current-context)")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 3*time.Minute, "Overall terraform-pr timeout")
	cmd.Flags().DurationVar(&f.EventsSince, "events-since", 60*time.Minute, "How far back to analyze Warning events")
	cmd.Flags().BoolVar(&f.IncludeSystemNamespaces, "include-system-namespaces", false, "Include kube-system and other system namespaces in workload/resource/policy checks")

	cmd.Flags().StringVar(&f.Provider, "llm-provider", "", "LLM provider: openai|anthropic|ollama")
	cmd.Flags().StringVar(&f.Model, "llm-model", "", "LLM model name (provider-specific)")
	cmd.Flags().StringVar(&f.BaseURL, "llm-base-url", "", "LLM base URL (optional)")
	cmd.Flags().StringVar(&f.APIKey, "llm-api-key", "", "LLM API key (prefer env vars; avoid shell history)")
	cmd.Flags().Float64Var(&f.Temperature, "llm-temperature", 0, "LLM temperature (optional)")

	cmd.Flags().StringVar(&f.InfraRepoPath, "infra-repo-path", "", "Path to the Terraform repository root (required)")
	cmd.Flags().StringVar(&f.PlanOut, "plan-out", "", "Where to write the generated TerraformPRPlan JSON (defaults to a temp file)")
	cmd.Flags().BoolVar(&f.Apply, "apply", false, "Apply Terraform repo changes after approval")
	cmd.Flags().BoolVar(&f.Notify, "notify", false, "Send a notification (n8n/Slack/Telegram via env vars)")

	cmd.Flags().StringVar(&f.ApprovalProvider, "approval-provider", "manual", "Approval provider: manual|n8n|telegram|slack")
	cmd.Flags().StringVar(&f.ApprovalID, "approval-id", "", "Optional approval id (generated if empty)")
	cmd.Flags().BoolVar(&f.WaitApproval, "wait-approval", true, "Wait/poll for approval decision")
	cmd.Flags().DurationVar(&f.ApprovalTimeout, "approval-timeout", 10*time.Minute, "How long to wait for approval")

	cmd.Flags().BoolVar(&f.GitPush, "git-push", false, "Push the new branch to origin after commit")
	cmd.Flags().BoolVar(&f.OpenPR, "open-pr", false, "Open a GitHub pull request with gh after push")
	cmd.Flags().StringVar(&f.BaseBranch, "base-branch", "", "Base branch for gh pr create (optional)")
	cmd.Flags().BoolVar(&f.SkipFmt, "skip-fmt", false, "Skip terraform fmt -recursive")
	cmd.Flags().BoolVar(&f.RunValidate, "terraform-validate", false, "Run terraform validate in the repo root after edits")
	cmd.Flags().BoolVar(&f.RequireClean, "require-clean-repo", true, "Refuse to edit the Terraform repo if git status is dirty")

	return cmd
}
