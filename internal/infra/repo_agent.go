package infra

import "context"

type PlanRefiner interface {
	Refine(ctx context.Context, req RefineRequest) (RefineResult, error)
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
