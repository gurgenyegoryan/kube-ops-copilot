package cli

import (
	"os"
	"testing"
)

func TestExecuteRefusesWithoutPlan(t *testing.T) {
	cmd := NewExecuteCmd()
	cmd.SetArgs([]string{"--approve", "--approval-id", "TICKET-1"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected refusal without plan")
	}
}

func TestExecuteRefusesWithoutApproval(t *testing.T) {
	f, err := os.CreateTemp("", "plan-*.json")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()

	_, _ = f.WriteString(`{"apiVersion":"kube-ops-copilot/v1alpha1","kind":"ExecutionPlan","createdAt":"2026-03-16T00:00:00Z","operation":{"type":"rollout_restart_deployment","namespace":"default","name":"x"},"verify":{"timeoutSeconds":1}}`)
	_ = f.Close()

	cmd := NewExecuteCmd()
	cmd.SetArgs([]string{"--plan", f.Name()})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected refusal without approval")
	}
}
