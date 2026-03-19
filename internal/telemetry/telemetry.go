package telemetry

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Kind string

const (
	KindTimeSeries Kind = "time_series"
	KindLogs       Kind = "logs"
	KindTraces     Kind = "traces"
)

type Backend struct {
	Kind         Kind
	Vendor       string
	Namespace    string
	Service      string
	Port         string
	Schemes      []string
	PathPrefixes []string
}

var proxyGetRaw = ProxyGetRaw

func Discover(ctx context.Context, client *kube.Client, inventory capability.Inventory) ([]Backend, error) {
	if client == nil || client.Kubernetes == nil {
		return nil, fmt.Errorf("kubernetes client is nil")
	}
	services, err := client.Kubernetes.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services for telemetry discovery: %w", err)
	}

	out := []Backend{}
	seen := map[string]struct{}{}
	for _, svc := range services.Items {
		for _, backend := range detectServiceBackends(svc, inventory) {
			key := strings.Join([]string{string(backend.Kind), backend.Vendor, backend.Namespace, backend.Service, backend.Port, strings.Join(backend.PathPrefixes, ","), strings.Join(backend.Schemes, ",")}, "|")
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, backend)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Vendor != out[j].Vendor {
			return out[i].Vendor < out[j].Vendor
		}
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Port < out[j].Port
	})
	return out, nil
}

func detectServiceBackends(svc corev1.Service, inventory capability.Inventory) []Backend {
	name := strings.ToLower(svc.Name)
	ns := strings.ToLower(svc.Namespace)
	out := []Backend{}
	for _, port := range svc.Spec.Ports {
		portName := strings.ToLower(port.Name)
		portNum := strconv.Itoa(int(port.Port))
		schemes := detectSchemes(port)

		if inventory["time_series_metrics"] != capability.NotConfirmed {
			switch {
			case containsAny(name, "prometheus", "thanos", "mimir", "cortex", "vmselect", "victoria", "victoria-metrics"):
				out = append(out, Backend{
					Kind:         KindTimeSeries,
					Vendor:       detectMetricsVendor(name),
					Namespace:    svc.Namespace,
					Service:      svc.Name,
					Port:         choosePortName(portName, portNum),
					Schemes:      schemes,
					PathPrefixes: metricsPathPrefixes(name),
				})
			case port.Port == 9090 && (strings.Contains(portName, "http") || strings.Contains(portName, "web") || strings.Contains(name, "query")):
				out = append(out, Backend{
					Kind:         KindTimeSeries,
					Vendor:       "prometheus-compatible",
					Namespace:    svc.Namespace,
					Service:      svc.Name,
					Port:         choosePortName(portName, portNum),
					Schemes:      schemes,
					PathPrefixes: []string{"/api/v1"},
				})
			}
		}

		if inventory["logs_backend"] != capability.NotConfirmed {
			switch {
			case containsAny(name, "loki"):
				out = append(out, Backend{
					Kind:         KindLogs,
					Vendor:       "loki",
					Namespace:    svc.Namespace,
					Service:      svc.Name,
					Port:         choosePortName(portName, portNum),
					Schemes:      schemes,
					PathPrefixes: []string{"/loki/api/v1"},
				})
			case containsAny(name, "elasticsearch", "opensearch") || containsAny(ns, "elasticsearch", "opensearch"):
				out = append(out, Backend{
					Kind:         KindLogs,
					Vendor:       detectSearchVendor(name, ns),
					Namespace:    svc.Namespace,
					Service:      svc.Name,
					Port:         choosePortName(portName, portNum),
					Schemes:      schemes,
					PathPrefixes: []string{"/"},
				})
			}
		}

		if inventory["traces_backend"] != capability.NotConfirmed {
			switch {
			case containsAny(name, "tempo"):
				out = append(out, Backend{
					Kind:         KindTraces,
					Vendor:       "tempo",
					Namespace:    svc.Namespace,
					Service:      svc.Name,
					Port:         choosePortName(portName, portNum),
					Schemes:      schemes,
					PathPrefixes: []string{"/", "/api/search"},
				})
			case containsAny(name, "jaeger"):
				out = append(out, Backend{
					Kind:         KindTraces,
					Vendor:       "jaeger",
					Namespace:    svc.Namespace,
					Service:      svc.Name,
					Port:         choosePortName(portName, portNum),
					Schemes:      schemes,
					PathPrefixes: []string{"/api", "/jaeger/api"},
				})
			}
		}
	}
	return out
}

func ProxyGetRaw(ctx context.Context, client *kube.Client, backend Backend, path string, params map[string]string) ([]byte, error) {
	if client == nil || client.Kubernetes == nil {
		return nil, fmt.Errorf("kubernetes client is nil")
	}
	path = strings.TrimPrefix(strings.TrimSpace(path), "/")
	var lastErr error
	for _, scheme := range backend.Schemes {
		resp, err := client.Kubernetes.CoreV1().Services(backend.Namespace).ProxyGet(scheme, backend.Service, backend.Port, path, params).DoRaw(ctx)
		if err == nil {
			return resp, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable scheme for backend %s/%s", backend.Namespace, backend.Service)
	}
	return nil, lastErr
}

func choosePortName(name, fallback string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return fallback
}

func detectSchemes(port corev1.ServicePort) []string {
	name := strings.ToLower(port.Name)
	switch {
	case port.Port == 443 || strings.Contains(name, "https") || strings.Contains(name, "tls"):
		return []string{"https", "http"}
	default:
		return []string{"http", "https"}
	}
}

func detectMetricsVendor(name string) string {
	switch {
	case containsAny(name, "thanos"):
		return "thanos"
	case containsAny(name, "mimir"):
		return "mimir"
	case containsAny(name, "cortex"):
		return "cortex"
	case containsAny(name, "vmselect", "victoria", "victoria-metrics"):
		return "victoriametrics"
	default:
		return "prometheus"
	}
}

func metricsPathPrefixes(name string) []string {
	switch detectMetricsVendor(name) {
	case "victoriametrics":
		return []string{"/select/0/prometheus/api/v1", "/api/v1"}
	default:
		return []string{"/api/v1", "/prometheus/api/v1"}
	}
}

func detectSearchVendor(name, ns string) string {
	switch {
	case containsAny(name, "opensearch") || containsAny(ns, "opensearch"):
		return "opensearch"
	default:
		return "elasticsearch"
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, strings.ToLower(strings.TrimSpace(sub))) {
			return true
		}
	}
	return false
}
