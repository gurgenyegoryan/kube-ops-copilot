package autoscalingposture

import (
	"context"
	"fmt"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

func (a *Analyzer) Name() string { return "autoscalingposture" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}
	res := analyzer.Result{}

	deployments, err := a.Client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list deployments: %w", err)
	}
	hpas, err := a.Client.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list HPAs: %w", err)
	}
	services, err := a.Client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list services: %w", err)
	}

	hpaTargets := map[string]struct{}{}
	for _, hpa := range hpas.Items {
		if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(hpa.Namespace) {
			continue
		}
		key := hpa.Namespace + "/" + hpa.Spec.ScaleTargetRef.Kind + "/" + hpa.Spec.ScaleTargetRef.Name
		hpaTargets[key] = struct{}{}
	}

	singleReplicaExposedNoHPA := []string{}
	for _, d := range deployments.Items {
		if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(d.Namespace) {
			continue
		}
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		if replicas != 1 {
			continue
		}
		if _, ok := hpaTargets[d.Namespace+"/Deployment/"+d.Name]; ok {
			continue
		}
		if isExposedByService(d, services.Items) {
			singleReplicaExposedNoHPA = append(singleReplicaExposedNoHPA, fmt.Sprintf("%s/%s", d.Namespace, d.Name))
		}
	}

	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("autoscaling posture analysis: singleReplicaExposedNoHPA=%d", len(singleReplicaExposedNoHPA)),
	})
	if len(singleReplicaExposedNoHPA) > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d exposed deployment(s) run as a single replica without HPA", len(singleReplicaExposedNoHPA)),
			Severity:              model.SeverityMedium,
			Urgency:               model.UrgencyThisWeek,
			Confidence:            model.ConfidenceMedium,
			AffectedScope:         "workloads/autoscaling",
			WhyItMatters:          "Single-replica exposed workloads have little resilience against node drains, restarts, or burst traffic and often become a hidden availability bottleneck before alerts fire.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		for _, item := range singleReplicaExposedNoHPA {
			res.Evidence = append(res.Evidence, model.Evidence{Signal: "single-replica exposed workload without HPA: " + item})
		}
		res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
			Title:               "Review exposed single-replica workloads for replica floor and autoscaling readiness",
			ExpectedBenefit:     "Reduces availability risk from pod loss and creates headroom for traffic bursts without blindly scaling everything.",
			RiskTradeoff:        "Extra replicas increase cost and may expose hidden statefulness assumptions.",
			Priority:            "P1",
			ExecutionSafety:     model.SafetyNeedsOperatorApproval,
			ApprovalRequirement: model.ApprovalNeedsOperator,
			RollbackOutline:     "Revert replica count or autoscaling policy if it causes instability or unnecessary spend.",
		})
	}

	return res, nil
}

func isExposedByService(d appsv1.Deployment, services []corev1.Service) bool {
	labels := d.Spec.Template.Labels
	if len(labels) == 0 {
		return false
	}
	for _, svc := range services {
		if svc.Namespace != d.Namespace {
			continue
		}
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer && svc.Spec.Type != corev1.ServiceTypeNodePort {
			continue
		}
		if selectorMatches(svc.Spec.Selector, labels) {
			return true
		}
	}
	return false
}

func selectorMatches(selector map[string]string, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}
