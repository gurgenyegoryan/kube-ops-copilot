package analyzer

import (
	"context"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

type Analyzer interface {
	Name() string
	Run(ctx context.Context) (Result, error)
}

type Result struct {
	Findings      []model.Finding
	Evidence      []model.Evidence
	Hypotheses    []model.Hypothesis
	Recommended   model.Actions
	ExecutionPlan *model.ExecutionPlan
	Unknowns      []string
	HiddenRisks   []string
}
