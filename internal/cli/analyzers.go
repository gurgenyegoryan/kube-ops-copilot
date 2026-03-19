package cli

import (
	"context"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/autoscalingposture"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/clusterhealth"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/clusterinfo"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/discovery"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/events"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/pdb"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/policycoverage"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/resources"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/runtimemetrics"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/telemetrycoverage"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/telemetryruntime"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/timeseriespressure"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/trafficexposure"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/workloadrisk"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/workloads"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
)

type capabilityAdapter interface {
	Name() string
	Match(capability.Inventory) bool
	Build(*kube.Client, bool, capability.Inventory) []analyzer.Analyzer
}

func defaultAnalyzers(ctx context.Context, client *kube.Client, includeSystemNamespaces bool, eventsSince time.Duration) []analyzer.Analyzer {
	if client == nil || client.Kubernetes == nil {
		return nil
	}
	analyzers := []analyzer.Analyzer{
		&clusterinfo.Analyzer{Client: client.Kubernetes},
		&discovery.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystemNamespaces},
		&clusterhealth.Analyzer{
			Client:                  client.Kubernetes,
			IncludeSystemNamespaces: includeSystemNamespaces,
			CollectPodLogHints:      true,
			MaxPodLogHints:          3,
			PodLogTailLines:         200,
		},
		&events.Analyzer{Client: client.Kubernetes, Since: eventsSince},
		&workloads.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystemNamespaces},
		&resources.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystemNamespaces},
		&workloadrisk.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystemNamespaces},
		&pdb.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystemNamespaces},
	}

	detection, err := capability.Detect(ctx, client.Kubernetes, true)
	if err != nil {
		return analyzers
	}
	for _, adapter := range capabilityAdapters() {
		if adapter.Match(detection.Inventory) {
			analyzers = append(analyzers, adapter.Build(client, includeSystemNamespaces, detection.Inventory)...)
		}
	}
	return analyzers
}

func capabilityAdapters() []capabilityAdapter {
	return []capabilityAdapter{
		telemetryAdapter{},
		trafficAdapter{},
		autoscalingAdapter{},
		policyAdapter{},
		runtimeMetricsAdapter{},
	}
}

type telemetryAdapter struct{}

func (telemetryAdapter) Name() string { return "telemetry" }
func (telemetryAdapter) Match(inv capability.Inventory) bool {
	return len(inv) > 0
}
func (telemetryAdapter) Build(client *kube.Client, includeSystem bool, inv capability.Inventory) []analyzer.Analyzer {
	analyzers := []analyzer.Analyzer{
		&telemetrycoverage.Analyzer{Inventory: inv},
	}
	if inv["time_series_metrics"] != capability.NotConfirmed || inv["logs_backend"] != capability.NotConfirmed || inv["traces_backend"] != capability.NotConfirmed {
		analyzers = append(analyzers, &telemetryruntime.Analyzer{Client: client, Inventory: inv})
	}
	if inv["time_series_metrics"] != capability.NotConfirmed {
		analyzers = append(analyzers, &timeseriespressure.Analyzer{
			Client:                  client,
			Inventory:               inv,
			IncludeSystemNamespaces: includeSystem,
		})
	}
	return analyzers
}

type trafficAdapter struct{}

func (trafficAdapter) Name() string { return "traffic" }
func (trafficAdapter) Match(inv capability.Inventory) bool {
	return inv["traffic_entrypoint"] != capability.NotConfirmed
}
func (trafficAdapter) Build(client *kube.Client, includeSystem bool, _ capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&trafficexposure.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystem}}
}

type autoscalingAdapter struct{}

func (autoscalingAdapter) Name() string { return "autoscaling" }
func (autoscalingAdapter) Match(inv capability.Inventory) bool {
	return inv["traffic_entrypoint"] != capability.NotConfirmed || inv["workload_autoscaling"] != capability.NotConfirmed
}
func (autoscalingAdapter) Build(client *kube.Client, includeSystem bool, _ capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&autoscalingposture.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystem}}
}

type policyAdapter struct{}

func (policyAdapter) Name() string { return "policy" }
func (policyAdapter) Match(inv capability.Inventory) bool {
	return len(inv) > 0
}
func (policyAdapter) Build(client *kube.Client, includeSystem bool, _ capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&policycoverage.Analyzer{Client: client.Kubernetes, IncludeSystemNamespaces: includeSystem}}
}

type runtimeMetricsAdapter struct{}

func (runtimeMetricsAdapter) Name() string { return "runtime-metrics" }
func (runtimeMetricsAdapter) Match(inv capability.Inventory) bool {
	return inv["resource_metrics"] == capability.Detected
}
func (runtimeMetricsAdapter) Build(client *kube.Client, includeSystem bool, _ capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&runtimemetrics.Analyzer{Client: client, IncludeSystemNamespaces: includeSystem}}
}
