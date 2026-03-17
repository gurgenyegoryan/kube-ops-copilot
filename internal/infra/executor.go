package infra

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Executor struct {
	RepoPath     string
	SkipFmt      bool
	RunValidate  bool
	Push         bool
	OpenPR       bool
	BaseBranch   string
	RequireClean bool
}

type Result struct {
	BranchName       string
	CommitSHA        string
	Pushed           bool
	PullRequestURL   string
	AppliedFiles     []string
	StartedAt        time.Time
	EndedAt          time.Time
	ValidationOutput []string
}

func (e *Executor) Apply(ctx context.Context, plan TerraformPRPlan) (Result, error) {
	if err := plan.Validate(); err != nil {
		return Result{}, err
	}
	repoPath := strings.TrimSpace(e.RepoPath)
	if repoPath == "" {
		return Result{}, fmt.Errorf("repo path is required")
	}
	if st, err := os.Stat(repoPath); err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("repo path is not a directory: %s", repoPath)
	}
	started := time.Now()

	if e.RequireClean {
		clean, out, err := gitClean(ctx, repoPath)
		if err != nil {
			return Result{}, err
		}
		if !clean {
			return Result{}, fmt.Errorf("refusing to edit dirty repo; commit or stash changes first\n%s", out)
		}
	}

	applied, err := applyEdits(repoPath, plan.Edits)
	if err != nil {
		return Result{}, err
	}

	validationOutput := []string{}
	if !e.SkipFmt {
		if out, err := runCmd(ctx, repoPath, "terraform", "fmt", "-recursive"); err != nil {
			return Result{}, fmt.Errorf("terraform fmt failed: %w\n%s", err, out)
		} else if strings.TrimSpace(out) != "" {
			validationOutput = append(validationOutput, out)
		}
	}
	if e.RunValidate {
		if out, err := runCmd(ctx, repoPath, "terraform", "validate"); err != nil {
			return Result{}, fmt.Errorf("terraform validate failed: %w\n%s", err, out)
		} else if strings.TrimSpace(out) != "" {
			validationOutput = append(validationOutput, out)
		}
	}

	if _, err := runCmd(ctx, repoPath, "git", "checkout", "-b", plan.BranchName); err != nil {
		return Result{}, fmt.Errorf("git checkout -b failed: %w", err)
	}
	addArgs := []string{"add"}
	addArgs = append(addArgs, applied...)
	if _, err := runCmd(ctx, repoPath, "git", addArgs...); err != nil {
		return Result{}, fmt.Errorf("git add failed: %w", err)
	}
	if _, err := runCmd(ctx, repoPath, "git", "commit", "-m", plan.CommitMessage); err != nil {
		return Result{}, fmt.Errorf("git commit failed: %w", err)
	}
	sha, err := runCmd(ctx, repoPath, "git", "rev-parse", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("git rev-parse failed: %w", err)
	}

	result := Result{
		BranchName:       plan.BranchName,
		CommitSHA:        strings.TrimSpace(sha),
		AppliedFiles:     applied,
		StartedAt:        started,
		EndedAt:          time.Now(),
		ValidationOutput: validationOutput,
	}

	if e.Push {
		args := []string{"push", "-u", "origin", plan.BranchName}
		if _, err := runCmd(ctx, repoPath, "git", args...); err != nil {
			return Result{}, fmt.Errorf("git push failed: %w", err)
		}
		result.Pushed = true
	}
	if e.OpenPR {
		args := []string{"pr", "create", "--title", plan.PRTitle, "--body", plan.PRBody, "--head", plan.BranchName}
		if strings.TrimSpace(e.BaseBranch) != "" {
			args = append(args, "--base", e.BaseBranch)
		}
		out, err := runCmd(ctx, repoPath, "gh", args...)
		if err != nil {
			return Result{}, fmt.Errorf("gh pr create failed: %w\n%s", err, out)
		}
		result.PullRequestURL = firstURL(out)
	}
	result.EndedAt = time.Now()
	return result, nil
}

func applyEdits(repoPath string, edits []TerraformEdit) ([]string, error) {
	appliedSet := map[string]struct{}{}
	for _, edit := range edits {
		target := filepath.Join(repoPath, filepath.Clean(edit.Path))
		content, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", edit.Path, err)
		}
		src := string(content)
		count := strings.Count(src, edit.Search)
		if count == 0 {
			return nil, fmt.Errorf("edit target not found in %s", edit.Path)
		}
		if count > 1 {
			return nil, fmt.Errorf("edit target matched multiple times in %s; refine snippet", edit.Path)
		}
		out := strings.Replace(src, edit.Search, edit.Replace, 1)
		if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", edit.Path, err)
		}
		appliedSet[edit.Path] = struct{}{}
	}
	files := make([]string, 0, len(appliedSet))
	for path := range appliedSet {
		files = append(files, path)
	}
	return files, nil
}

func gitClean(ctx context.Context, repoPath string) (bool, string, error) {
	out, err := runCmd(ctx, repoPath, "git", "status", "--porcelain")
	if err != nil {
		return false, out, fmt.Errorf("git status failed: %w", err)
	}
	return strings.TrimSpace(out) == "", out, nil
}

func runCmd(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func firstURL(s string) string {
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			return field
		}
	}
	return ""
}
