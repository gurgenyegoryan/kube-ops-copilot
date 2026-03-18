package infra

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type Status struct {
	PlanPath       string `json:"planPath"`
	ResultPath     string `json:"resultPath,omitempty"`
	Backend        string `json:"backend"`
	BranchName     string `json:"branchName"`
	BranchExists   bool   `json:"branchExists"`
	DirtyRepo      bool   `json:"dirtyRepo"`
	CommitSHA      string `json:"commitSHA,omitempty"`
	PullRequestURL string `json:"pullRequestURL,omitempty"`
}

func Inspect(ctx context.Context, repoPath, planPath string) (Status, error) {
	plan, err := LoadPlan(planPath)
	if err != nil {
		return Status{}, err
	}
	st := Status{
		PlanPath:   planPath,
		ResultPath: ResultPathForPlan(planPath),
		Backend:    string(plan.Backend),
		BranchName: plan.BranchName,
	}

	if repoPath != "" {
		if _, err := os.Stat(repoPath); err != nil {
			return Status{}, err
		}
		if out, err := runCmd(ctx, repoPath, "git", "rev-parse", "--verify", plan.BranchName); err == nil {
			st.BranchExists = true
			st.CommitSHA = strings.TrimSpace(out)
		}
		if clean, _, err := gitClean(ctx, repoPath); err == nil {
			st.DirtyRepo = !clean
		}
	}

	if result, err := LoadResult(st.ResultPath); err == nil {
		if st.CommitSHA == "" {
			st.CommitSHA = result.CommitSHA
		}
		if result.PullRequestURL != "" {
			st.PullRequestURL = result.PullRequestURL
		}
	}
	return st, nil
}

func (s Status) Summary() string {
	return fmt.Sprintf("backend=%s branch=%s branchExists=%t dirtyRepo=%t commit=%s pr=%s", s.Backend, s.BranchName, s.BranchExists, s.DirtyRepo, s.CommitSHA, s.PullRequestURL)
}
