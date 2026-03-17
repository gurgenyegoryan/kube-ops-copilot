package policycoverage

import (
	"context"
	"fmt"
	"sort"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

func (a *Analyzer) Name() string { return "policycoverage" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}

	res := analyzer.Result{}
	namespaces, err := a.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list namespaces: %w", err)
	}
	resourceQuotas, err := a.Client.CoreV1().ResourceQuotas("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list ResourceQuotas: %w", err)
	}
	limitRanges, err := a.Client.CoreV1().LimitRanges("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list LimitRanges: %w", err)
	}
	networkPolicies, err := a.Client.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list NetworkPolicies: %w", err)
	}
	pods, err := a.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list pods: %w", err)
	}

	activeNamespaces := map[string]struct{}{}
	for _, p := range pods.Items {
		if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(p.Namespace) {
			continue
		}
		activeNamespaces[p.Namespace] = struct{}{}
	}

	quotaNS := namespaceSetRQ(resourceQuotas.Items)
	limitNS := namespaceSetLR(limitRanges.Items)
	netpolNS := namespaceSetNP(networkPolicies.Items)

	missingDefaults := []string{}
	missingSegmentation := []string{}
	for _, ns := range namespaces.Items {
		if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(ns.Name) {
			continue
		}
		if _, ok := activeNamespaces[ns.Name]; !ok {
			continue
		}
		if _, ok := quotaNS[ns.Name]; !ok {
			if _, ok := limitNS[ns.Name]; !ok {
				missingDefaults = append(missingDefaults, ns.Name)
			}
		}
		if _, ok := netpolNS[ns.Name]; !ok {
			missingSegmentation = append(missingSegmentation, ns.Name)
		}
	}

	sort.Strings(missingDefaults)
	sort.Strings(missingSegmentation)
	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("policy coverage analysis: activeNamespaces=%d missingQuotaAndLimitRange=%d missingNetworkPolicy=%d", len(activeNamespaces), len(missingDefaults), len(missingSegmentation)),
	})

	if len(missingDefaults) > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d active namespace(s) lack both ResourceQuota and LimitRange", len(missingDefaults)),
			Severity:              model.SeverityLow,
			Urgency:               model.UrgencyThisWeek,
			Confidence:            model.ConfidenceMedium,
			AffectedScope:         "policies/namespaces",
			WhyItMatters:          "Namespaces without basic defaults are more likely to accumulate mis-sized workloads and accidental noisy-neighbor incidents.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.HiddenRisks = append(res.HiddenRisks, "Missing namespace defaults make future incidents more likely even when the current snapshot looks stable.")
	}

	if len(missingSegmentation) > 0 {
		res.HiddenRisks = append(res.HiddenRisks, fmt.Sprintf("NetworkPolicy is absent in active namespaces: %s", joinSample(missingSegmentation, 8)))
	}

	return res, nil
}

func namespaceSetRQ(items []corev1.ResourceQuota) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range items {
		out[item.Namespace] = struct{}{}
	}
	return out
}

func namespaceSetLR(items []corev1.LimitRange) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range items {
		out[item.Namespace] = struct{}{}
	}
	return out
}

func namespaceSetNP(items []networkingv1.NetworkPolicy) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range items {
		out[item.Namespace] = struct{}{}
	}
	return out
}

func joinSample(in []string, n int) string {
	if len(in) > n {
		in = in[:n]
	}
	if len(in) == 0 {
		return "none"
	}
	out := in[0]
	for i := 1; i < len(in); i++ {
		out += ", " + in[i]
	}
	return out
}
