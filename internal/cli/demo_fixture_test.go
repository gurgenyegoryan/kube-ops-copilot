package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSuggestGoldenFixtureParsesExecutionPlan(t *testing.T) {
	text := mustReadCLIText(t, cliFixturePath(t, "01-exposed-single-replica", "suggest.golden.txt"))
	plan, err := extractAndValidatePlan(text)
	if err != nil {
		t.Fatalf("extractAndValidatePlan: %v", err)
	}
	if plan == nil {
		t.Fatalf("expected execution plan from golden fixture")
	}
	if plan.Operation.Name != "prometheus-grafana" {
		t.Fatalf("unexpected operation name: %s", plan.Operation.Name)
	}
}

func TestTerraformPRGoldenFixtureParsesInfraPlan(t *testing.T) {
	text := mustReadCLIText(t, cliFixturePath(t, "04-terragrunt-helm-remediation", "terraform-pr.golden.txt"))
	plan, err := extractAndValidateTerraformPRPlan(text)
	if err != nil {
		t.Fatalf("extractAndValidateTerraformPRPlan: %v", err)
	}
	if plan == nil {
		t.Fatalf("expected infra plan from golden fixture")
	}
	if len(plan.Edits) != 1 {
		t.Fatalf("expected exactly one edit, got %d", len(plan.Edits))
	}
	if plan.Edits[0].Path != "helm-charts/monitoring/grafana/values-test.yaml" {
		t.Fatalf("unexpected edit path: %s", plan.Edits[0].Path)
	}
}

func TestRemediateGoldenFixtureParsesExecutionPlan(t *testing.T) {
	text := mustReadCLIText(t, cliFixturePath(t, "07-n8n-approval-live-remediation", "remediate.golden.txt"))
	plan, err := extractAndValidatePlan(text)
	if err != nil {
		t.Fatalf("extractAndValidatePlan: %v", err)
	}
	if plan == nil {
		t.Fatalf("expected execution plan from remediate golden fixture")
	}
	if plan.Operation.Namespace != "payments" || plan.Operation.Name != "payments-api" {
		t.Fatalf("unexpected execution target: %s/%s", plan.Operation.Namespace, plan.Operation.Name)
	}
}

func TestSmartRemediateGoldenFixtureParsesCompoundPlan(t *testing.T) {
	text := mustReadCLIText(t, cliFixturePath(t, "08-smart-remediate-compound", "smart-remediate.golden.txt"))
	plan, err := extractAnyPlan(text)
	if err != nil {
		t.Fatalf("extractAnyPlan: %v", err)
	}
	if plan.Compound == nil {
		t.Fatalf("expected compound plan from smart-remediate golden fixture")
	}
	if plan.Compound.Live.Operation.Name != "payments-api" {
		t.Fatalf("unexpected live plan target: %s", plan.Compound.Live.Operation.Name)
	}
	if plan.Compound.Infra.BranchName != "koc/payments-api-replica-floor" {
		t.Fatalf("unexpected infra branch: %s", plan.Compound.Infra.BranchName)
	}
}

func BenchmarkGoldenPlanExtraction(b *testing.B) {
	suggestText := mustReadCLIText(b, cliFixturePath(b, "01-exposed-single-replica", "suggest.golden.txt"))
	infraText := mustReadCLIText(b, cliFixturePath(b, "04-terragrunt-helm-remediation", "terraform-pr.golden.txt"))
	remediateText := mustReadCLIText(b, cliFixturePath(b, "07-n8n-approval-live-remediation", "remediate.golden.txt"))
	smartText := mustReadCLIText(b, cliFixturePath(b, "08-smart-remediate-compound", "smart-remediate.golden.txt"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := extractAndValidatePlan(suggestText); err != nil {
			b.Fatalf("extractAndValidatePlan: %v", err)
		}
		if _, err := extractAndValidateTerraformPRPlan(infraText); err != nil {
			b.Fatalf("extractAndValidateTerraformPRPlan: %v", err)
		}
		if _, err := extractAndValidatePlan(remediateText); err != nil {
			b.Fatalf("extractAndValidatePlan(remediate): %v", err)
		}
		if _, err := extractAnyPlan(smartText); err != nil {
			b.Fatalf("extractAnyPlan: %v", err)
		}
	}
}

func cliFixturePath(tb testing.TB, scenario, name string) string {
	tb.Helper()
	return filepath.Join("..", "..", "examples", "demo-fixtures", scenario, name)
}

func mustReadCLIText(tb testing.TB, path string) string {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
