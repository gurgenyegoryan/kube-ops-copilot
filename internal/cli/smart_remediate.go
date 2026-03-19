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

	RepoAgent repoAgentFlags
}

func NewSmartRemediateCmd() *cobra.Command {
	f := smartRemediateFlags{}
	cmd := &cobra.Command{
		Use:   "smart-remediate",
		Short: "Let the agent choose the safest remediation path: live cluster action or infra PR",
		RunE: func(cmd *cobra.Command, args []string) error {
			progress := newLiveProgress(cmd.OutOrStdout(), "smart-remediate")
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
				return errors.New("no LLM provider configured")
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

			repoInventory := "No infrastructure repo provided."
			if strings.TrimSpace(f.InfraRepoPath) != "" {
				progress.Updatef("building infrastructure repository inventory")
				inv, err := cliBuildTerraformRepoInventory(f.InfraRepoPath, string(repJSON), 60, 180000)
				if err != nil {
					progress.Failf("building infrastructure repository inventory")
					return err
				}
				repoInventory = inv
				progress.Eventf("prepared infrastructure repository inventory bytes=%d", len(inv))
				if path, err := writeTextArtifact("kube-ops-copilot-infra-inventory", inv); err == nil {
					progress.Printf("infrastructure repo inventory snapshot: %s", path)
				}
			} else {
				progress.Eventf("no infrastructure repository path provided; plan selection limited to live actions")
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

			user := fmt.Sprintf("Diagnosis report JSON:\n\n%s\n\nInfra repo inventory:\n\n%s\n\nOutput Markdown first, then one JSON fenced block containing either:\n1) ExecutionPlan\n2) InfraPRPlan\n3) CompoundRemediationPlan\n4) null\n\nExecutionPlan schema:\n{\n  \"apiVersion\":\"kube-ops-copilot/v1alpha1\",\n  \"kind\":\"ExecutionPlan\",\n  \"createdAt\":\"RFC3339\",\n  \"approvalId\":\"\",\n  \"operation\":{\"type\":\"rollout_restart_deployment|scale_deployment\",\"namespace\":\"...\",\"name\":\"...\",\"replicas\":3,\"reason\":\"...\"},\n  \"verify\":{\"timeoutSeconds\":180}\n}\n\nInfraPRPlan schema:\n{\n  \"apiVersion\":\"kube-ops-copilot/v1alpha1\",\n  \"kind\":\"InfraPRPlan\",\n  \"backend\":\"terraform|opentofu|terragrunt\",\n  \"createdAt\":\"RFC3339\",\n  \"approvalId\":\"\",\n  \"summary\":\"...\",\n  \"agentPrompt\":\"optional concise repo-editing brief for another coding agent\",\n  \"branchName\":\"...\",\n  \"commitMessage\":\"...\",\n  \"prTitle\":\"...\",\n  \"prBody\":\"...\",\n  \"edits\":[{\"type\":\"hcl_set_attribute|hcl_delete_attribute|hcl_replace_block|hcl_append_block_body|search_replace\",\"path\":\"relative/path\",\"blockType\":\"resource|module|locals|variable|terraform|provider|dependency|include\",\"labels\":[\"...\"],\"attribute\":\"...\",\"valueHCL\":\"...\",\"blockHCL\":\"...\",\"search\":\"...\",\"replace\":\"...\"}],\n  \"verify\":{\"commands\":[\"...\"],\"notes\":[\"...\"]},\n  \"metadata\":{\"risk_note\":\"optional\",\"post_merge_check\":\"optional\"}\n}\n\nCompoundRemediationPlan schema:\n{\n  \"apiVersion\":\"kube-ops-copilot/v1alpha1\",\n  \"kind\":\"CompoundRemediationPlan\",\n  \"createdAt\":\"RFC3339\",\n  \"approvalId\":\"\",\n  \"summary\":\"...\",\n  \"live\": { ExecutionPlan object },\n  \"infra\": { InfraPRPlan object }\n}\n", string(repJSON), repoInventory)

			progress.Updatef("asking LLM for remediation path selection")
			progress.Eventf("submitting LLM request provider=%s model=%s", provider, strings.TrimSpace(f.Model))
			resp, err := client.Complete(ctx, llm.Request{System: system, User: user, Model: f.Model, Temperature: f.Temperature})
			if err != nil {
				progress.Failf("asking LLM for remediation path selection")
				return withLLMTimeoutHint(err, "smart-remediate", f.Timeout)
			}
			md := strings.TrimSpace(stripJSONPlanBlock(resp.Text))
			if md != "" {
				progress.Printf("%s", md)
			}
			plan, err := extractAnyPlan(resp.Text)
			if err != nil {
				progress.Failf("validating remediation plan")
				return err
			}
			if plan.Exec == nil && plan.Infra == nil && plan.Compound == nil {
				progress.Printf("(no executable remediation plan proposed for this snapshot)")
				if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate", truncateForTelegram(md, 3500)); err != nil {
					progress.Failf("sending notification")
					return err
				}
				progress.Donef("advisory-only result; no executable remediation path proposed")
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

			if plan.Compound != nil {
				return executeSmartCompoundPlan(ctx, progress, cmd, f, approvalProvider, approvalID, md, *plan.Compound, kclient.Kubernetes, repoInventory)
			}
			if plan.Exec != nil {
				return executeSmartLivePlan(ctx, progress, cmd, f, approvalProvider, approvalID, md, *plan.Exec, kclient.Kubernetes)
			}
			return executeSmartInfraPlan(ctx, progress, cmd, f, approvalProvider, approvalID, md, *plan.Infra, repoInventory)
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context override")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 10*time.Minute, "Overall timeout")
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
	cmd.Flags().StringVar(&f.RepoAgent.Provider, "repo-agent-provider", "", "Optional secondary repo agent provider: openai|anthropic|ollama|codex-cli|claude-code")
	cmd.Flags().StringVar(&f.RepoAgent.Model, "repo-agent-model", "", "Optional secondary repo agent model (defaults to --llm-model)")
	cmd.Flags().StringVar(&f.RepoAgent.BaseURL, "repo-agent-base-url", "", "Optional secondary repo agent base URL")
	cmd.Flags().StringVar(&f.RepoAgent.APIKey, "repo-agent-api-key", "", "Optional secondary repo agent API key")
	cmd.Flags().StringVar(&f.RepoAgent.Command, "repo-agent-command", "", "Optional external repo agent command path (useful for codex-cli or claude-code)")
	cmd.Flags().Float64Var(&f.RepoAgent.Temperature, "repo-agent-temperature", 0, "Optional secondary repo agent temperature")

	return cmd
}

func executeSmartLivePlan(ctx context.Context, progress *liveProgress, cmd *cobra.Command, f smartRemediateFlags, approvalProvider, approvalID, md string, plan exec.Plan, client *kubernetes.Clientset) error {
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
	progress.Updatef("writing live remediation plan")
	if err := os.WriteFile(planPath, b, 0o600); err != nil {
		return err
	}
	cfg := approval.FromEnv()
	cfg.Provider = approval.Provider(approvalProvider)
	ap, err := approval.New(cfg)
	if err != nil {
		return err
	}
	progress.Updatef("requesting approval")
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
	progress.Printf("approval requested: provider=%s approval-id=%s", approvalProvider, approvalID)
	progress.Printf("plan written: %s", planPath)
	if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate (approval requested)", truncateForTelegram(fmt.Sprintf("approvalId=%s provider=%s mode=live op=%s target=%s/%s\nplan=%s", approvalID, approvalProvider, plan.Operation.Type, plan.Operation.Namespace, plan.Operation.Name, planPath), 3500)); err != nil {
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
		progress.Donef("approval recorded; live changes not applied because --apply was not requested")
		return nil
	}
	progress.Updatef("applying approved live remediation")
	ex := exec.Executor{Client: client, Progress: progress.Eventf}
	result, err := ex.Apply(ctx, plan)
	if err != nil {
		progress.Failf("applying approved live remediation")
		return err
	}
	progress.Printf("applied live remediation: op=%s target=%s verified=%t", result.Operation, result.Target, result.Verified)
	if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate (applied)", truncateForTelegram(fmt.Sprintf("approvalId=%s mode=live op=%s target=%s verified=%t", approvalID, result.Operation, result.Target, result.Verified), 3500)); err != nil {
		progress.Failf("sending notification")
		return err
	}
	progress.Donef("live remediation applied")
	return nil
}

func executeSmartInfraPlan(ctx context.Context, progress *liveProgress, cmd *cobra.Command, f smartRemediateFlags, approvalProvider, approvalID, md string, plan infra.PRPlan, repoInventory string) error {
	plan.CreatedAt = time.Now().UTC()
	plan.ApprovalID = approvalID
	ensureInfraAgentPrompt(&plan)
	planPath := strings.TrimSpace(f.PlanOut)
	if planPath == "" {
		planPath = filepath.Join(os.TempDir(), "kube-ops-copilot-smart-infra-plan-"+approvalID+".json")
	}
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	progress.Updatef("writing infrastructure remediation plan")
	if err := os.WriteFile(planPath, b, 0o600); err != nil {
		return err
	}
	cfg := approval.FromEnv()
	cfg.Provider = approval.Provider(approvalProvider)
	ap, err := approval.New(cfg)
	if err != nil {
		return err
	}
	progress.Updatef("requesting approval")
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
	progress.Printf("approval requested: provider=%s approval-id=%s", approvalProvider, approvalID)
	progress.Printf("plan written: %s", planPath)
	if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate (approval requested)", truncateForTelegram(fmt.Sprintf("approvalId=%s provider=%s mode=infra branch=%s repo=%s\nplan=%s", approvalID, approvalProvider, plan.BranchName, f.InfraRepoPath, planPath), 3500)); err != nil {
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
		progress.Donef("approval recorded; infrastructure changes not applied because --apply was not requested")
		return nil
	}
	repoAgent, err := maybeNewRepoAgentWorker(ctx, progress, f.RepoAgent, llm.Config{
		Provider: llm.Provider(strings.ToLower(strings.TrimSpace(f.Provider))),
		Model:    f.Model,
		BaseURL:  f.BaseURL,
		APIKey:   f.APIKey,
	})
	if err != nil {
		progress.Failf("initializing repo agent")
		return err
	}
	progress.Updatef("applying approved infrastructure plan")
	ex := infra.Executor{
		RepoPath:      f.InfraRepoPath,
		SkipFmt:       f.SkipFmt,
		RunValidate:   f.RunValidate,
		Push:          f.GitPush,
		OpenPR:        f.OpenPR,
		BaseBranch:    f.BaseBranch,
		RequireClean:  f.RequireClean,
		Progress:      progress.Eventf,
		RepoAgent:     repoAgent,
		RepoInventory: repoInventory,
	}
	result, err := ex.Apply(ctx, plan)
	if err != nil {
		progress.Failf("applying approved infrastructure plan")
		return err
	}
	progress.Updatef("writing infrastructure execution result")
	if err := infra.WriteResult(infra.ResultPathForPlan(planPath), result); err != nil {
		return err
	}
	progress.Printf("applied infra repo changes: backend=%s branch=%s commit=%s pushed=%t pr=%s", plan.Backend, result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
	if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate (applied)", truncateForTelegram(formatInfraApplyNotification("approvalId="+approvalID+" mode=infra backend="+string(plan.Backend), result), 3500)); err != nil {
		progress.Failf("sending notification")
		return err
	}
	progress.Donef("infrastructure remediation applied")
	return nil
}

func executeSmartCompoundPlan(ctx context.Context, progress *liveProgress, cmd *cobra.Command, f smartRemediateFlags, approvalProvider, approvalID, md string, plan compoundPlan, client *kubernetes.Clientset, repoInventory string) error {
	plan.CreatedAt = time.Now().UTC()
	plan.ApprovalID = approvalID
	if plan.Infra != nil {
		ensureInfraAgentPrompt(plan.Infra)
	}
	planPath := strings.TrimSpace(f.PlanOut)
	if planPath == "" {
		planPath = filepath.Join(os.TempDir(), "kube-ops-copilot-compound-plan-"+approvalID+".json")
	}
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	progress.Updatef("writing compound remediation plan")
	if err := os.WriteFile(planPath, b, 0o600); err != nil {
		return err
	}
	cfg := approval.FromEnv()
	cfg.Provider = approval.Provider(approvalProvider)
	ap, err := approval.New(cfg)
	if err != nil {
		return err
	}
	progress.Updatef("requesting approval")
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
	progress.Printf("approval requested: provider=%s approval-id=%s", approvalProvider, approvalID)
	progress.Printf("plan written: %s", planPath)
	if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate (approval requested)", truncateForTelegram(fmt.Sprintf("approvalId=%s provider=%s mode=compound summary=%s\nplan=%s", approvalID, approvalProvider, plan.Summary, planPath), 3500)); err != nil {
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
		progress.Donef("approval recorded; compound changes not applied because --apply was not requested")
		return nil
	}
	progress.Updatef("writing compound execution trace")
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
		progress.Updatef("applying compound live mitigation")
		executionResult.Live.Status = compoundPhaseRunning
		executionResult.Live.StartedAt = time.Now().UTC()
		_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
		live := *plan.Live
		live.ApprovalID = approvalID
		live.CreatedAt = time.Now().UTC()
		ex := exec.Executor{Client: client, Progress: progress.Eventf}
		result, err := ex.Apply(ctx, live)
		if err != nil {
			progress.Failf("applying compound live mitigation")
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
		progress.Printf("compound live mitigation applied: op=%s target=%s verified=%t", result.Operation, result.Target, result.Verified)
	}
	if plan.Infra != nil {
		progress.Updatef("applying compound infrastructure remediation")
		executionResult.Infra.Status = compoundPhaseRunning
		executionResult.Infra.StartedAt = time.Now().UTC()
		_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
		infraPlan := *plan.Infra
		infraPlan.ApprovalID = approvalID
		infraPlan.CreatedAt = time.Now().UTC()
		ensureInfraAgentPrompt(&infraPlan)
		repoAgent, err := maybeNewRepoAgentWorker(ctx, progress, f.RepoAgent, llm.Config{
			Provider: llm.Provider(strings.ToLower(strings.TrimSpace(f.Provider))),
			Model:    f.Model,
			BaseURL:  f.BaseURL,
			APIKey:   f.APIKey,
		})
		if err != nil {
			progress.Failf("initializing repo agent")
			executionResult.Infra.Status = compoundPhaseFailed
			executionResult.Infra.EndedAt = time.Now().UTC()
			executionResult.Infra.Message = err.Error()
			executionResult.EndedAt = time.Now().UTC()
			_ = writeCompoundResult(compoundResultPath(planPath), executionResult)
			return err
		}
		ex := infra.Executor{
			RepoPath:      f.InfraRepoPath,
			SkipFmt:       f.SkipFmt,
			RunValidate:   f.RunValidate,
			Push:          f.GitPush,
			OpenPR:        f.OpenPR,
			BaseBranch:    f.BaseBranch,
			RequireClean:  f.RequireClean,
			Progress:      progress.Eventf,
			RepoAgent:     repoAgent,
			RepoInventory: repoInventory,
		}
		result, err := ex.Apply(ctx, infraPlan)
		if err != nil {
			progress.Failf("applying compound infrastructure remediation")
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
		progress.Printf("compound infra remediation applied: backend=%s branch=%s commit=%s pushed=%t pr=%s", infraPlan.Backend, result.BranchName, result.CommitSHA, result.Pushed, result.PullRequestURL)
	} else if executionResult.Infra != nil {
		executionResult.Infra.Status = compoundPhaseSkipped
	}
	executionResult.EndedAt = time.Now().UTC()
	if err := writeCompoundResult(compoundResultPath(planPath), executionResult); err != nil {
		return err
	}
	compoundMsg := fmt.Sprintf("approvalId=%s mode=compound live=%t infra=%t result=%s", approvalID, plan.Live != nil, plan.Infra != nil, compoundResultPath(planPath))
	if executionResult.Infra != nil && executionResult.Infra.Status == compoundPhaseSucceeded && strings.TrimSpace(executionResult.Infra.Message) != "" {
		compoundMsg += "\n" + executionResult.Infra.Message
	}
	if err := sendRemediateNotification(ctx, f.Notify, "kube-ops-copilot smart-remediate (applied)", truncateForTelegram(compoundMsg, 3500)); err != nil {
		progress.Failf("sending notification")
		return err
	}
	progress.Donef("compound remediation applied")
	return nil
}
