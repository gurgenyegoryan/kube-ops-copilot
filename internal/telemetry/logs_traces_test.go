package telemetry

import "testing"

func TestDetectLokiLabelPair(t *testing.T) {
	ns, pod := detectLokiLabelPair([]string{"cluster", "namespace", "pod", "container"})
	if ns != "namespace" || pod != "pod" {
		t.Fatalf("unexpected label pair: %s %s", ns, pod)
	}
}

func TestDetectLokiLabelPairWithExportedAliases(t *testing.T) {
	ns, pod := detectLokiLabelPair([]string{"cluster", "exported_namespace", "exported_pod", "container"})
	if ns != "exported_namespace" || pod != "exported_pod" {
		t.Fatalf("unexpected label pair: %s %s", ns, pod)
	}
}

func TestJoinProxyPath(t *testing.T) {
	got := joinProxyPath("/api/v1/", "/query")
	if got != "api/v1/query" {
		t.Fatalf("unexpected proxy path: %s", got)
	}
}
