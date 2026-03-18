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

func BenchmarkGoldenPlanExtraction(b *testing.B) {
	suggestText := mustReadCLIText(b, cliFixturePath(b, "01-exposed-single-replica", "suggest.golden.txt"))
	infraText := mustReadCLIText(b, cliFixturePath(b, "04-terragrunt-helm-remediation", "terraform-pr.golden.txt"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := extractAndValidatePlan(suggestText); err != nil {
			b.Fatalf("extractAndValidatePlan: %v", err)
		}
		if _, err := extractAndValidateTerraformPRPlan(infraText); err != nil {
			b.Fatalf("extractAndValidateTerraformPRPlan: %v", err)
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
