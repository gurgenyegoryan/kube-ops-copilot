package infra

import "context"

type PlanRefiner interface {
	Refine(ctx context.Context, req RefineRequest) (RefineResult, error)
}

type RepoAgentExecutionMode string

const (
	RepoAgentExecutionModeInPlace          RepoAgentExecutionMode = "in_place"
	RepoAgentExecutionModeIsolatedWorktree RepoAgentExecutionMode = "isolated_worktree"
)

type ExecutionModeAwareRefiner interface {
	ExecutionMode() RepoAgentExecutionMode
}

func RefinerExecutionModeOf(refiner PlanRefiner) RepoAgentExecutionMode {
	if aware, ok := refiner.(ExecutionModeAwareRefiner); ok {
		if mode := aware.ExecutionMode(); mode != "" {
			return mode
		}
	}
	return RepoAgentExecutionModeInPlace
}

type RefineRequest struct {
	RepoPath      string
	RepoInventory string
	Plan          PRPlan
}

type RefineResult struct {
	Plan        PRPlan
	Narrative   string
	DirectEdits bool
}
