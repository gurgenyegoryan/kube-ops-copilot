package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

func (a *Analyzer) Name() string { return "discovery" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}

	res := analyzer.Result{}

	namespaces, err := a.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list namespaces: %w", err)
	}
	userNamespaces := 0
	nsNames := make([]string, 0, len(namespaces.Items))
	for _, ns := range namespaces.Items {
		if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(ns.Name) {
			continue
		}
		userNamespaces++
		nsNames = append(nsNames, ns.Name)
	}
	sort.Strings(nsNames)
	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("namespace inventory: total=%d user=%d sample=%s", len(namespaces.Items), userNamespaces, joinOrNone(limitStrings(nsNames, 8))),
	})

	groups, err := a.Client.Discovery().ServerGroups()
	groupSet := map[string]struct{}{}
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to query API groups for platform capability discovery: %v", err))
	} else {
		for _, g := range groups.Groups {
			groupSet[g.Name] = struct{}{}
		}
		detectedGroups := []string{}
		for _, name := range []string{
			"monitoring.coreos.com",
			"metrics.k8s.io",
			"custom.metrics.k8s.io",
			"external.metrics.k8s.io",
			"gateway.networking.k8s.io",
			"networking.k8s.io",
			"storage.k8s.io",
			"snapshot.storage.k8s.io",
			"autoscaling.k8s.io",
			"policy",
			"opentelemetry.io",
			"jaegertracing.io",
		} {
			if capability.HasKey(groupSet, name) {
				detectedGroups = append(detectedGroups, name)
			}
		}
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf("discovered platform API groups: %s", joinOrNone(detectedGroups)),
		})
	}

	serviceSignals := map[string]struct{}{}
	services, err := a.Client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list services for exposure inventory: %v", err))
	} else {
		clusterIP := 0
		nodePort := 0
		loadBalancer := 0
		externalName := 0
		for _, svc := range services.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(svc.Namespace) {
				continue
			}
			for _, signal := range capability.DetectServiceSignals(svc) {
				serviceSignals[signal] = struct{}{}
			}
			switch svc.Spec.Type {
			case "NodePort":
				nodePort++
			case "LoadBalancer":
				loadBalancer++
			case "ExternalName":
				externalName++
			default:
				clusterIP++
			}
		}
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf("service exposure inventory: ClusterIP=%d NodePort=%d LoadBalancer=%d ExternalName=%d", clusterIP, nodePort, loadBalancer, externalName),
		})
		if len(serviceSignals) > 0 {
			res.Evidence = append(res.Evidence, model.Evidence{
				Signal: fmt.Sprintf("service capability hints: %s", joinOrNone(capability.SortedKeys(serviceSignals))),
			})
		}
	}

	ingresses, err := a.Client.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list ingresses: %v", err))
	} else {
		count := 0
		for _, ing := range ingresses.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(ing.Namespace) {
				continue
			}
			count++
		}
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d ingress resource(s) outside filtered system namespaces", count)})
	}

	hpas, err := a.Client.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list HPAs: %v", err))
	} else {
		hpaTargets := []string{}
		count := 0
		for _, hpa := range hpas.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(hpa.Namespace) {
				continue
			}
			count++
			hpaTargets = append(hpaTargets, fmt.Sprintf("%s/%s->%s/%s min=%d max=%d", hpa.Namespace, hpa.Name, hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name, derefInt32(hpa.Spec.MinReplicas), hpa.Spec.MaxReplicas))
		}
		sort.Strings(hpaTargets)
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf("observed %d HorizontalPodAutoscaler(s) outside filtered system namespaces; sample=%s", count, joinOrNone(limitStrings(hpaTargets, 6))),
		})
		if count == 0 && userNamespaces > 0 {
			res.Findings = append(res.Findings, model.Finding{
				Title:                 "No HorizontalPodAutoscalers detected outside system namespaces",
				Severity:              model.SeverityMedium,
				Urgency:               model.UrgencyThisWeek,
				Confidence:            model.ConfidenceMedium,
				AffectedScope:         "workloads/autoscaling",
				WhyItMatters:          "Without workload autoscaling, burst-sensitive services depend entirely on static replica counts, increasing the chance of queue growth or latency spikes during traffic changes.",
				AutomationSuitability: model.SuitabilityAdvisoryOnly,
			})
			res.HiddenRisks = append(res.HiddenRisks, "A cluster can look healthy at rest while still being non-production-ready if user workloads rely only on static replica counts.")
			res.Recommended.Preventive = append(res.Recommended.Preventive, model.Action{
				Title:               "Review burst-sensitive workloads for HPA/KEDA suitability after validating requests and scaling signals",
				ExpectedBenefit:     "Reduces risk of latency spikes and queue growth under bursty load while keeping scaling policy evidence-driven.",
				RiskTradeoff:        "Autoscaling on bad requests/metrics can create oscillation or amplify churn; validate scaling signals first.",
				Priority:            "P2",
				ExecutionSafety:     model.SafetyNeedsOperatorApproval,
				ApprovalRequirement: model.ApprovalNeedsOperator,
				RollbackOutline:     "Revert autoscaler manifests or pin replica counts back to the previous values.",
			})
		}
	}

	statefulsets, err := a.Client.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list StatefulSets: %v", err))
	} else {
		count := 0
		for _, ss := range statefulsets.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(ss.Namespace) {
				continue
			}
			count++
		}
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d StatefulSet(s) outside filtered system namespaces", count)})
	}

	daemonsets, err := a.Client.AppsV1().DaemonSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list DaemonSets: %v", err))
	} else {
		count := 0
		for _, ds := range daemonsets.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(ds.Namespace) {
				continue
			}
			count++
		}
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d DaemonSet(s) outside filtered system namespaces", count)})
	}

	cronjobs, err := a.Client.BatchV1().CronJobs("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list CronJobs: %v", err))
	} else {
		count := 0
		for _, cj := range cronjobs.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(cj.Namespace) {
				continue
			}
			count++
		}
		res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d CronJob(s) outside filtered system namespaces", count)})
	}

	storageClasses, err := a.Client.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list StorageClasses: %v", err))
	} else {
		defaultClasses := []string{}
		classNames := make([]string, 0, len(storageClasses.Items))
		for _, sc := range storageClasses.Items {
			classNames = append(classNames, sc.Name)
			if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" || sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
				defaultClasses = append(defaultClasses, sc.Name)
			}
		}
		sort.Strings(classNames)
		sort.Strings(defaultClasses)
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf("storage class inventory: total=%d default=%s sample=%s", len(storageClasses.Items), joinOrNone(defaultClasses), joinOrNone(limitStrings(classNames, 6))),
		})
	}

	pvcs, err := a.Client.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list PersistentVolumeClaims: %v", err))
	} else {
		pending := []string{}
		for _, pvc := range pvcs.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(pvc.Namespace) {
				continue
			}
			if strings.EqualFold(string(pvc.Status.Phase), "Pending") {
				pending = append(pending, fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name))
			}
		}
		sort.Strings(pending)
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf("persistent volume claim inventory: total=%d pending=%d samplePending=%s", len(pvcs.Items), len(pending), joinOrNone(limitStrings(pending, 6))),
		})
		if len(pending) > 0 {
			res.Findings = append(res.Findings, model.Finding{
				Title:                 fmt.Sprintf("%d PersistentVolumeClaim(s) Pending", len(pending)),
				Severity:              model.SeverityHigh,
				Urgency:               model.UrgencyToday,
				Confidence:            model.ConfidenceHigh,
				AffectedScope:         "storage/pvc",
				WhyItMatters:          "Pending PVCs block pod scheduling and can silently stall rollouts, stateful recovery, and job execution even when compute capacity looks healthy.",
				AutomationSuitability: model.SuitabilityAdvisoryOnly,
			})
		}
	}

	networkPolicies, err := a.Client.NetworkingV1().NetworkPolicies("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list NetworkPolicies: %v", err))
	} else {
		count := 0
		namespacesWithPolicies := map[string]struct{}{}
		for _, np := range networkPolicies.Items {
			if !a.IncludeSystemNamespaces && capability.IsSystemNamespace(np.Namespace) {
				continue
			}
			count++
			namespacesWithPolicies[np.Namespace] = struct{}{}
		}
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf("network policy inventory: policies=%d protectedNamespaces=%d", count, len(namespacesWithPolicies)),
		})
		if userNamespaces > 0 && count == 0 {
			res.HiddenRisks = append(res.HiddenRisks, "No NetworkPolicies were detected for user namespaces; reliability incidents can spread faster when east-west traffic is unconstrained.")
		}
	}

	resourceQuotas, rqErr := a.Client.CoreV1().ResourceQuotas("").List(ctx, metav1.ListOptions{})
	if rqErr != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list ResourceQuotas: %v", rqErr))
	}
	limitRanges, lrErr := a.Client.CoreV1().LimitRanges("").List(ctx, metav1.ListOptions{})
	if lrErr != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list LimitRanges: %v", lrErr))
	}
	if rqErr == nil && lrErr == nil {
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf("policy inventory: ResourceQuotas=%d LimitRanges=%d", len(resourceQuotas.Items), len(limitRanges.Items)),
		})
	}

	pods, err := a.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list pods for platform component heuristics: %v", err))
	} else {
		componentSignals := capability.DetectPlatformComponents(pods.Items, a.IncludeSystemNamespaces)
		if len(componentSignals) > 0 {
			res.Evidence = append(res.Evidence, model.Evidence{
				Signal: fmt.Sprintf("observability/platform components detected by pod heuristic: %s", strings.Join(componentSignals, ", ")),
			})
		} else {
			res.Unknowns = append(res.Unknowns, "No monitoring/logging/tracing platform components were confirmed by API groups or common pod-name heuristics.")
		}
	}

	capabilityInventory := capability.BuildInventory(groupSet, serviceSignals, componentSignalsSetFromEvidence(res.Evidence))
	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("capability inventory: %s", capability.RenderInventory(capabilityInventory)),
	})
	appendCapabilityUnknowns(&res, capabilityInventory)

	if userNamespaces > 0 &&
		capabilityInventory["resource_metrics"] == capability.NotConfirmed &&
		capabilityInventory["time_series_metrics"] == capability.NotConfirmed &&
		capabilityInventory["logs_backend"] == capability.NotConfirmed {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 "Cluster observability coverage is not confirmed from discovery signals",
			Severity:              model.SeverityMedium,
			Urgency:               model.UrgencyThisWeek,
			Confidence:            model.ConfidenceMedium,
			AffectedScope:         "platform/observability",
			WhyItMatters:          "Without confirmed metric and log collection paths, the agent can detect some Kubernetes symptoms but cannot produce high-confidence production suggestions for saturation, latency, error trends, or pre-incident drift.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.Recommended.Preventive = append(res.Recommended.Preventive, model.Action{
			Title:               "Confirm or add cluster-wide telemetry entry points for metrics and logs",
			ExpectedBenefit:     "Enables evidence-based incident analysis and lets the agent reason about emerging production risks before user-visible failures.",
			RiskTradeoff:        "Adds operational overhead and requires decisions about retention, access control, and cost.",
			Priority:            "P2",
			ExecutionSafety:     model.SafetyNeedsOperatorApproval,
			ApprovalRequirement: model.ApprovalNeedsOperator,
			RollbackOutline:     "If new telemetry components are introduced, remove or scale them back after validating alternatives.",
		})
	}

	res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
		Rank:        4,
		Description: "Some production-readiness gaps may come from missing or unconfirmed platform capabilities (telemetry, autoscaling, storage readiness, policy coverage, traffic entry points) rather than from a single failing workload.",
		Probability: model.ConfidenceMedium,
		SupportingEvidence: []model.Evidence{
			{Signal: "Discovery inventory was used to infer capability classes that are detected, candidate-only, or not confirmed."},
		},
		ContradictoryOrMissing: []model.Evidence{
			{Signal: "Absence of a detected API group, service hint, or pod heuristic is not absolute proof that a capability is unavailable or unused."},
		},
		WhatToVerifyNext: []string{
			"Confirm which telemetry, autoscaling, traffic, and policy controls are actually installed and used in production",
			"Map critical workloads to entry points, storage, scaling signals, and disruption controls to find real coverage gaps",
		},
	})

	return res, nil
}

func derefInt32(v *int32) int32 {
	if v == nil {
		return 1
	}
	return *v
}

func limitStrings(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

func joinOrNone(in []string) string {
	if len(in) == 0 {
		return "none"
	}
	return strings.Join(in, ", ")
}

func appendCapabilityUnknowns(res *analyzer.Result, capabilities capability.Inventory) {
	if capabilities["resource_metrics"] == capability.NotConfirmed {
		res.Unknowns = append(res.Unknowns, "Resource metrics pipeline is not confirmed; CPU and memory saturation advice will remain snapshot-based.")
	}
	if capabilities["time_series_metrics"] == capability.NotConfirmed {
		res.Unknowns = append(res.Unknowns, "Time-series metrics backend is not confirmed; trend-based reliability analysis is limited.")
	}
	if capabilities["logs_backend"] == capability.NotConfirmed {
		res.Unknowns = append(res.Unknowns, "Cluster-wide log aggregation is not confirmed; pod log hints may miss cross-workload failure patterns.")
	}
	if capabilities["traces_backend"] == capability.NotConfirmed {
		res.Unknowns = append(res.Unknowns, "Distributed tracing backend is not confirmed; dependency-latency root cause analysis may be incomplete.")
	}
}

func componentSignalsSetFromEvidence(evidence []model.Evidence) map[string]struct{} {
	out := map[string]struct{}{}
	for _, e := range evidence {
		if !strings.HasPrefix(e.Signal, "observability/platform components detected by pod heuristic: ") {
			continue
		}
		payload := strings.TrimPrefix(e.Signal, "observability/platform components detected by pod heuristic: ")
		for _, item := range strings.Split(payload, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				out[item] = struct{}{}
			}
		}
	}
	return out
}
