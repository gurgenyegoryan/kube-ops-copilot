package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
)

type repoAgentFlags struct {
	Provider    string
	Model       string
	BaseURL     string
	APIKey      string
	Temperature float64
	Command     string
}

var repoAgentJSONFenceRe = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\}|null)\\s*```")

type llmRepoWorker struct {
	Client      llm.Client
	Model       string
	Temperature float64
}

type externalRepoWorker struct {
	Provider string
	Command  string
	Model    string
	Progress func(string, ...any)
}

func (w llmRepoWorker) ExecutionMode() infra.RepoAgentExecutionMode {
	return infra.RepoAgentExecutionModeInPlace
}

func (w externalRepoWorker) ExecutionMode() infra.RepoAgentExecutionMode {
	return infra.RepoAgentExecutionModeIsolatedWorktree
}

func maybeNewRepoAgentWorker(ctx context.Context, progress *liveProgress, flags repoAgentFlags, fallback llm.Config) (infra.PlanRefiner, error) {
	providerName := strings.ToLower(strings.TrimSpace(flags.Provider))
	if providerName == "" || providerName == string(llm.ProviderNone) {
		return nil, nil
	}
	switch providerName {
	case "codex":
		providerName = "codex-cli"
	case "claude":
		providerName = "claude-code"
	}
	switch providerName {
	case "codex-cli", "claude-code":
		command := strings.TrimSpace(flags.Command)
		if command == "" {
			command = defaultRepoAgentCommand(providerName)
		}
		var progressFn func(string, ...any)
		if progress != nil {
			progress.Updatef("initializing repo agent")
			progress.Eventf("repo agent provider=%s command=%s model=%s", providerName, command, strings.TrimSpace(flags.Model))
			progressFn = progress.Eventf
		}
		return externalRepoWorker{
			Provider: providerName,
			Command:  command,
			Model:    strings.TrimSpace(flags.Model),
			Progress: progressFn,
		}, nil
	}
	provider := llm.Provider(providerName)
	cfg := llm.Config{
		Provider: provider,
		Model:    strings.TrimSpace(flags.Model),
		BaseURL:  strings.TrimSpace(flags.BaseURL),
		APIKey:   strings.TrimSpace(flags.APIKey),
	}
	if cfg.Model == "" {
		cfg.Model = fallback.Model
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = fallback.BaseURL
	}
	if cfg.APIKey == "" {
		cfg.APIKey = fallback.APIKey
	}
	if progress != nil {
		progress.Updatef("initializing repo agent")
		progress.Eventf("repo agent provider=%s model=%s", cfg.Provider, strings.TrimSpace(cfg.Model))
	}
	client, err := cliNewLLMClient(cfg)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("repo agent provider %q is configured but no client could be created", cfg.Provider)
	}
	return llmRepoWorker{
		Client:      client,
		Model:       cfg.Model,
		Temperature: flags.Temperature,
	}, nil
}

func (w llmRepoWorker) Refine(ctx context.Context, req infra.RefineRequest) (infra.RefineResult, error) {
	if w.Client == nil {
		return infra.RefineResult{}, fmt.Errorf("repo agent client is nil")
	}
	if err := req.Plan.Validate(); err != nil {
		return infra.RefineResult{}, fmt.Errorf("base infra plan is invalid: %w", err)
	}
	brief := strings.TrimSpace(req.Plan.AgentPrompt)
	if brief == "" {
		brief = synthesizeInfraAgentPrompt(req.Plan)
	}
	system := strings.TrimSpace(`You are a repository worker agent for infrastructure remediation.
You are given:
- a repository root path
- a deterministic repository inventory sampled strictly from that repo root
- a high-level infrastructure remediation plan
- an agent brief describing what should change

Rules:
- Operate only within the provided repository root.
- Do not invent files that are not present in the inventory.
- Return a refined InfraPRPlan with concrete relative file paths and realistic edits.
- Preserve the original branch/commit/pr intent unless a small improvement is clearly needed.
- Prefer minimal safe edits.
- If the base plan is already concrete and correct, you may keep the same edits.

Output:
- concise Markdown first
- then EXACTLY ONE fenced json block containing an InfraPRPlan`)
	user := fmt.Sprintf(`Repository root:
%s

High-level infra plan JSON:
%s

Agent brief:
%s

Repository inventory:
%s

Return a refined InfraPRPlan that keeps the same intent but uses the most plausible concrete repo paths and edits visible in the inventory.
`, req.RepoPath, mustRepoAgentJSON(req.Plan), brief, req.RepoInventory)
	resp, err := w.Client.Complete(ctx, llm.Request{
		System:      system,
		User:        user,
		Model:       w.Model,
		Temperature: w.Temperature,
	})
	if err != nil {
		return infra.RefineResult{}, err
	}
	refined, err := extractRepoAgentPlan(resp.Text)
	if err != nil {
		return infra.RefineResult{}, err
	}
	merged := mergeRepoAgentPlan(req.Plan, refined)
	if err := merged.Validate(); err != nil {
		return infra.RefineResult{}, fmt.Errorf("repo agent returned invalid plan: %w", err)
	}
	return infra.RefineResult{
		Plan:        merged,
		Narrative:   strings.TrimSpace(stripRepoAgentJSON(resp.Text)),
		DirectEdits: false,
	}, nil
}

func (w externalRepoWorker) Refine(ctx context.Context, req infra.RefineRequest) (infra.RefineResult, error) {
	if strings.TrimSpace(w.Command) == "" {
		return infra.RefineResult{}, fmt.Errorf("external repo agent command is empty")
	}
	if err := req.Plan.Validate(); err != nil {
		return infra.RefineResult{}, fmt.Errorf("base infra plan is invalid: %w", err)
	}
	brief := strings.TrimSpace(req.Plan.AgentPrompt)
	if brief == "" {
		brief = synthesizeInfraAgentPrompt(req.Plan)
	}
	prompt := buildExternalRepoAgentPrompt(req, brief)
	args, stdoutPath, err := externalRepoAgentInvocation(w, req.RepoPath)
	if err != nil {
		return infra.RefineResult{}, err
	}
	if stdoutPath != "" {
		defer func() {
			_ = os.Remove(stdoutPath)
		}()
	}
	if w.Progress != nil {
		w.Progress("repo agent external runner: provider=%s command=%s", w.Provider, strings.Join(append([]string{w.Command}, args...), " "))
	}
	cmd := exec.CommandContext(ctx, w.Command, args...)
	cmd.Dir = req.RepoPath
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return infra.RefineResult{}, fmt.Errorf("external repo agent failed: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	narrative := strings.TrimSpace(string(out))
	if stdoutPath != "" {
		if b, readErr := os.ReadFile(stdoutPath); readErr == nil && strings.TrimSpace(string(b)) != "" {
			narrative = strings.TrimSpace(string(b))
		}
	}
	if strings.TrimSpace(narrative) == "" {
		narrative = "external repo agent completed direct repo edits"
	}
	return infra.RefineResult{
		Plan:        req.Plan,
		Narrative:   narrative,
		DirectEdits: true,
	}, nil
}

func ensureInfraAgentPrompt(plan *infra.PRPlan) {
	if plan == nil || strings.TrimSpace(plan.AgentPrompt) != "" {
		return
	}
	plan.AgentPrompt = synthesizeInfraAgentPrompt(*plan)
}

func synthesizeInfraAgentPrompt(plan infra.PRPlan) string {
	var b strings.Builder
	b.WriteString("Apply the smallest safe infrastructure-code change for this remediation.\n")
	if strings.TrimSpace(plan.Summary) != "" {
		b.WriteString("\nGoal:\n")
		b.WriteString(strings.TrimSpace(plan.Summary))
		b.WriteString("\n")
	}
	if strings.TrimSpace(plan.PRBody) != "" {
		b.WriteString("\nContext:\n")
		b.WriteString(strings.TrimSpace(plan.PRBody))
		b.WriteString("\n")
	}
	if len(plan.Edits) > 0 {
		b.WriteString("\nStarting edit hints:\n")
		for _, edit := range plan.Edits {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(edit.Path))
			if strings.TrimSpace(edit.Attribute) != "" {
				b.WriteString(" attribute=")
				b.WriteString(strings.TrimSpace(edit.Attribute))
			}
			if strings.TrimSpace(edit.BlockType) != "" {
				b.WriteString(" blockType=")
				b.WriteString(strings.TrimSpace(edit.BlockType))
			}
			b.WriteString("\n")
		}
	}
	if len(plan.Verify.Commands) > 0 {
		b.WriteString("\nValidation commands to preserve:\n")
		for _, cmd := range plan.Verify.Commands {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(cmd))
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func formatInfraApplyNotification(prefix string, result infra.Result) string {
	lines := []string{
		fmt.Sprintf("%s branch=%s", prefix, result.BranchName),
		fmt.Sprintf("commit=%s", result.CommitSHA),
		fmt.Sprintf("pushed=%t", result.Pushed),
	}
	if strings.TrimSpace(result.PullRequestURL) != "" {
		lines = append(lines, "prUrl="+strings.TrimSpace(result.PullRequestURL))
	}
	return strings.Join(lines, "\n")
}

func defaultRepoAgentCommand(provider string) string {
	switch provider {
	case "codex-cli":
		return "codex"
	case "claude-code":
		return "claude"
	default:
		return ""
	}
}

func buildExternalRepoAgentPrompt(req infra.RefineRequest, brief string) string {
	return fmt.Sprintf(`You are an external repository coding agent.

Work only inside the current repository root.
Do not commit, push, open a PR, change branches, or modify files outside this repo.
Do not touch git config, remotes, or credentials.
Apply the smallest safe infrastructure-code change that matches the brief.
After editing files, print a concise plain-text summary of what you changed.

High-level infra plan JSON:
%s

Agent brief:
%s

Repository inventory:
%s
`, mustRepoAgentJSON(req.Plan), brief, req.RepoInventory)
}

func externalRepoAgentInvocation(worker externalRepoWorker, repoPath string) ([]string, string, error) {
	switch worker.Provider {
	case "codex-cli":
		lastMessageFile := filepath.Join(repoPath, ".koc-repo-agent-last-message.txt")
		args := []string{"exec", "--full-auto", "-C", repoPath, "-o", lastMessageFile}
		if strings.TrimSpace(worker.Model) != "" {
			args = append(args, "--model", strings.TrimSpace(worker.Model))
		}
		return args, lastMessageFile, nil
	case "claude-code":
		args := []string{"-p", "--permission-mode", "bypassPermissions", "--output-format", "text"}
		if strings.TrimSpace(worker.Model) != "" {
			args = append(args, "--model", strings.TrimSpace(worker.Model))
		}
		return args, "", nil
	default:
		return nil, "", fmt.Errorf("unsupported external repo agent provider: %q", worker.Provider)
	}
}

func extractRepoAgentPlan(text string) (infra.PRPlan, error) {
	m := repoAgentJSONFenceRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return infra.PRPlan{}, fmt.Errorf("repo agent response did not contain a fenced json InfraPRPlan")
	}
	payload := strings.TrimSpace(m[1])
	if payload == "null" {
		return infra.PRPlan{}, fmt.Errorf("repo agent returned null")
	}
	var p infra.PRPlan
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return infra.PRPlan{}, fmt.Errorf("parse repo agent plan: %w", err)
	}
	return p, nil
}

func stripRepoAgentJSON(text string) string {
	return strings.TrimSpace(repoAgentJSONFenceRe.ReplaceAllString(text, ""))
}

func mergeRepoAgentPlan(base, refined infra.PRPlan) infra.PRPlan {
	merged := refined
	if merged.APIVersion == "" {
		merged.APIVersion = base.APIVersion
	}
	if merged.Kind == "" {
		merged.Kind = base.Kind
	}
	if merged.Backend == "" {
		merged.Backend = base.Backend
	}
	if merged.CreatedAt.IsZero() {
		merged.CreatedAt = base.CreatedAt
	}
	if merged.ApprovalID == "" {
		merged.ApprovalID = base.ApprovalID
	}
	if strings.TrimSpace(merged.Summary) == "" {
		merged.Summary = base.Summary
	}
	if strings.TrimSpace(merged.AgentPrompt) == "" {
		merged.AgentPrompt = base.AgentPrompt
	}
	if strings.TrimSpace(merged.BranchName) == "" {
		merged.BranchName = base.BranchName
	}
	if strings.TrimSpace(merged.CommitMessage) == "" {
		merged.CommitMessage = base.CommitMessage
	}
	if strings.TrimSpace(merged.PRTitle) == "" {
		merged.PRTitle = base.PRTitle
	}
	if strings.TrimSpace(merged.PRBody) == "" {
		merged.PRBody = base.PRBody
	}
	if len(merged.Edits) == 0 {
		merged.Edits = base.Edits
	}
	if len(merged.Verify.Commands) == 0 && len(merged.Verify.Notes) == 0 {
		merged.Verify = base.Verify
	}
	if len(merged.Metadata) == 0 && len(base.Metadata) > 0 {
		merged.Metadata = map[string]string{}
		for k, v := range base.Metadata {
			merged.Metadata[k] = v
		}
	}
	return merged
}

func mustRepoAgentJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}
