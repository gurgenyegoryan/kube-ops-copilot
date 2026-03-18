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
			progress := newLiveProgress(cmd.OutOrStdout(), "terraform-pr")
			defer progress.Close()
			if strings.TrimSpace(f.InfraRepoPath) == "" {
				progress.Failf("missing infrastructure repository path")
				return errors.New("missing --infra-repo-path")
			}

			provider := llm.Provider(strings.ToLower(strings.TrimSpace(f.Provider)))
			if provider == "" {
				provider = llm.ProviderNone
			}
			progress.Updatef("initializing LLM client")
			client, err := llm.New(llm.Config{Provider: provider, Model: f.Model, BaseURL: f.BaseURL, APIKey: f.APIKey})
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
			kclient, err := kube.NewClient(kube.Config{Kubeconfig: f.Kubeconfig, Context: f.Context})
			if err != nil {
				progress.Failf("connecting to cluster")
				return err
			}
			progress.Updatef("running analyzers")
			e := engine.Engine{Analyzers: defaultAnalyzers(ctx, kclient.Kubernetes, f.IncludeSystemNamespaces, f.EventsSince), Progress: progress.Eventf}
			results, err := e.Run(ctx)
			if err != nil {
				progress.Failf("running analyzers")
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

			progress.Updatef("building infrastructure repository inventory")
			repoInventory, err := buildTerraformRepoInventory(f.InfraRepoPath, string(repJSON), 60, 180000)
			if err != nil {
				progress.Failf("building infrastructure repository inventory")
				return err
			}
			inventoryPath, err := writeTextArtifact("kube-ops-copilot-infra-inventory", repoInventory)
			if err == nil {
				progress.Printf("infrastructure repo inventory snapshot: %s", inventoryPath)
			}

			system := strings.TrimSpace(`You are Kube Ops Copilot in Terraform PR mode.
You are given:
- a deterministic Kubernetes diagnosis report
- a sampled infrastructure repository inventory and file contents that may include Terraform, Terragrunt, Helm charts, Helmfile, values files, Kustomize overlays, raw YAML manifests, templates, and shell or CI entrypoints that call kubectl/helm/kustomize

Your job is to propose exactly one infrastructure code remediation when the durable fix should live in infrastructure-as-code.

Rules:
- Be evidence-first. Do not invent cluster facts or repo files.
- Prefer durable infrastructure-as-code changes over live kubectl actions in this mode.
- Modify only files that are explicitly present in the repo inventory.
- Produce a conservative patch that is realistic for the repo layout you were given.
- If the relevant workload configuration appears to live in Helm chart values/templates, Helmfile, Kustomize, raw manifests, or script-driven kubectl/helm entrypoints, it is acceptable to use search_replace edits against those files.
- Prefer HCL-aware edits like hcl_set_attribute, hcl_delete_attribute, hcl_replace_block, and hcl_append_block_body for HCL files, and use search_replace for non-HCL files when needed.
- If you cannot identify a safe Terraform change from the provided evidence and files, output null.

Output format:
- First, concise Markdown explaining the problem, why infrastructure-as-code is the right fix path, and what will change.
- Then EXACTLY ONE fenced code block labeled json containing either an InfraPRPlan object or null.`)

			user := fmt.Sprintf(`Kubernetes diagnosis report JSON:

%s

Infrastructure repository inventory:

%s

Task:
1) Decide whether the best remediation should be implemented in infrastructure code.
2) If yes, emit exactly one InfraPRPlan for backend=terraform.
3) The plan must only touch files present in the inventory.
4) Prefer HCL-aware edit types for HCL files, but use search_replace for YAML/templates/values files, Helmfile, Kustomize, or command-entrypoint files when that is the correct place to change Kubernetes behavior.
5) Keep the change minimal, safe, and production-realistic.

InfraPRPlan schema:
{
  "apiVersion": "kube-ops-copilot/v1alpha1",
  "kind": "InfraPRPlan",
  "backend": "terraform",
  "createdAt": "RFC3339",
  "approvalId": "",
  "summary": "...",
  "branchName": "...",
  "commitMessage": "...",
  "prTitle": "...",
  "prBody": "...",
  "edits": [
    {
      "type": "hcl_set_attribute|hcl_delete_attribute|hcl_replace_block|hcl_append_block_body|search_replace",
      "path": "relative/path",
      "blockType": "resource|module|locals|variable|terraform|provider|dependency|include",
      "labels": ["optional", "block", "labels"],
      "attribute": "optional attribute name",
      "valueHCL": "optional HCL value",
      "blockHCL": "optional HCL block/body",
      "search": "optional exact snippet",
      "replace": "optional replacement snippet"
    }
  ],
  "verify": {
    "commands": ["terraform fmt -recursive", "terraform validate"],
    "notes": ["..."]
  },
  "metadata": {
    "resource": "optional",
    "risk_note": "optional",
    "post_merge_check": "optional"
  }
}

Requirements:
- backend must be terraform.
- branchName, commitMessage, prTitle, summary are required.
- approvalId must be empty string.
- edits must be safe and realistic.
- If there is not enough evidence or repo context, output null.
`, string(repJSON), repoInventory)

			progress.Updatef("asking LLM for infrastructure PR plan")
			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				progress.Failf("asking LLM for infrastructure PR plan")
				return err
			}
			md := strings.TrimSpace(stripJSONPlanBlock(resp.Text))
			progress.Printf("%s", md)

			plan, err := extractAndValidateTerraformPRPlan(resp.Text)
			if err != nil {
				progress.Failf("validating infrastructure PR plan")
				return err
			}
			if plan == nil {
				progress.Printf("")
				progress.Donef("no infrastructure PR plan proposed for this snapshot")
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
			details := strings.TrimSpace(md)
			if len(details) > 900 {
				details = strings.TrimSpace(details[:900]) + "…"
			}
			progress.Updatef("requesting approval")
			_, err = ap.Request(ctx, approval.Request{
				ApprovalID: approvalID,
				Summary:    "terraform-pr " + strings.TrimSpace(plan.Summary),
				PlanPath:   planPath,
				Operation:  "terraform_pr",
				Target:     f.InfraRepoPath,
				Details:    details,
			})
			if err != nil {
				progress.Failf("requesting approval")
				return err
			}

			progress.Printf("approval requested: provider=%s approval-id=%s", approvalProvider, approvalID)
			progress.Printf("plan written: %s", planPath)
			if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot terraform-pr (approval requested)", truncateForTelegram(fmt.Sprintf("approvalId=%s provider=%s branch=%s repo=%s\nplan=%s", approvalID, approvalProvider, plan.BranchName, f.InfraRepoPath, planPath), 3500)); err != nil {
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
				progress.Donef("approval recorded; repo changes not applied because --apply was not requested")
				return nil
			}

			progress.Updatef("applying approved infrastructure plan")
			ex := infra.Executor{
				RepoPath:     f.InfraRepoPath,
				SkipFmt:      f.SkipFmt,
				RunValidate:  f.RunValidate,
				Push:         f.GitPush,
				OpenPR:       f.OpenPR,
				BaseBranch:   f.BaseBranch,
				RequireClean: f.RequireClean,
				Progress:     progress.Eventf,
			}
			result, err := ex.Apply(ctx, *plan)
			if err != nil {
				progress.Failf("applying infrastructure plan")
				return err
			}
			progress.Updatef("writing execution result")
			if err := infra.WriteResult(infra.ResultPathForPlan(planPath), result); err != nil {
				return err
			}
			progress.Donef("applied infrastructure repo changes")
			progress.Printf("applied terraform repo changes: branch=%s commit=%s pushed=%t pr=%s", result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
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
	cmd.Flags().BoolVar(&f.RunValidate, "terraform-validate", false, "Run terraform validate in changed module directories after edits")
	cmd.Flags().BoolVar(&f.RequireClean, "require-clean-repo", true, "Refuse to edit the Terraform repo if git status is dirty")

	return cmd
}
