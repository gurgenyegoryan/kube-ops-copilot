package exec

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type OperationType string

const (
	OpRolloutRestartDeployment OperationType = "rollout_restart_deployment"
)

type Plan struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	CreatedAt  time.Time  `json:"createdAt"`
	ApprovalID string     `json:"approvalId,omitempty"`
	Operation  Operation  `json:"operation"`
	Verify     VerifyPlan `json:"verify"`
}

type Operation struct {
	Type      OperationType `json:"type"`
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Reason    string        `json:"reason,omitempty"`
}

type VerifyPlan struct {
	TimeoutSeconds int `json:"timeoutSeconds"`
}

func LoadPlan(path string) (Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return Plan{}, err
	}
	if p.APIVersion == "" {
		p.APIVersion = "kube-ops-copilot/v1alpha1"
	}
	if p.Kind == "" {
		p.Kind = "ExecutionPlan"
	}
	return p, nil
}

func (p Plan) Validate() error {
	if p.Operation.Type == "" {
		return fmt.Errorf("plan.operation.type is required")
	}
	if p.Operation.Namespace == "" {
		return fmt.Errorf("plan.operation.namespace is required")
	}
	if p.Operation.Name == "" {
		return fmt.Errorf("plan.operation.name is required")
	}
	switch p.Operation.Type {
	case OpRolloutRestartDeployment:
		return nil
	default:
		return fmt.Errorf("unsupported operation type: %q", p.Operation.Type)
	}
}
