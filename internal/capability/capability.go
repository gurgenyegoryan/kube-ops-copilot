package capability

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Status string

const (
	Detected     Status = "detected"
	Candidate    Status = "candidate"
	NotConfirmed Status = "not_confirmed"
)

type Inventory map[string]Status

type Detection struct {
	Inventory        Inventory
	Groups           map[string]struct{}
	ServiceSignals   map[string]struct{}
	ComponentSignals map[string]struct{}
}

func Detect(ctx context.Context, client *kubernetes.Clientset, includeSystem bool) (Detection, error) {
	d := Detection{
		Inventory:        Inventory{},
		Groups:           map[string]struct{}{},
		ServiceSignals:   map[string]struct{}{},
		ComponentSignals: map[string]struct{}{},
	}

	groups, err := client.Discovery().ServerGroups()
	if err == nil {
		for _, g := range groups.Groups {
			d.Groups[g.Name] = struct{}{}
		}
	}

	services, err := client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, svc := range services.Items {
			if !includeSystem && IsSystemNamespace(svc.Namespace) {
				continue
			}
			for _, signal := range DetectServiceSignals(svc) {
				d.ServiceSignals[signal] = struct{}{}
			}
		}
	}

	pods, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, signal := range DetectPlatformComponents(pods.Items, includeSystem) {
			d.ComponentSignals[signal] = struct{}{}
		}
	}

	d.Inventory = BuildInventory(d.Groups, d.ServiceSignals, d.ComponentSignals)
	return d, nil
}

func BuildInventory(groupSet map[string]struct{}, serviceSignals map[string]struct{}, componentSignals map[string]struct{}) Inventory {
	caps := Inventory{
		"resource_metrics":       NotConfirmed,
		"time_series_metrics":    NotConfirmed,
		"logs_backend":           NotConfirmed,
		"traces_backend":         NotConfirmed,
		"telemetry_pipeline":     NotConfirmed,
		"workload_autoscaling":   NotConfirmed,
		"traffic_entrypoint":     NotConfirmed,
		"network_segmentation":   NotConfirmed,
		"dynamic_storage":        NotConfirmed,
		"disruption_control":     NotConfirmed,
		"policy_defaults":        NotConfirmed,
		"certificate_management": NotConfirmed,
		"gitops":                 NotConfirmed,
		"secret_management":      NotConfirmed,
		"service_mesh":           NotConfirmed,
	}

	if HasKey(groupSet, "metrics.k8s.io") {
		caps["resource_metrics"] = Detected
	} else if HasKey(serviceSignals, "metrics_endpoint") {
		caps["resource_metrics"] = Candidate
	}
	if HasKey(groupSet, "monitoring.coreos.com") || HasKey(componentSignals, "time_series_metrics") || HasKey(serviceSignals, "time_series_metrics") {
		caps["time_series_metrics"] = Detected
	} else if HasKey(serviceSignals, "metrics_endpoint") {
		caps["time_series_metrics"] = Candidate
	}
	if HasKey(componentSignals, "logs_backend") || HasKey(serviceSignals, "logs_backend") {
		caps["logs_backend"] = Detected
	} else if HasKey(serviceSignals, "logs_endpoint") || HasKey(componentSignals, "logs_collection") {
		caps["logs_backend"] = Candidate
	}
	if HasKey(groupSet, "opentelemetry.io") || HasKey(componentSignals, "traces_backend") || HasKey(serviceSignals, "traces_backend") {
		caps["traces_backend"] = Detected
	} else if HasKey(serviceSignals, "traces_endpoint") {
		caps["traces_backend"] = Candidate
	}
	if HasKey(groupSet, "opentelemetry.io") || HasKey(componentSignals, "telemetry_pipeline") || HasKey(serviceSignals, "telemetry_pipeline") {
		caps["telemetry_pipeline"] = Detected
	}
	if HasKey(groupSet, "custom.metrics.k8s.io") || HasKey(groupSet, "external.metrics.k8s.io") || HasKey(groupSet, "autoscaling.k8s.io") {
		caps["workload_autoscaling"] = Detected
	}
	if HasKey(groupSet, "networking.k8s.io") || HasKey(groupSet, "gateway.networking.k8s.io") || HasKey(serviceSignals, "north_south_entrypoint") {
		caps["traffic_entrypoint"] = Detected
	}
	if HasKey(groupSet, "networking.k8s.io") {
		caps["network_segmentation"] = Candidate
	}
	if HasKey(groupSet, "storage.k8s.io") {
		caps["dynamic_storage"] = Detected
	}
	if HasKey(groupSet, "policy") {
		caps["disruption_control"] = Detected
	}
	if HasKey(groupSet, "cert-manager.io") || HasKey(componentSignals, "certificate_management") {
		caps["certificate_management"] = Detected
	}
	if HasKey(groupSet, "argoproj.io") || HasKey(groupSet, "source.toolkit.fluxcd.io") || HasKey(groupSet, "kustomize.toolkit.fluxcd.io") || HasKey(componentSignals, "gitops") || HasKey(serviceSignals, "gitops") {
		caps["gitops"] = Detected
	}
	if HasKey(groupSet, "external-secrets.io") || HasKey(groupSet, "secrets-store.csi.x-k8s.io") || HasKey(componentSignals, "secret_management") {
		caps["secret_management"] = Detected
	}
	if HasKey(groupSet, "install.istio.io") || HasKey(groupSet, "networking.istio.io") || HasKey(groupSet, "linkerd.io") || HasKey(componentSignals, "service_mesh") {
		caps["service_mesh"] = Detected
	}

	return caps
}

func DetectPlatformComponents(pods []corev1.Pod, includeSystem bool) []string {
	seen := map[string]struct{}{}
	for _, pod := range pods {
		if !includeSystem && IsSystemNamespace(pod.Namespace) {
			continue
		}
		name := strings.ToLower(pod.Name)
		switch {
		case strings.Contains(name, "prometheus"):
			seen["time_series_metrics"] = struct{}{}
		case strings.Contains(name, "grafana"):
			seen["dashboards"] = struct{}{}
		case strings.Contains(name, "alertmanager"):
			seen["alerting"] = struct{}{}
		case strings.Contains(name, "loki"):
			seen["logs_backend"] = struct{}{}
		case strings.Contains(name, "promtail"):
			seen["logs_collection"] = struct{}{}
		case strings.Contains(name, "fluent-bit") || strings.Contains(name, "fluentd"):
			seen["logs_collection"] = struct{}{}
		case strings.Contains(name, "vector"):
			seen["logs_collection"] = struct{}{}
		case strings.Contains(name, "tempo"):
			seen["traces_backend"] = struct{}{}
		case strings.Contains(name, "jaeger"):
			seen["traces_backend"] = struct{}{}
		case strings.Contains(name, "otel") || strings.Contains(name, "opentelemetry"):
			seen["telemetry_pipeline"] = struct{}{}
		case strings.Contains(name, "kiali"):
			seen["service_mesh"] = struct{}{}
		case strings.Contains(name, "argocd") || strings.Contains(name, "argo-cd") || strings.Contains(name, "flux"):
			seen["gitops"] = struct{}{}
		case strings.Contains(name, "cert-manager"):
			seen["certificate_management"] = struct{}{}
		case strings.Contains(name, "external-secrets") || strings.Contains(name, "secret-store"):
			seen["secret_management"] = struct{}{}
		case strings.Contains(name, "istio") || strings.Contains(name, "linkerd") || strings.Contains(name, "cilium"):
			seen["service_mesh"] = struct{}{}
		}
	}
	return SortedKeys(seen)
}

func DetectServiceSignals(svc corev1.Service) []string {
	seen := map[string]struct{}{}
	name := strings.ToLower(svc.Name)
	for _, port := range svc.Spec.Ports {
		switch {
		case port.Port == 9090 || strings.Contains(strings.ToLower(port.Name), "metrics"):
			seen["metrics_endpoint"] = struct{}{}
		case port.Port == 3100 || strings.Contains(strings.ToLower(port.Name), "logs"):
			seen["logs_endpoint"] = struct{}{}
		case port.Port == 4317 || port.Port == 4318 || strings.Contains(strings.ToLower(port.Name), "otlp"):
			seen["telemetry_pipeline"] = struct{}{}
		case port.Port == 16686 || port.Port == 14268 || strings.Contains(strings.ToLower(port.Name), "jaeger") || strings.Contains(strings.ToLower(port.Name), "tempo"):
			seen["traces_endpoint"] = struct{}{}
		case port.Port == 80 || port.Port == 443:
			if svc.Spec.Type == corev1.ServiceTypeLoadBalancer || svc.Spec.Type == corev1.ServiceTypeNodePort {
				seen["north_south_entrypoint"] = struct{}{}
			}
		}
	}
	switch {
	case strings.Contains(name, "grafana"):
		seen["dashboards"] = struct{}{}
	case strings.Contains(name, "prometheus"):
		seen["time_series_metrics"] = struct{}{}
	case strings.Contains(name, "loki"):
		seen["logs_backend"] = struct{}{}
	case strings.Contains(name, "tempo") || strings.Contains(name, "jaeger"):
		seen["traces_backend"] = struct{}{}
	case strings.Contains(name, "argocd") || strings.Contains(name, "argo-cd") || strings.Contains(name, "flux"):
		seen["gitops"] = struct{}{}
	}
	return SortedKeys(seen)
}

func RenderInventory(inventory Inventory) string {
	keys := make([]string, 0, len(inventory))
	for k := range inventory {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+string(inventory[k]))
	}
	return strings.Join(out, ", ")
}

func IsSystemNamespace(ns string) bool {
	s := strings.ToLower(ns)
	if s == "kube-system" || s == "kube-public" || s == "kube-node-lease" {
		return true
	}
	for _, p := range []string{"kube-", "amazon-", "aws-", "istio-", "ingress-", "cert-manager", "monitoring", "observability"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func SortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func HasKey[K comparable, V any](m map[K]V, key K) bool {
	_, ok := m[key]
	return ok
}
