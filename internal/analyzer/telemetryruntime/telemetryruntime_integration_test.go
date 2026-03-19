package telemetryruntime

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/testutil/fakekube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAnalyzerRunCorrelatesDiscoveredTelemetryBackends(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/services":
			fakekube.WriteJSON(w, http.StatusOK, corev1.ServiceList{
				Items: []corev1.Service{
					service("monitoring", "thanos-query", "http", 9090),
					service("observability", "loki", "http", 3100),
					service("observability", "jaeger-query", "http", 16686),
					service("observability", "opensearch", "http", 9200),
				},
			})
		case strings.Contains(r.URL.Path, "thanos-query") && strings.HasSuffix(r.URL.Path, "/status/buildinfo"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{"status": "success", "data": map[string]any{"version": "2.51.0"}})
		case strings.Contains(r.URL.Path, "thanos-query") && strings.HasSuffix(r.URL.Path, "/query"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []any{
						map[string]any{"metric": map[string]any{"namespace": "payments", "pod": "payments-api-7f8d9"}, "value": []any{1710000000, "1"}},
					},
				},
			})
		case strings.Contains(r.URL.Path, "thanos-query") && strings.HasSuffix(r.URL.Path, "/stores"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{"status": "success", "data": []any{map[string]any{"name": "store-a"}, map[string]any{"name": "store-b"}}})
		case strings.Contains(r.URL.Path, "loki") && strings.HasSuffix(r.URL.Path, "/labels"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{"status": "success", "data": []string{"cluster", "exported_namespace", "exported_pod", "container"}})
		case strings.Contains(r.URL.Path, "loki") && strings.HasSuffix(r.URL.Path, "/query"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{
				"status": "success",
				"data": map[string]any{
					"resultType": "vector",
					"result": []any{
						map[string]any{"metric": map[string]any{"exported_namespace": "payments", "exported_pod": "payments-api-7f8d9"}, "value": []any{1710000000, "19"}},
					},
				},
			})
		case strings.Contains(r.URL.Path, "jaeger-query") && strings.HasSuffix(r.URL.Path, "/services"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{"data": []string{"checkout", "payments"}})
		case strings.Contains(r.URL.Path, "jaeger-query") && strings.HasSuffix(r.URL.Path, "/services/checkout/operations"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{"data": []string{"GET /healthz", "POST /checkout"}})
		case strings.Contains(r.URL.Path, "opensearch") && strings.HasSuffix(r.URL.Path, "/_cluster/health"):
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{"status": "yellow"})
		default:
			http.NotFound(w, r)
		}
	})

	client, closeFn, err := fakekube.NewClient(handler)
	if err != nil {
		t.Fatalf("new fake kube client: %v", err)
	}
	defer closeFn()

	an := Analyzer{
		Client: client,
		Inventory: capability.Inventory{
			"time_series_metrics": capability.Detected,
			"logs_backend":        capability.Detected,
			"traces_backend":      capability.Detected,
		},
	}

	res, err := an.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !containsEvidence(res.Evidence, "telemetry runtime adapters discovered: timeSeries=1 logs=2 traces=1") {
		t.Fatalf("missing adapter discovery evidence: %+v", res.Evidence)
	}
	if !containsEvidence(res.Evidence, "time-series backend reachable: vendor=thanos service=monitoring/thanos-query") {
		t.Fatalf("missing time-series evidence: %+v", res.Evidence)
	}
	if !containsEvidence(res.Evidence, "logs error hotspot: payments/payments-api-7f8d9 value=19 backend=loki") {
		t.Fatalf("missing logs hotspot evidence: %+v", res.Evidence)
	}
	if !containsEvidence(res.Evidence, "traced services sample: checkout, payments") {
		t.Fatalf("missing traced services evidence: %+v", res.Evidence)
	}
	if !containsFinding(res.Findings, "Logs backend observability/opensearch reports yellow health") {
		t.Fatalf("missing logs health finding: %+v", res.Findings)
	}
	if len(res.HiddenRisks) == 0 || !strings.Contains(res.HiddenRisks[0], "partially queryable") {
		t.Fatalf("expected hidden risk about partial queryability, got %+v", res.HiddenRisks)
	}
}

func service(namespace, name, portName string, port int32) corev1.Service {
	return corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: portName, Port: port}},
		},
	}
}

func containsEvidence(evidence []model.Evidence, want string) bool {
	for _, item := range evidence {
		if strings.Contains(item.Signal, want) {
			return true
		}
	}
	return false
}

func containsFinding(findings []model.Finding, want string) bool {
	for _, item := range findings {
		if strings.Contains(item.Title, want) {
			return true
		}
	}
	return false
}
