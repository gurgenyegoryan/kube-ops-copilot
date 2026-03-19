package telemetry

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
)

func TestQueryPrometheusVectorFirstFallsBackAcrossExpressionsAndPaths(t *testing.T) {
	restore := stubProxyGetRaw(t, func(_ context.Context, _ *kube.Client, _ Backend, path string, params map[string]string) ([]byte, error) {
		query := strings.TrimSpace(params["query"])
		switch {
		case query == "bad_expr":
			return []byte(`{"status":"error","error":"parse error"}`), nil
		case query == "good_expr" && path == "prometheus/api/v1/query":
			return []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"exported_namespace":"prod","exported_pod":"api-123"},"value":[1710000000,"5"]}]}}`), nil
		default:
			return nil, fmt.Errorf("not found")
		}
	})
	defer restore()

	backend := Backend{
		Namespace:    "monitoring",
		Service:      "thanos-query",
		PathPrefixes: []string{"/api/v1", "/prometheus/api/v1"},
	}

	result, err := QueryPrometheusVectorFirst(context.Background(), nil, backend, []string{"bad_expr", "good_expr"})
	if err != nil {
		t.Fatalf("QueryPrometheusVectorFirst returned error: %v", err)
	}
	if result.Expression != "good_expr" {
		t.Fatalf("expected fallback expression to be selected, got %q", result.Expression)
	}
	if result.PathPrefix != "/prometheus/api/v1" {
		t.Fatalf("expected fallback path prefix to be selected, got %q", result.PathPrefix)
	}
	if len(result.Samples) != 1 || result.Samples[0].Value != 5 {
		t.Fatalf("unexpected vector samples: %+v", result.Samples)
	}
}

func TestProbePrometheusBackendThanos(t *testing.T) {
	restore := stubProxyGetRaw(t, func(_ context.Context, _ *kube.Client, _ Backend, path string, params map[string]string) ([]byte, error) {
		switch {
		case path == "api/v1/status/buildinfo":
			return []byte(`{"status":"success","data":{"version":"2.51.0"}}`), nil
		case path == "api/v1/query" && strings.TrimSpace(params["query"]) == "up":
			return []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"namespace":"prod","pod":"api-123"},"value":[1710000000,"1"]}]}}`), nil
		case path == "api/v1/stores":
			return []byte(`{"status":"success","data":[{"name":"store-a"},{"name":"store-b"}]}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", path)
		}
	})
	defer restore()

	probe := ProbePrometheusBackend(context.Background(), nil, Backend{
		Vendor:       "thanos",
		Namespace:    "monitoring",
		Service:      "thanos-query",
		PathPrefixes: []string{"/api/v1"},
	})

	if !probe.Reachable {
		t.Fatalf("expected probe to mark backend reachable")
	}
	if probe.BuildVersion != "2.51.0" {
		t.Fatalf("unexpected build version: %q", probe.BuildVersion)
	}
	joined := strings.Join(probe.Signals, " | ")
	for _, want := range []string{"buildinfo version=2.51.0", "queryable expr=up", "thanos stores=2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected signals to contain %q, got %q", want, joined)
		}
	}
}

func TestProbeLogsBackendLokiWithAliasLabels(t *testing.T) {
	restore := stubProxyGetRaw(t, func(_ context.Context, _ *kube.Client, _ Backend, path string, params map[string]string) ([]byte, error) {
		switch path {
		case "loki/api/v1/labels":
			return []byte(`{"status":"success","data":["cluster","exported_namespace","exported_pod","container"]}`), nil
		case "loki/api/v1/query":
			query := params["query"]
			if strings.Contains(query, "|~") {
				return []byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"exported_namespace":"payments","exported_pod":"payments-api-7f8d9"},"value":[1710000000,"17"]}]}}`), nil
			}
			return []byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", path)
		}
	})
	defer restore()

	probe := ProbeLogsBackend(context.Background(), nil, Backend{
		Vendor:       "loki",
		Namespace:    "observability",
		Service:      "loki",
		PathPrefixes: []string{"/loki/api/v1"},
	})

	if !probe.Reachable {
		t.Fatalf("expected Loki probe to be reachable")
	}
	if len(probe.TopErrorPods) != 1 {
		t.Fatalf("expected one hotspot sample, got %+v", probe.TopErrorPods)
	}
	if probe.TopErrorPods[0].Namespace != "payments" || probe.TopErrorPods[0].Pod != "payments-api-7f8d9" {
		t.Fatalf("unexpected hotspot sample: %+v", probe.TopErrorPods[0])
	}
}

func TestProbeTracesBackendJaeger(t *testing.T) {
	restore := stubProxyGetRaw(t, func(_ context.Context, _ *kube.Client, _ Backend, path string, _ map[string]string) ([]byte, error) {
		switch path {
		case "api/services":
			return []byte(`{"data":["checkout","payments"]}`), nil
		case "api/services/checkout/operations":
			return []byte(`{"data":["GET /healthz","POST /checkout"]}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", path)
		}
	})
	defer restore()

	probe := ProbeTracesBackend(context.Background(), nil, Backend{
		Vendor:       "jaeger",
		Namespace:    "observability",
		Service:      "jaeger-query",
		PathPrefixes: []string{"/api"},
	})

	if !probe.Reachable {
		t.Fatalf("expected Jaeger probe to be reachable")
	}
	if len(probe.TracedServices) != 2 {
		t.Fatalf("unexpected traced services: %+v", probe.TracedServices)
	}
	if joined := strings.Join(probe.Signals, " | "); !strings.Contains(joined, "sample service checkout operations=2") {
		t.Fatalf("expected operations signal, got %q", joined)
	}
}

func TestProbeTracesBackendTempo(t *testing.T) {
	restore := stubProxyGetRaw(t, func(_ context.Context, _ *kube.Client, _ Backend, path string, _ map[string]string) ([]byte, error) {
		switch path {
		case "ready":
			return []byte("ok"), nil
		case "api/search/tag/service.name/values":
			return []byte(`{"tagValues":["frontend","payments"]}`), nil
		default:
			return nil, fmt.Errorf("unexpected path %s", path)
		}
	})
	defer restore()

	probe := ProbeTracesBackend(context.Background(), nil, Backend{
		Vendor:       "tempo",
		Namespace:    "observability",
		Service:      "tempo-query",
		PathPrefixes: []string{"/", "/api/search"},
	})

	if !probe.Reachable {
		t.Fatalf("expected Tempo probe to be reachable")
	}
	if joined := strings.Join(probe.Signals, " | "); !strings.Contains(joined, "service-tag values=2") {
		t.Fatalf("expected tempo service signal, got %q", joined)
	}
}

func stubProxyGetRaw(t *testing.T, fn func(context.Context, *kube.Client, Backend, string, map[string]string) ([]byte, error)) func() {
	t.Helper()
	prev := proxyGetRaw
	proxyGetRaw = fn
	return func() {
		proxyGetRaw = prev
	}
}
