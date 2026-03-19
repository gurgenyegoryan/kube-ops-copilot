package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/notify"
)

func TestDiagnoseCommandFlowWithProgressAndNotification(t *testing.T) {
	restore := stubCLIDeps(t)
	defer restore()

	rec := &recordingNotifier{}
	cliNewNotifierFromEnv = func() notify.Notifier { return rec }
	cliNewKubeClient = func(cfg kube.Config) (*kube.Client, error) {
		return &kube.Client{WarningCollector: kube.NewWarningCollector()}, nil
	}
	cliDefaultAnalyzers = func(ctx context.Context, client *kube.Client, includeSystemNamespaces bool, eventsSince time.Duration) []analyzer.Analyzer {
		return []analyzer.Analyzer{
			staticAnalyzer{
				name: "synthetic-health",
				result: analyzer.Result{
					Findings: []model.Finding{{
						Title:                 "Synthetic exposed single-replica workload",
						Severity:              model.SeverityHigh,
						Urgency:               model.UrgencyImmediate,
						Confidence:            model.ConfidenceHigh,
						AffectedScope:         "payments/deployments",
						WhyItMatters:          "A single replica behind external traffic is a hard SPOF.",
						AutomationSuitability: model.SuitabilityAdvisoryOnly,
					}},
					Evidence:    []model.Evidence{{Signal: "synthetic evidence: exposed single replica without PDB"}},
					HiddenRisks: []string{"Synthetic hidden risk for CLI flow integration coverage."},
				},
			},
		}
	}

	var out bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"diagnose", "--notify", "--timeout", "2s"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diagnose command failed: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"[progress] diagnose: connecting to cluster",
		"[progress] diagnose: running analyzers",
		"[event] diagnose: analyzer 1/1: synthetic-health",
		"[event] diagnose: completed analyzer 1/1: synthetic-health",
		"### Executive Summary",
		"Synthetic exposed single-replica workload",
		"[progress] diagnose: sending notification",
		"[done] diagnose: notification sent",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected diagnose output to contain %q, got:\n%s", want, output)
		}
	}
	if len(rec.Messages) != 1 {
		t.Fatalf("expected one diagnose notification, got %+v", rec.Messages)
	}
	if rec.Messages[0].Title != "kube-ops-copilot diagnose" {
		t.Fatalf("unexpected diagnose notification: %+v", rec.Messages[0])
	}
}

func TestSuggestCommandFlowWithPlanOutProgressAndNotification(t *testing.T) {
	restore := stubCLIDeps(t)
	defer restore()

	rec := &recordingNotifier{}
	cliNewNotifierFromEnv = func() notify.Notifier { return rec }
	cliNewKubeClient = func(cfg kube.Config) (*kube.Client, error) {
		return &kube.Client{WarningCollector: kube.NewWarningCollector()}, nil
	}
	cliDefaultAnalyzers = func(ctx context.Context, client *kube.Client, includeSystemNamespaces bool, eventsSince time.Duration) []analyzer.Analyzer {
		return []analyzer.Analyzer{
			staticAnalyzer{
				name: "synthetic-risk",
				result: analyzer.Result{
					Findings: []model.Finding{{
						Title:                 "Synthetic runtime hotspot",
						Severity:              model.SeverityHigh,
						Urgency:               model.UrgencyToday,
						Confidence:            model.ConfidenceHigh,
						AffectedScope:         "payments/runtime",
						WhyItMatters:          "Live CPU pressure is already above request baseline.",
						AutomationSuitability: model.SuitabilityAdvisoryOnly,
					}},
					Evidence: []model.Evidence{{Signal: "synthetic evidence: cpu usage is 130% of requests"}},
				},
			},
		}
	}
	cliNewLLMClient = func(cfg llm.Config) (llm.Client, error) {
		return fakeLLMClient{Text: strings.TrimSpace(strings.Join([]string{
			"## Triage",
			"",
			"Prioritize `payments/payments-api` because it is already running hot against declared CPU requests.",
			"",
			"Next read-only verification:",
			"- kubectl top pods -n payments --containers",
			"",
			"Recommended remediation:",
			"Scale the deployment from 1 to 2 replicas while validating the request baseline.",
			"",
			"```json",
			"{",
			`  "apiVersion": "kube-ops-copilot/v1alpha1",`,
			`  "kind": "ExecutionPlan",`,
			`  "createdAt": "2026-03-19T00:00:00Z",`,
			`  "approvalId": "",`,
			`  "operation": {`,
			`    "type": "scale_deployment",`,
			`    "namespace": "payments",`,
			`    "name": "payments-api",`,
			`    "replicas": 2,`,
			`    "reason": "Reduce immediate risk from observed CPU pressure on a single replica."`,
			`  },`,
			`  "verify": { "timeoutSeconds": 180 }`,
			"}",
			"```",
		}, "\n"))}, nil
	}

	planPath := filepath.Join(t.TempDir(), "suggest-plan.json")
	var out bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"suggest",
		"--llm-provider", "openai",
		"--llm-model", "gpt-5.2",
		"--plan-out", planPath,
		"--notify",
		"--timeout", "2s",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("suggest command failed: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"[progress] suggest: initializing LLM client",
		"[progress] suggest: connecting to cluster",
		"[progress] suggest: running analyzers",
		"[progress] suggest: asking LLM for operator suggestions",
		"## Triage",
		"(wrote executable plan to " + planPath + ")",
		"[progress] suggest: sending notification",
		"[done] suggest: notification sent",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected suggest output to contain %q, got:\n%s", want, output)
		}
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("expected suggest plan file to exist: %v", err)
	}
	if len(rec.Messages) != 1 || rec.Messages[0].Title != "kube-ops-copilot suggest" {
		t.Fatalf("unexpected suggest notification messages: %+v", rec.Messages)
	}
	if strings.Contains(output, `"kind": "ExecutionPlan"`) {
		t.Fatalf("expected stripped markdown output without raw json block, got:\n%s", output)
	}
}

func TestTerraformPRCommandFlowWithProgressPlanAndNotification(t *testing.T) {
	restore := stubCLIDeps(t)
	defer restore()

	rec := &recordingNotifier{}
	cliNewNotifierFromEnv = func() notify.Notifier { return rec }
	cliNewKubeClient = func(cfg kube.Config) (*kube.Client, error) {
		return &kube.Client{WarningCollector: kube.NewWarningCollector()}, nil
	}
	cliDefaultAnalyzers = func(ctx context.Context, client *kube.Client, includeSystemNamespaces bool, eventsSince time.Duration) []analyzer.Analyzer {
		return []analyzer.Analyzer{
			staticAnalyzer{
				name: "synthetic-terraform-risk",
				result: analyzer.Result{
					Findings: []model.Finding{{
						Title:                 "Synthetic replica floor gap",
						Severity:              model.SeverityHigh,
						Urgency:               model.UrgencyToday,
						Confidence:            model.ConfidenceHigh,
						AffectedScope:         "payments/terraform",
						WhyItMatters:          "Single replica is externally exposed.",
						AutomationSuitability: model.SuitabilityApprovalGatedChange,
					}},
				},
			},
		}
	}
	cliBuildTerraformRepoInventory = func(repoPath, reportJSON string, maxFiles, maxBytes int) (string, error) {
		return "FILE main.tf\n---\nresource \"example\" \"payments\" {\n  replicas = 1\n}\n", nil
	}
	cliNewLLMClient = func(cfg llm.Config) (llm.Client, error) {
		return fakeLLMClient{Text: strings.TrimSpace(strings.Join([]string{
			"Terraform remediation is the durable fix path because the replica floor belongs in IaC.",
			"",
			"```json",
			"{",
			`  "apiVersion": "kube-ops-copilot/v1alpha1",`,
			`  "kind": "InfraPRPlan",`,
			`  "backend": "terraform",`,
			`  "createdAt": "2026-03-19T00:00:00Z",`,
			`  "approvalId": "",`,
			`  "summary": "Persist payments-api replica floor",`,
			`  "branchName": "koc/payments-api-replica-floor",`,
			`  "commitMessage": "Set payments-api replicas to 2",`,
			`  "prTitle": "Set payments-api replicas to 2",`,
			`  "prBody": "Codifies the higher replica floor in Terraform.",`,
			`  "edits": [`,
			`    {`,
			`      "type": "search_replace",`,
			`      "path": "main.tf",`,
			`      "search": "replicas = 1",`,
			`      "replace": "replicas = 2"`,
			`    }`,
			`  ],`,
			`  "verify": {`,
			`    "commands": ["terraform validate"],`,
			`    "notes": ["Review rollout after the next apply."]`,
			`  },`,
			`  "metadata": {`,
			`    "risk_note": "Low-medium rollout risk."`,
			`  }`,
			"}",
			"```",
		}, "\n"))}, nil
	}

	repoPath := t.TempDir()
	planPath := filepath.Join(t.TempDir(), "terraform-plan.json")
	var out bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"terraform-pr",
		"--llm-provider", "openai",
		"--llm-model", "gpt-5.2",
		"--infra-repo-path", repoPath,
		"--plan-out", planPath,
		"--approval-provider", "manual",
		"--notify",
		"--timeout", "2s",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("terraform-pr command failed: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"[progress] terraform-pr: initializing LLM client",
		"[progress] terraform-pr: connecting to cluster",
		"[progress] terraform-pr: running analyzers",
		"[progress] terraform-pr: building infrastructure repository inventory",
		"infrastructure repo inventory snapshot:",
		"[progress] terraform-pr: asking LLM for infrastructure PR plan",
		"[progress] terraform-pr: writing plan file",
		"[progress] terraform-pr: requesting approval",
		"approval requested: provider=manual",
		"plan written: " + planPath,
		"[done] terraform-pr: approval recorded; repo changes not applied because --apply was not requested",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected terraform-pr output to contain %q, got:\n%s", want, output)
		}
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("expected terraform-pr plan file to exist: %v", err)
	}
	if len(rec.Messages) != 1 || rec.Messages[0].Title != "kube-ops-copilot terraform-pr (approval requested)" {
		t.Fatalf("unexpected terraform-pr notification messages: %+v", rec.Messages)
	}
}

type staticAnalyzer struct {
	name   string
	result analyzer.Result
}

func (a staticAnalyzer) Name() string { return a.name }

func (a staticAnalyzer) Run(ctx context.Context) (analyzer.Result, error) {
	_ = ctx
	return a.result, nil
}

type fakeLLMClient struct {
	Text string
}

func (f fakeLLMClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	_ = req
	return llm.Response{Text: f.Text}, nil
}

func stubCLIDeps(t *testing.T) func() {
	t.Helper()
	prevKube := cliNewKubeClient
	prevNotifier := cliNewNotifierFromEnv
	prevLLM := cliNewLLMClient
	prevAnalyzers := cliDefaultAnalyzers
	prevInventory := cliBuildTerraformRepoInventory
	return func() {
		cliNewKubeClient = prevKube
		cliNewNotifierFromEnv = prevNotifier
		cliNewLLMClient = prevLLM
		cliDefaultAnalyzers = prevAnalyzers
		cliBuildTerraformRepoInventory = prevInventory
	}
}
