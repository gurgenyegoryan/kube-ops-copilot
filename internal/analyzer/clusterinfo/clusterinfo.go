package clusterinfo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client *kubernetes.Clientset
}

func (a *Analyzer) Name() string { return "clusterinfo" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}

	res := analyzer.Result{}

	if v, err := a.Client.Discovery().ServerVersion(); err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to read apiserver version: %v", err))
	} else if v != nil {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("kubernetes apiserver version: %s", strings.TrimSpace(v.GitVersion))})
	}

	nodes, err := a.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list nodes for version inventory: %v", err))
		return res, nil
	}

	kubeletVersions := map[string]int{}
	osImages := map[string]int{}
	runtimes := map[string]int{}
	kernels := map[string]int{}

	for _, n := range nodes.Items {
		if n.Status.NodeInfo.KubeletVersion != "" {
			kubeletVersions[n.Status.NodeInfo.KubeletVersion]++
		}
		if n.Status.NodeInfo.OSImage != "" {
			osImages[n.Status.NodeInfo.OSImage]++
		}
		if n.Status.NodeInfo.ContainerRuntimeVersion != "" {
			runtimes[n.Status.NodeInfo.ContainerRuntimeVersion]++
		}
		if n.Status.NodeInfo.KernelVersion != "" {
			kernels[n.Status.NodeInfo.KernelVersion]++
		}
	}

	if len(kubeletVersions) > 0 {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("node kubelet versions: %s", formatCountMap(kubeletVersions, 6))})
		if len(kubeletVersions) > 2 {
			res.Findings = append(res.Findings, model.Finding{
				Title:                 fmt.Sprintf("heterogeneous kubelet versions detected (variants=%d)", len(kubeletVersions)),
				Severity:              model.SeverityLow,
				Urgency:               model.UrgencyBacklog,
				Confidence:            model.ConfidenceMedium,
				AffectedScope:         "cluster/nodes",
				WhyItMatters:          "Large kubelet version skew can complicate incident triage and may hide node-specific behavior differences; plan upgrades to keep the fleet consistent.",
				AutomationSuitability: model.SuitabilityAdvisoryOnly,
			})
		}
	}
	if len(runtimes) > 0 {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("node container runtimes: %s", formatCountMap(runtimes, 6))})
	}
	if len(osImages) > 0 {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("node OS images: %s", formatCountMap(osImages, 6))})
	}
	if len(kernels) > 0 {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("node kernel versions: %s", formatCountMap(kernels, 6))})
	}

	// Metrics API (metrics-server) presence
	if _, err := a.Client.Discovery().ServerResourcesForGroupVersion("metrics.k8s.io/v1beta1"); err != nil {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "metrics.k8s.io API not detected (metrics-server likely missing or not registered)"})
		res.Unknowns = append(res.Unknowns, "CPU/memory sizing and saturation checks require a metrics pipeline (metrics-server and/or Prometheus)")
	} else {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "metrics.k8s.io API detected"})
	}

	// Prometheus / monitoring detection (best-effort)
	groups, err := a.Client.Discovery().ServerGroups()
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to query server API groups (for monitoring detection): %v", err))
		return res, nil
	}

	operatorDetected := false
	for _, g := range groups.Groups {
		if g.Name == "monitoring.coreos.com" {
			operatorDetected = true
			break
		}
	}

	if operatorDetected {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "Prometheus Operator API group detected (monitoring.coreos.com)"})
	} else {
		// fall back to a light heuristic: look in common namespaces for pods with prometheus/grafana/alertmanager in name
		candidates := []string{"monitoring", "observability"}
		found := []string{}
		for _, ns := range candidates {
			pods, err := a.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}
			for _, p := range pods.Items {
				name := strings.ToLower(p.Name)
				if strings.Contains(name, "prometheus") || strings.Contains(name, "grafana") || strings.Contains(name, "alertmanager") {
					found = append(found, fmt.Sprintf("%s/%s", ns, p.Name))
					if len(found) >= 5 {
						break
					}
				}
			}
			if len(found) >= 5 {
				break
			}
		}
		if len(found) > 0 {
			res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("monitoring pods detected (heuristic): %s", strings.Join(found, ", "))})
		} else {
			res.Evidence = append(res.Evidence, model.Evidence{Signal: "Prometheus/monitoring stack not detected (best-effort heuristic)"})
			res.Unknowns = append(res.Unknowns, "Time-series monitoring not confirmed; validate Prometheus/Grafana availability to support SLO-driven debugging")
		}
	}

	res.Recommended.Preventive = append(res.Recommended.Preventive, model.Action{
		Title:               "Ensure cluster has metrics and alerting coverage (metrics-server + time-series monitoring)",
		ExpectedBenefit:     "Enables evidence-based triage (resource saturation, latency/error trends) and reduces guesswork during incidents.",
		RiskTradeoff:        "Operational overhead of running and maintaining monitoring components.",
		Priority:            "P2",
		ExecutionSafety:     model.SafetyNeedsOperatorApproval,
		ApprovalRequirement: model.ApprovalNeedsOperator,
		RollbackOutline:     "N/A (planning task)",
	})

	return res, nil
}

func formatCountMap(m map[string]int, maxItems int) string {
	type kv struct {
		k string
		v int
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{k: k, v: v})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].v == items[j].v {
			return items[i].k < items[j].k
		}
		return items[i].v > items[j].v
	})
	if maxItems <= 0 {
		maxItems = 6
	}
	if len(items) > maxItems {
		items = items[:maxItems]
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, fmt.Sprintf("%s=%d", it.k, it.v))
	}
	return strings.Join(out, ", ")
}
