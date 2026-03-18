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
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/report"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

type smartRemediateFlags struct {
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

func NewSmartRemediateCmd() *cobra.Command {
	f := smartRemediateFlags{}
	cmd := &cobra.Command{
		Use:   "smart-remediate",
		Short: "Let the agent choose the safest remediation path: live cluster action or infra PR",
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
				return errors.New("no LLM provider configured")
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

			repoInventory := "No infrastructure repo provided."
			if strings.TrimSpace(f.InfraRepoPath) != "" {
				inv, err := buildTerraformRepoInventory(f.InfraRepoPath, string(repJSON), 60, 180000)
				if err != nil {
					return err
				}
				repoInventory = inv
				if path, err := writeTextArtifact("kube-ops-copilot-infra-inventory", inv); err == nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "infrastructure repo inventory snapshot: %s\n", path)
				}
			}

			system := strings.TrimSpace(`You are Kube Ops Copilot in unified remediation mode.
You must choose the safest remediation path for the current evidence:
- a live cluster action via ExecutionPlan
- an infrastructure pull request via InfraPRPlan
- a compound plan with immediate live mitigation plus durable infra PR
- or null if neither is justified

Rules:
- choose live action only when the evidence clearly supports a safe immediate kubernetes change
- choose infra PR when the durable fix belongs in infrastructure-as-code and the repo inventory is sufficient, including cases where the real editable target is a Helm/Kustomize/raw YAML file or a shell/CI entrypoint rather than only HCL
- choose a compound plan only when the evidence supports both an immediate mitigation and a durable code fix
- if unsure, output null
- do not invent repo files or cluster facts
- produce exactly one JSON plan block`)

			user := fmt.Sprintf("Diagnosis report JSON:\n\n%s\n\nInfra repo inventory:\n\n%s\n\nOutput Markdown first, then one JSON fenced block containing either:\n1) ExecutionPlan\n2) InfraPRPlan\n3) CompoundRemediationPlan\n4) null\n\nExecutionPlan schema:\n{\n  \"apiVersion\":\"kube-ops-copilot/v1alpha1\",\n  \"kind\":\"ExecutionPlan\",\n  \"createdAt\":\"RFC3339\",\n  \"approvalId\":\"\",\n  \"operation\":{\"type\":\"rollout_restart_deployment|scale_deployment\",\"namespace\":\"...\",\"name\":\"...\",\"replicas\":3,\"reason\":\"...\"},\n  \"verify\":{\"timeoutSeconds\":180}\n}\n\nInfraPRPlan schema:\n{\n  \"apiVersion\":\"kube-ops-copilot/v1alpha1\",\n  \"kind\":\"InfraPRPlan\",\n  \"backend\":\"terraform|opentofu|terragrunt\",\n  \"createdAt\":\"RFC3339\",\n  \"approvalId\":\"\",\n  \"summary\":\"...\",\n  \"branchName\":\"...\",\n  \"commitMessage\":\"...\",\n  \"prTitle\":\"...\",\n  \"prBody\":\"...\",\n  \"edits\":[{\"type\":\"hcl_set_attribute|hcl_delete_attribute|hcl_replace_block|hcl_append_block_body|search_replace\",\"path\":\"relative/path\",\"blockType\":\"resource|module|locals|variable|terraform|provider|dependency|include\",\"labels\":[\"...\"],\"attribute\":\"...\",\"valueHCL\":\"...\",\"blockHCL\":\"...\",\"search\":\"...\",\"replace\":\"...\"}],\n  \"verify\":{\"commands\":[\"...\"],\"notes\":[\"...\"]},\n  \"metadata\":{\"risk_note\":\"optional\",\"post_merge_check\":\"optional\"}\n}\n\nCompoundRemediationPlan schema:\n{\n  \"apiVersion\":\"kube-ops-copilot/v1alpha1\",\n  \"kind\":\"CompoundRemediationPlan\",\n  \"createdAt\":\"RFC3339\",\n  \"approvalId\":\"\",\n  \"summary\":\"...\",\n  \"live\": { ExecutionPlan object },\n  \"infra\": { InfraPRPlan object }\n}\n", string(repJSON), repoInventory)

			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				return err
			}
			md := strings.TrimSpace(stripJSONPlanBlock(resp.Text))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), md)
			plan, err := extractAnyPlan(resp.Text)
			if err != nil {
				return err
			}
			if plan.Exec == nil && plan.Infra == nil && plan.Compound == nil {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "\n(no executable remediation plan proposed for this snapshot)")
				return sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate", truncateForTelegram(md, 3500))
			}

			approvalProvider := strings.ToLower(strings.TrimSpace(f.ApprovalProvider))
			if approvalProvider == "" {
				approvalProvider = string(approval.ProviderManual)
			}
			approvalID := strings.TrimSpace(f.ApprovalID)
			if approvalID == "" {
				approvalID = uuid.NewString()
			}

			if plan.Compound != nil {
				return executeSmartCompoundPlan(ctx, cmd, f, approvalProvider, approvalID, md, *plan.Compound, kclient.Kubernetes)
			}
			if plan.Exec != nil {
				return executeSmartLivePlan(ctx, cmd, f, approvalProvider, approvalID, md, *plan.Exec, kclient.Kubernetes)
			}
			return executeSmartInfraPlan(ctx, cmd, f, approvalProvider, approvalID, md, *plan.Infra)
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context override")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 3*time.Minute, "Overall timeout")
	cmd.Flags().DurationVar(&f.EventsSince, "events-since", 60*time.Minute, "How far back to analyze Warning events")
	cmd.Flags().BoolVar(&f.IncludeSystemNamespaces, "include-system-namespaces", false, "Include system namespaces in checks")
	cmd.Flags().StringVar(&f.Provider, "llm-provider", "", "LLM provider")
	cmd.Flags().StringVar(&f.Model, "llm-model", "", "LLM model")
	cmd.Flags().StringVar(&f.BaseURL, "llm-base-url", "", "LLM base URL")
	cmd.Flags().StringVar(&f.APIKey, "llm-api-key", "", "LLM API key")
	cmd.Flags().Float64Var(&f.Temperature, "llm-temperature", 0, "LLM temperature")
	cmd.Flags().StringVar(&f.InfraRepoPath, "infra-repo-path", "", "Optional infra repo path for infra PR planning")
	cmd.Flags().StringVar(&f.PlanOut, "plan-out", "", "Optional plan output path")
	cmd.Flags().BoolVar(&f.Apply, "apply", false, "Apply chosen plan after approval")
	cmd.Flags().BoolVar(&f.Notify, "notify", false, "Send notifications")
	cmd.Flags().StringVar(&f.ApprovalProvider, "approval-provider", "manual", "Approval provider")
	cmd.Flags().StringVar(&f.ApprovalID, "approval-id", "", "Approval identifier")
	cmd.Flags().BoolVar(&f.WaitApproval, "wait-approval", true, "Wait for approval")
	cmd.Flags().DurationVar(&f.ApprovalTimeout, "approval-timeout", 10*time.Minute, "Approval timeout")
	cmd.Flags().BoolVar(&f.GitPush, "git-push", false, "Push infra branch")
	cmd.Flags().BoolVar(&f.OpenPR, "open-pr", false, "Open infra PR")
	cmd.Flags().StringVar(&f.BaseBranch, "base-branch", "", "Infra PR base branch")
	cmd.Flags().BoolVar(&f.SkipFmt, "skip-fmt", false, "Skip infra fmt")
	cmd.Flags().BoolVar(&f.RunValidate, "validate", false, "Run infra validate")
	cmd.Flags().BoolVar(&f.RequireClean, "require-clean-repo", true, "Require clean infra repo")

	return cmd
}

func executeSmartLivePlan(ctx context.Context, cmd *cobra.Command, f smartRemediateFlags, approvalProvider, approvalID, md string, plan exec.Plan, client *kubernetes.Clientset) error {
	plan.CreatedAt = time.Now().UTC()
	plan.ApprovalID = approvalID
	planPath := strings.TrimSpace(f.PlanOut)
	if planPath == "" {
		planPath = filepath.Join(os.TempDir(), "kube-ops-copilot-smart-plan-"+approvalID+".json")
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
	_, err = ap.Request(ctx, approval.Request{
		ApprovalID: approvalID,
		Summary:    fmt.Sprintf("%s %s/%s", plan.Operation.Type, plan.Operation.Namespace, plan.Operation.Name),
		PlanPath:   planPath,
		Operation:  string(plan.Operation.Type),
		Target:     fmt.Sprintf("%s/%s", plan.Operation.Namespace, plan.Operation.Name),
		Details:    truncateForTelegram(md, 900),
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\napproval requested: provider=%s approval-id=%s\n", approvalProvider, approvalID)
	if approvalProvider != string(approval.ProviderManual) {
		if f.WaitApproval {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "waiting for approval decision (timeout=%s)…\n", f.ApprovalTimeout)
		}
		if err := ensureApproved(cmd.Context(), approvalProvider, approvalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
			return err
		}
	}
	if !f.Apply {
		return nil
	}
	ex := exec.Executor{Client: client}
	result, err := ex.Apply(ctx, plan)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "applied live remediation: op=%s target=%s verified=%t\n", result.Operation, result.Target, result.Verified)
	return nil
}

func executeSmartInfraPlan(ctx context.Context, cmd *cobra.Command, f smartRemediateFlags, approvalProvider, approvalID, md string, plan infra.PRPlan) error {
	plan.CreatedAt = time.Now().UTC()
	plan.ApprovalID = approvalID
	planPath := strings.TrimSpace(f.PlanOut)
	if planPath == "" {
		planPath = filepath.Join(os.TempDir(), "kube-ops-copilot-smart-infra-plan-"+approvalID+".json")
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
	_, err = ap.Request(ctx, approval.Request{
		ApprovalID: approvalID,
		Summary:    "infra-pr " + plan.Summary,
		PlanPath:   planPath,
		Operation:  "infra_pr",
		Target:     f.InfraRepoPath,
		Details:    truncateForTelegram(md, 900),
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\napproval requested: provider=%s approval-id=%s\n", approvalProvider, approvalID)
	if approvalProvider != string(approval.ProviderManual) {
		if f.WaitApproval {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "waiting for approval decision (timeout=%s)…\n", f.ApprovalTimeout)
		}
		if err := ensureApproved(cmd.Context(), approvalProvider, approvalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
			return err
		}
	}
	if !f.Apply {
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
	result, err := ex.Apply(ctx, plan)
	if err != nil {
		return err
	}
	if err := infra.WriteResult(infra.ResultPathForPlan(planPath), result); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "applied infra repo changes: backend=%s branch=%s commit=%s pushed=%t pr=%s\n", plan.Backend, result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
	return nil
}

func executeSmartCompoundPlan(ctx context.Context, cmd *cobra.Command, f smartRemediateFlags, approvalProvider, approvalID, md string, plan compoundPlan, client *kubernetes.Clientset) error {
	plan.CreatedAt = time.Now().UTC()
	plan.ApprovalID = approvalID
	planPath := strings.TrimSpace(f.PlanOut)
	if planPath == "" {
		planPath = filepath.Join(os.TempDir(), "kube-ops-copilot-compound-plan-"+approvalID+".json")
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
	_, err = ap.Request(ctx, approval.Request{
		ApprovalID: approvalID,
		Summary:    "compound-remediation " + plan.Summary,
		PlanPath:   planPath,
		Operation:  "compound_remediation",
		Target:     f.Context,
		Details:    truncateForTelegram(md, 900),
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\napproval requested: provider=%s approval-id=%s\n", approvalProvider, approvalID)
	if approvalProvider != string(approval.ProviderManual) {
		if f.WaitApproval {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "waiting for approval decision (timeout=%s)…\n", f.ApprovalTimeout)
		}
		if err := ensureApproved(cmd.Context(), approvalProvider, approvalID, f.WaitApproval, f.ApprovalTimeout); err != nil {
			return err
		}
	}
	if !f.Apply {
		return nil
	}
	executionResult := compoundExecutionResult{
		ApprovalID: approvalID,
		PlanPath:   planPath,
		Summary:    plan.Summary,
		StartedAt:  time.Now().UTC(),
	}
	if plan.Live != nil {
		executionResult.Live = &compoundPhaseResult{Name: "live", Status: compoundPhasePending}
	}
	if plan.Infra != nil {
		executionResult.Infra = &compoundPhaseResult{Name: "infra", Status: compoundPhasePending}
	}
	if err := writeCompoundResult(compoundResultPath(planPath), executionResult); err != nil {
		return err
	}
	if plan.Live != nil {
		executionResult.Live.Status = compoundPhaseRunning
		executionResult.Live.StartedAt = time.Now().UTC()
		_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
		live := *plan.Live
		live.ApprovalID = approvalID
		live.CreatedAt = time.Now().UTC()
		ex := exec.Executor{Client: client}
		result, err := ex.Apply(ctx, live)
		if err != nil {
			executionResult.Live.Status = compoundPhaseFailed
			executionResult.Live.EndedAt = time.Now().UTC()
			executionResult.Live.Message = err.Error()
			executionResult.EndedAt = time.Now().UTC()
			_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
			return err
		}
		executionResult.Live.Status = compoundPhaseSucceeded
		executionResult.Live.EndedAt = time.Now().UTC()
		executionResult.Live.Message = fmt.Sprintf("op=%s target=%s verified=%t", result.Operation, result.Target, result.Verified)
		_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "compound live mitigation applied: op=%s target=%s verified=%t\n", result.Operation, result.Target, result.Verified)
	}
	if plan.Infra != nil {
		executionResult.Infra.Status = compoundPhaseRunning
		executionResult.Infra.StartedAt = time.Now().UTC()
		_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
		infraPlan := *plan.Infra
		infraPlan.ApprovalID = approvalID
		infraPlan.CreatedAt = time.Now().UTC()
		ex := infra.Executor{
			RepoPath:     f.InfraRepoPath,
			SkipFmt:      f.SkipFmt,
			RunValidate:  f.RunValidate,
			Push:         f.GitPush,
			OpenPR:       f.OpenPR,
			BaseBranch:   f.BaseBranch,
			RequireClean: f.RequireClean,
		}
		result, err := ex.Apply(ctx, infraPlan)
		if err != nil {
			executionResult.Infra.Status = compoundPhaseFailed
			executionResult.Infra.EndedAt = time.Now().UTC()
			executionResult.Infra.Message = err.Error()
			executionResult.EndedAt = time.Now().UTC()
			_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
			return err
		}
		if err := infra.WriteResult(infra.ResultPathForPlan(planPath), result); err != nil {
			return err
		}
		executionResult.Infra.Status = compoundPhaseSucceeded
		executionResult.Infra.EndedAt = time.Now().UTC()
		executionResult.Infra.Message = fmt.Sprintf("backend=%s branch=%s commit=%s pushed=%t pr=%s", infraPlan.Backend, result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
		_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "compound infra remediation applied: backend=%s branch=%s commit=%s pushed=%t pr=%s\n", infraPlan.Backend, result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
	} else if executionResult.Infra != nil {
		executionResult.Infra.Status = compoundPhaseSkipped
	}
	executionResult.EndedAt = time.Now().UTC()
	if err := writeCompoundResult(compoundResultPath(planPath), executionResult); err != nil {
		return err
	}
	return nil
}
