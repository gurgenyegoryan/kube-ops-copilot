package report

import (
	"testing"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

func TestBuildSortsBySeverity(t *testing.T) {
	res := []analyzer.Result{{
		Findings: []model.Finding{
			{Title: "info", Severity: model.SeverityInfo, WhyItMatters: "x"},
			{Title: "critical", Severity: model.SeverityCritical, WhyItMatters: "y"},
		},
	}}
	r := Build(res)
	if len(r.KeyFindings) != 2 {
		t.Fatalf("expected 2 findings")
	}
	if r.KeyFindings[0].Severity != model.SeverityCritical {
		t.Fatalf("expected critical first")
	}
	if r.Assessment.OperationalRisk != model.SeverityCritical {
		t.Fatalf("expected critical operational risk, got %s", r.Assessment.OperationalRisk)
	}
	if r.Assessment.ProductionReadinessScore >= 100 {
		t.Fatalf("expected score to decrease when findings exist")
	}
}
