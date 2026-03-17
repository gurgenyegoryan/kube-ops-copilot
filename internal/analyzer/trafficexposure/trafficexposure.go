package trafficexposure

import (
	"context"
	"fmt"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

func (a *Analyzer) Name() string { return "trafficexposure" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}
	res := analyzer.Result{}

	services, err := a.Client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list services: %w", err)
	}

	exposedServices := 0
	withoutEndpoints := []string{}
	for _, svc := range services.Items {
		if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(svc.Namespace) {
			continue
		}
		if svc.Spec.Type != "LoadBalancer" && svc.Spec.Type != "NodePort" {
			continue
		}
		exposedServices++
		if len(svc.Spec.Selector) == 0 {
			continue
		}
		slices, err := a.Client.DiscoveryV1().EndpointSlices(svc.Namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "kubernetes.io/service-name=" + svc.Name,
		})
		if err != nil {
			continue
		}
		if countReadyEndpoints(slices.Items) == 0 {
			withoutEndpoints = append(withoutEndpoints, fmt.Sprintf("%s/%s", svc.Namespace, svc.Name))
		}
	}

	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("traffic exposure analysis: exposedServices=%d servicesWithoutReadyEndpoints=%d", exposedServices, len(withoutEndpoints)),
	})

	if len(withoutEndpoints) > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d externally exposed service(s) have no ready endpoints", len(withoutEndpoints)),
			Severity:              model.SeverityHigh,
			Urgency:               model.UrgencyImmediate,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "traffic/services",
			WhyItMatters:          "Externally exposed services without ready endpoints can produce immediate user-visible failures even when deployments appear healthy at a glance.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		for _, item := range withoutEndpoints {
			res.Evidence = append(res.Evidence, model.Evidence{Signal: "exposed service without ready endpoints: " + item})
		}
		res.Recommended.Immediate = append(res.Recommended.Immediate, model.Action{
			Title:               "Trace why exposed services have zero ready endpoints",
			ExpectedBenefit:     "Separates selector drift, readiness failures, and rollout gaps before users experience a broader outage.",
			RiskTradeoff:        "Low; read-only investigation.",
			Priority:            "P0",
			ExecutionSafety:     model.SafetySafeAutoCandidate,
			ApprovalRequirement: model.ApprovalNone,
			RollbackOutline:     "N/A (no changes)",
		})
	}

	return res, nil
}

func countReadyEndpoints(items []discoveryv1.EndpointSlice) int {
	ready := 0
	for _, slice := range items {
		for _, ep := range slice.Endpoints {
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				ready++
			}
		}
	}
	return ready
}
