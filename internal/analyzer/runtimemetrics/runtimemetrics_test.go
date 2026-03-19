package runtimemetrics

import (
	"strings"
	"testing"
)

func TestScoreRuntimeDetectsHotspotAgainstRequests(t *testing.T) {
	runtime := workloadRuntime{
		Key: workloadKey{
			Namespace: "prod",
			Kind:      "Deployment",
			Name:      "api",
		},
		Pods:               3,
		CPUUsageMilli:      1800,
		CPURequestMilli:    1500,
		MemoryUsageBytes:   1800 * 1024 * 1024,
		MemoryRequestBytes: 2 * 1024 * 1024 * 1024,
	}

	score, reasons := scoreRuntime(runtime)
	if score <= 0 {
		t.Fatalf("expected positive hotspot score, got %d", score)
	}
	if !strings.Contains(strings.Join(reasons, " | "), "CPU usage") {
		t.Fatalf("expected CPU pressure reason, got %v", reasons)
	}
}

func TestScoreRuntimeIgnoresHealthyRequests(t *testing.T) {
	runtime := workloadRuntime{
		Key: workloadKey{
			Namespace: "prod",
			Kind:      "Deployment",
			Name:      "worker",
		},
		Pods:               2,
		CPUUsageMilli:      250,
		CPURequestMilli:    1000,
		MemoryUsageBytes:   256 * 1024 * 1024,
		MemoryRequestBytes: 1024 * 1024 * 1024,
	}

	score, reasons := scoreRuntime(runtime)
	if score != 0 {
		t.Fatalf("expected zero hotspot score, got %d (%v)", score, reasons)
	}
}
