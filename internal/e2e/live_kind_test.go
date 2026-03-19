//go:build livee2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/cli"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

func TestLiveDiagnoseMarkdownAgainstDisposableTelemetryCluster(t *testing.T) {
	requireLiveE2E(t)

	var out bytes.Buffer
	cmd := cli.NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"diagnose", "--timeout", "90s"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diagnose failed: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"telemetry runtime adapters discovered: timeSeries=1 logs=1 traces=2",
		"time-series backend reachable: vendor=prometheus service=monitoring/prometheus",
		"logs backend reachable: vendor=loki service=observability/loki",
		"traces backend reachable: vendor=jaeger service=observability/jaeger-query",
		"traces backend reachable: vendor=tempo service=observability/tempo",
		"single-replica exposed workload without HPA: payments/payments-api",
		"time-series correlated hotspot: payments/StatefulSet/payments-worker",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected live diagnose output to contain %q, got:\n%s", want, output)
		}
	}
}

func TestLiveDiagnoseJSONAgainstDisposableTelemetryCluster(t *testing.T) {
	requireLiveE2E(t)

	var out bytes.Buffer
	cmd := cli.NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"diagnose", "--output", "json", "--timeout", "90s"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diagnose json failed: %v\n%s", err, out.String())
	}

	var report model.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("decode report json: %v\n%s", err, out.String())
	}
	if report.Assessment.ObservabilityCoverage == "" {
		t.Fatalf("expected observability coverage to be set: %+v", report.Assessment)
	}
	if !containsFinding(report.KeyFindings, "single replica without HPA") {
		t.Fatalf("expected autoscaling posture finding in report: %+v", report.KeyFindings)
	}
	if !containsEvidence(report.Evidence, "time-series correlated hotspot: payments/StatefulSet/payments-worker") {
		t.Fatalf("expected correlated hotspot evidence in report")
	}
	if !containsEvidence(report.Evidence, "traces backend reachable: vendor=tempo service=observability/tempo") {
		t.Fatalf("expected tempo evidence in report")
	}
}

func requireLiveE2E(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("KOC_LIVE_E2E")) == "" {
		t.Skip("set KOC_LIVE_E2E=1 to run live external E2E tests")
	}
	if strings.TrimSpace(os.Getenv("KUBECONFIG")) == "" {
		t.Skip("KUBECONFIG must point to the disposable live test cluster")
	}
	time.Sleep(2 * time.Second)
}

func containsEvidence(evidence []model.Evidence, needle string) bool {
	for _, item := range evidence {
		if strings.Contains(item.Signal, needle) {
			return true
		}
	}
	return false
}

func containsFinding(findings []model.Finding, needle string) bool {
	for _, item := range findings {
		if strings.Contains(strings.ToLower(item.Title), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
