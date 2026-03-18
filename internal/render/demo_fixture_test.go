package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

func TestDemoFixtureDiagnoseGolden(t *testing.T) {
	reportPath := demoFixturePath(t, "01-exposed-single-replica", "diagnose-report.json")
	goldenPath := demoFixturePath(t, "01-exposed-single-replica", "diagnose.golden.md")

	var rep model.Report
	mustReadJSON(t, reportPath, &rep)
	got := Markdown(rep)
	want := mustReadText(t, goldenPath)
	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", reportPath, got, want)
	}
}

func BenchmarkMarkdownDemoFixtures(b *testing.B) {
	paths := []string{
		demoFixturePath(b, "01-exposed-single-replica", "diagnose-report.json"),
		demoFixturePath(b, "02-telemetry-blind-cluster", "diagnose-report.json"),
		demoFixturePath(b, "03-pending-pvc-storage-risk", "diagnose-report.json"),
		demoFixturePath(b, "04-terragrunt-helm-remediation", "diagnose-report.json"),
	}
	reports := make([]model.Report, 0, len(paths))
	for _, path := range paths {
		var rep model.Report
		mustReadJSON(b, path, &rep)
		reports = append(reports, rep)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, rep := range reports {
			_ = Markdown(rep)
		}
	}
}

func demoFixturePath(tb testing.TB, scenario, name string) string {
	tb.Helper()
	return filepath.Join("..", "..", "examples", "demo-fixtures", scenario, name)
}

func mustReadText(tb testing.TB, path string) string {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustReadJSON(tb testing.TB, path string, out any) {
	tb.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		tb.Fatalf("unmarshal %s: %v", path, err)
	}
}
