package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
)

type compoundPlan struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	CreatedAt  time.Time     `json:"createdAt"`
	ApprovalID string        `json:"approvalId,omitempty"`
	Summary    string        `json:"summary"`
	Live       *exec.Plan    `json:"live,omitempty"`
	Infra      *infra.PRPlan `json:"infra,omitempty"`
}

func (p compoundPlan) Validate() error {
	if p.Kind != "CompoundRemediationPlan" {
		return fmt.Errorf("unsupported compound kind: %q", p.Kind)
	}
	if p.Live == nil && p.Infra == nil {
		return fmt.Errorf("compound plan requires at least one sub-plan")
	}
	if p.Live != nil {
		if err := p.Live.Validate(); err != nil {
			return fmt.Errorf("invalid live sub-plan: %w", err)
		}
	}
	if p.Infra != nil {
		if err := p.Infra.Validate(); err != nil {
			return fmt.Errorf("invalid infra sub-plan: %w", err)
		}
	}
	return nil
}

type compoundPhaseStatus string

const (
	compoundPhasePending   compoundPhaseStatus = "pending"
	compoundPhaseRunning   compoundPhaseStatus = "running"
	compoundPhaseSucceeded compoundPhaseStatus = "succeeded"
	compoundPhaseFailed    compoundPhaseStatus = "failed"
	compoundPhaseSkipped   compoundPhaseStatus = "skipped"
)

type compoundPhaseResult struct {
	Name      string              `json:"name"`
	Status    compoundPhaseStatus `json:"status"`
	Message   string              `json:"message,omitempty"`
	StartedAt time.Time           `json:"startedAt,omitempty"`
	EndedAt   time.Time           `json:"endedAt,omitempty"`
}

type compoundExecutionResult struct {
	ApprovalID string               `json:"approvalId"`
	PlanPath   string               `json:"planPath"`
	Summary    string               `json:"summary"`
	StartedAt  time.Time            `json:"startedAt"`
	EndedAt    time.Time            `json:"endedAt,omitempty"`
	Live       *compoundPhaseResult `json:"live,omitempty"`
	Infra      *compoundPhaseResult `json:"infra,omitempty"`
}

func compoundResultPath(planPath string) string {
	return planPath + ".compound-result.json"
}

func writeCompoundResult(path string, result compoundExecutionResult) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
