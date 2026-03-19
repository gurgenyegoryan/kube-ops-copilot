package workloadrisk

import (
	"strings"
	"testing"
)

func TestScoreProfileFlagsCompoundExposureRisk(t *testing.T) {
	profile := workloadProfile{
		Key: workloadKey{
			Namespace: "gosell-test",
			Kind:      "Deployment",
			Name:      "prometheus-grafana",
		},
		DesiredReplicas:  1,
		ReadyReplicas:    1,
		ServiceCount:     1,
		ExternalServices: 1,
		ReadyEndpoints:   1,
		MissingReadiness: 1,
		MissingLiveness:  1,
		MissingRequests:  1,
		MissingLimits:    1,
	}

	score, reasons := scoreProfile(profile)
	if score < 40 {
		t.Fatalf("expected strong compound-risk score, got %d", score)
	}

	joined := strings.Join(reasons, " | ")
	for _, want := range []string{
		"external traffic depends on a single replica",
		"externally exposed without HPA",
		"missing readiness probes",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected reasons to contain %q, got %q", want, joined)
		}
	}
}

func TestScoreProfileHealthyWorkloadIsLowRisk(t *testing.T) {
	profile := workloadProfile{
		Key: workloadKey{
			Namespace: "prod",
			Kind:      "Deployment",
			Name:      "api",
		},
		DesiredReplicas:  3,
		ReadyReplicas:    3,
		ServiceCount:     1,
		ExternalServices: 0,
		ReadyEndpoints:   3,
		HasHPA:           true,
		HasPDB:           true,
	}

	score, reasons := scoreProfile(profile)
	if score != 0 {
		t.Fatalf("expected zero score for healthy profile, got %d (%v)", score, reasons)
	}
}
