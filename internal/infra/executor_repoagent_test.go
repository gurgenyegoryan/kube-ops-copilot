package infra

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRepoWorker struct {
	called      bool
	plan        PRPlan
	directEdits bool
	repoEdit    func(repo string) error
	repoPath    string
	mode        RepoAgentExecutionMode
}

func (f *fakeRepoWorker) Refine(_ context.Context, req RefineRequest) (RefineResult, error) {
	f.called = true
	f.repoPath = req.RepoPath
	if f.repoEdit != nil {
		if err := f.repoEdit(req.RepoPath); err != nil {
			return RefineResult{}, err
		}
	}
	return RefineResult{Plan: f.plan, Narrative: "repo worker refined plan", DirectEdits: f.directEdits}, nil
}

func (f *fakeRepoWorker) ExecutionMode() RepoAgentExecutionMode {
	return f.mode
}

func TestExecutorUsesRepoAgentPlan(t *testing.T) {
	repo := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, string(out))
		}
	}

	mustWrite("charts/analytics-service/values-stage.yaml", "replicaCount: 1\n")
	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Repo Agent Test")
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	basePlan := PRPlan{
		APIVersion:    "kube-ops-copilot/v1alpha1",
		Kind:          "InfraPRPlan",
		Backend:       BackendTerragrunt,
		CreatedAt:     time.Now().UTC(),
		Summary:       "Increase analytics replicas",
		BranchName:    "base/branch",
		CommitMessage: "base commit",
		PRTitle:       "Base title",
		PRBody:        "Base body",
		Edits: []Edit{{
			Type:    EditTypeSearchReplace,
			Path:    "charts/analytics-service/values-stage.yaml",
			Search:  "replicaCount: 1",
			Replace: "replicaCount: 2",
		}},
		Verify: Verify{Commands: []string{"terragrunt hclfmt"}},
	}
	worker := &fakeRepoWorker{plan: PRPlan{
		APIVersion:    basePlan.APIVersion,
		Kind:          basePlan.Kind,
		Backend:       basePlan.Backend,
		CreatedAt:     basePlan.CreatedAt,
		Summary:       basePlan.Summary,
		AgentPrompt:   "Refined by repo worker",
		BranchName:    "repo-agent/branch",
		CommitMessage: "repo agent commit",
		PRTitle:       "Repo agent title",
		PRBody:        "Repo agent body",
		Edits: []Edit{{
			Type:    EditTypeSearchReplace,
			Path:    "charts/analytics-service/values-stage.yaml",
			Search:  "replicaCount: 1",
			Replace: "replicaCount: 3",
		}},
		Verify: basePlan.Verify,
	}}

	ex := Executor{
		RepoPath:     repo,
		SkipFmt:      true,
		RunValidate:  false,
		Push:         false,
		OpenPR:       false,
		RequireClean: false,
		RepoAgent:    worker,
	}
	result, err := ex.Apply(context.Background(), basePlan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !worker.called {
		t.Fatalf("expected repo worker to be called")
	}
	if result.BranchName != "repo-agent/branch" {
		t.Fatalf("expected refined branch, got %q", result.BranchName)
	}
	body, err := os.ReadFile(filepath.Join(repo, "charts/analytics-service/values-stage.yaml"))
	if err != nil {
		t.Fatalf("read values file: %v", err)
	}
	if !strings.Contains(string(body), "replicaCount: 3") {
		t.Fatalf("expected refined edit to be applied, got:\n%s", string(body))
	}
}

func TestExecutorUsesRepoAgentDirectEdits(t *testing.T) {
	repo := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, string(out))
		}
	}

	mustWrite("charts/analytics-service/values-stage.yaml", "replicaCount: 1\n")
	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Repo Agent Test")
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	plan := PRPlan{
		APIVersion:    "kube-ops-copilot/v1alpha1",
		Kind:          "InfraPRPlan",
		Backend:       BackendTerragrunt,
		CreatedAt:     time.Now().UTC(),
		Summary:       "Increase analytics replicas",
		BranchName:    "repo-agent/direct",
		CommitMessage: "repo agent direct commit",
		PRTitle:       "Direct repo agent edit",
		PRBody:        "Direct repo agent body",
		Edits: []Edit{{
			Type:    EditTypeSearchReplace,
			Path:    "charts/analytics-service/values-stage.yaml",
			Search:  "replicaCount: 1",
			Replace: "replicaCount: 2",
		}},
		Verify: Verify{Commands: []string{"terragrunt hclfmt"}},
	}

	worker := &fakeRepoWorker{
		plan:        plan,
		directEdits: true,
		repoEdit: func(repo string) error {
			return os.WriteFile(filepath.Join(repo, "charts/analytics-service/values-stage.yaml"), []byte("replicaCount: 5\n"), 0o644)
		},
	}
	ex := Executor{
		RepoPath:     repo,
		SkipFmt:      true,
		RunValidate:  false,
		Push:         false,
		OpenPR:       false,
		RequireClean: false,
		RepoAgent:    worker,
	}
	result, err := ex.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !worker.called {
		t.Fatalf("expected repo worker to be called")
	}
	if result.BranchName != "repo-agent/direct" {
		t.Fatalf("expected direct-edit branch, got %q", result.BranchName)
	}
	body, err := os.ReadFile(filepath.Join(repo, "charts/analytics-service/values-stage.yaml"))
	if err != nil {
		t.Fatalf("read values file: %v", err)
	}
	if !strings.Contains(string(body), "replicaCount: 5") {
		t.Fatalf("expected direct repo edit to be preserved, got:\n%s", string(body))
	}
}

func TestExecutorUsesRepoAgentDirectEditsInIsolatedWorktree(t *testing.T) {
	repo := t.TempDir()
	mustWrite := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, string(out))
		}
		return string(out)
	}

	mustWrite("charts/analytics-service/values-stage.yaml", "replicaCount: 1\n")
	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "Repo Agent Test")
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	plan := PRPlan{
		APIVersion:    "kube-ops-copilot/v1alpha1",
		Kind:          "InfraPRPlan",
		Backend:       BackendTerragrunt,
		CreatedAt:     time.Now().UTC(),
		Summary:       "Increase analytics replicas",
		BranchName:    "repo-agent/direct-isolated",
		CommitMessage: "repo agent isolated direct commit",
		PRTitle:       "Direct repo agent isolated edit",
		PRBody:        "Direct repo agent isolated body",
		Edits: []Edit{{
			Type:    EditTypeSearchReplace,
			Path:    "charts/analytics-service/values-stage.yaml",
			Search:  "replicaCount: 1",
			Replace: "replicaCount: 2",
		}},
		Verify: Verify{Commands: []string{"terragrunt hclfmt"}},
	}

	worker := &fakeRepoWorker{
		plan:        plan,
		directEdits: true,
		mode:        RepoAgentExecutionModeIsolatedWorktree,
		repoEdit: func(repoPath string) error {
			return os.WriteFile(filepath.Join(repoPath, "charts/analytics-service/values-stage.yaml"), []byte("replicaCount: 7\n"), 0o644)
		},
	}
	var progressLog []string
	ex := Executor{
		RepoPath:     repo,
		SkipFmt:      true,
		RunValidate:  false,
		Push:         false,
		OpenPR:       false,
		RequireClean: false,
		RepoAgent:    worker,
		Progress: func(format string, args ...any) {
			progressLog = append(progressLog, fmt.Sprintf(format, args...))
		},
	}
	result, err := ex.Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !worker.called {
		t.Fatalf("expected repo worker to be called")
	}
	if worker.repoPath == "" || worker.repoPath == repo {
		t.Fatalf("expected repo agent to receive isolated worktree path, got %q", worker.repoPath)
	}
	if _, err := os.Stat(worker.repoPath); !os.IsNotExist(err) {
		t.Fatalf("expected isolated worktree to be cleaned up, stat err=%v", err)
	}
	if result.BranchName != "repo-agent/direct-isolated" {
		t.Fatalf("expected isolated branch, got %q", result.BranchName)
	}
	body, err := os.ReadFile(filepath.Join(repo, "charts/analytics-service/values-stage.yaml"))
	if err != nil {
		t.Fatalf("read base repo values file: %v", err)
	}
	if !strings.Contains(string(body), "replicaCount: 1") {
		t.Fatalf("expected base repo working tree to stay unchanged, got:\n%s", string(body))
	}
	show := run("git", "show", "repo-agent/direct-isolated:charts/analytics-service/values-stage.yaml")
	if !strings.Contains(show, "replicaCount: 7") {
		t.Fatalf("expected branch commit to contain isolated repo edit, got:\n%s", show)
	}
	joinedProgress := strings.Join(progressLog, "\n")
	if !strings.Contains(joinedProgress, "creating isolated repo-agent worktree") {
		t.Fatalf("expected isolated worktree creation progress, got:\n%s", joinedProgress)
	}
	if !strings.Contains(joinedProgress, "preparing isolated repo-agent branch repo-agent/direct-isolated") {
		t.Fatalf("expected isolated branch preparation progress, got:\n%s", joinedProgress)
	}
	if !strings.Contains(joinedProgress, "cleaning isolated repo-agent worktree") {
		t.Fatalf("expected isolated worktree cleanup progress, got:\n%s", joinedProgress)
	}
	if !strings.Contains(joinedProgress, "cleaned isolated repo-agent worktree") {
		t.Fatalf("expected isolated worktree cleaned progress, got:\n%s", joinedProgress)
	}
}
