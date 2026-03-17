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
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/telemetrycoverage"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/trafficexposure"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer/workloads"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"k8s.io/client-go/kubernetes"
)

type capabilityAdapter interface {
	Name() string
	Match(capability.Inventory) bool
	Build(*kubernetes.Clientset, bool, capability.Inventory) []analyzer.Analyzer
}

func defaultAnalyzers(ctx context.Context, client *kubernetes.Clientset, includeSystemNamespaces bool, eventsSince time.Duration) []analyzer.Analyzer {
	analyzers := []analyzer.Analyzer{
		&clusterinfo.Analyzer{Client: client},
		&discovery.Analyzer{Client: client, IncludeSystemNamespaces: includeSystemNamespaces},
		&clusterhealth.Analyzer{
			Client:                  client,
			IncludeSystemNamespaces: includeSystemNamespaces,
			CollectPodLogHints:      true,
			MaxPodLogHints:          3,
			PodLogTailLines:         200,
		},
		&events.Analyzer{Client: client, Since: eventsSince},
		&workloads.Analyzer{Client: client, IncludeSystemNamespaces: includeSystemNamespaces},
		&resources.Analyzer{Client: client, IncludeSystemNamespaces: includeSystemNamespaces},
		&pdb.Analyzer{Client: client, IncludeSystemNamespaces: includeSystemNamespaces},
	}

	detection, err := capability.Detect(ctx, client, includeSystemNamespaces)
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
	}
}

type telemetryAdapter struct{}

func (telemetryAdapter) Name() string { return "telemetry" }
func (telemetryAdapter) Match(inv capability.Inventory) bool {
	return len(inv) > 0
}
func (telemetryAdapter) Build(_ *kubernetes.Clientset, _ bool, inv capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&telemetrycoverage.Analyzer{Inventory: inv}}
}

type trafficAdapter struct{}

func (trafficAdapter) Name() string { return "traffic" }
func (trafficAdapter) Match(inv capability.Inventory) bool {
	return inv["traffic_entrypoint"] != capability.NotConfirmed
}
func (trafficAdapter) Build(client *kubernetes.Clientset, includeSystem bool, _ capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&trafficexposure.Analyzer{Client: client, IncludeSystemNamespaces: includeSystem}}
}

type autoscalingAdapter struct{}

func (autoscalingAdapter) Name() string { return "autoscaling" }
func (autoscalingAdapter) Match(inv capability.Inventory) bool {
	return inv["traffic_entrypoint"] != capability.NotConfirmed || inv["workload_autoscaling"] != capability.NotConfirmed
}
func (autoscalingAdapter) Build(client *kubernetes.Clientset, includeSystem bool, _ capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&autoscalingposture.Analyzer{Client: client, IncludeSystemNamespaces: includeSystem}}
}

type policyAdapter struct{}

func (policyAdapter) Name() string { return "policy" }
func (policyAdapter) Match(inv capability.Inventory) bool {
	return len(inv) > 0
}
func (policyAdapter) Build(client *kubernetes.Clientset, includeSystem bool, _ capability.Inventory) []analyzer.Analyzer {
	return []analyzer.Analyzer{&policycoverage.Analyzer{Client: client, IncludeSystemNamespaces: includeSystem}}
}
