package timeseriespressure

import (
	"strings"
	"testing"
)

func TestWorkloadForMetricAcceptsAliasLabels(t *testing.T) {
	key := workloadKey{Namespace: "prod", Kind: "Deployment", Name: "api"}
	podOwners := map[string]workloadKey{
		namespacedName("prod", "api-7f8d9"): key,
	}

	got := workloadForMetric(map[string]string{
		"exported_namespace": "prod",
		"exported_pod":       "api-7f8d9",
	}, podOwners)

	if got != key {
		t.Fatalf("expected alias labels to resolve owner %+v, got %+v", key, got)
	}
}

func TestPrometheusExpressionsIncludeAliasFallbacks(t *testing.T) {
	joined := strings.Join(restartExpressions(), "\n")
	for _, want := range []string{
		"sum by (namespace,pod)",
		"sum by (namespace,pod_name)",
		"sum by (exported_namespace,exported_pod)",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected restart expressions to contain %q", want)
		}
	}
}

func TestScoreTrendCorrelatesRuntimeAndStructuralRisk(t *testing.T) {
	trend := workloadTrend{
		Key:             workloadKey{Namespace: "gosell-test", Kind: "Deployment", Name: "grafana"},
		RestartIncrease: 6,
		CPUPeak:         1.6,
		MemoryPeak:      1.5 * 1024 * 1024 * 1024,
	}
	profile := structuralProfile{
		Key:              workloadKey{Namespace: "gosell-test", Kind: "Deployment", Name: "grafana"},
		DesiredReplicas:  1,
		ServiceCount:     1,
		ExternalServices: 1,
		HasHPA:           false,
		MissingReadiness: 1,
	}

	score, reasons := scoreTrend(trend, profile)
	if score < 40 {
		t.Fatalf("expected strong correlation score, got %d", score)
	}
	joined := strings.Join(reasons, " | ")
	for _, want := range []string{
		"24h restart increase",
		"externally exposed single replica",
		"restart growth overlaps with external single-replica exposure",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected reasons to contain %q, got %q", want, joined)
		}
	}
}

func TestScoreTrendIgnoresColdHealthyWorkload(t *testing.T) {
	trend := workloadTrend{
		Key:             workloadKey{Namespace: "prod", Kind: "Deployment", Name: "worker"},
		RestartIncrease: 0,
		CPUPeak:         0.1,
		MemoryPeak:      128 * 1024 * 1024,
	}
	profile := structuralProfile{
		Key:             workloadKey{Namespace: "prod", Kind: "Deployment", Name: "worker"},
		DesiredReplicas: 3,
		ServiceCount:    0,
		HasHPA:          true,
		HasPDB:          true,
	}

	score, reasons := scoreTrend(trend, profile)
	if score != 0 {
		t.Fatalf("expected zero score, got %d (%v)", score, reasons)
	}
}
