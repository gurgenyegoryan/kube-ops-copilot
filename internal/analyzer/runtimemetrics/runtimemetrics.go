package runtimemetrics

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type Analyzer struct {
	Client                  *kube.Client
	IncludeSystemNamespaces bool
}

type workloadKey struct {
	Namespace string
	Kind      string
	Name      string
}

type workloadRuntime struct {
	Key                 workloadKey
	Pods                int
	CPUUsageMilli       int64
	MemoryUsageBytes    int64
	CPURequestMilli     int64
	MemoryRequestBytes  int64
	PodsWithoutRequests int
	PodsWithoutMemReq   int
}

type scoredRuntime struct {
	Runtime workloadRuntime
	Score   int
	Reasons []string
}

var podMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

func (a *Analyzer) Name() string { return "runtimemetrics" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	res := analyzer.Result{}
	if a.Client == nil || a.Client.Kubernetes == nil || a.Client.RESTConfig == nil {
		res.Unknowns = append(res.Unknowns, "Runtime metrics adapter could not initialize because Kubernetes REST configuration is unavailable.")
		return res, nil
	}

	dc, err := dynamic.NewForConfig(a.Client.RESTConfig)
	if err != nil {
		res.Unknowns = append(res.Unknowns, "Resource metrics API was detected, but the runtime metrics adapter could not create a dynamic client.")
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "runtime metrics adapter init failed: " + strings.TrimSpace(err.Error())})
		return res, nil
	}

	podMetricsList, err := dc.Resource(podMetricsGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, "metrics.k8s.io is detected, but live pod metrics could not be queried in this run.")
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "runtime metrics query failed: " + strings.TrimSpace(err.Error())})
		return res, nil
	}

	pods, err := a.Client.Kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list pods for runtime metrics: %w", err)
	}
	replicaSets, err := a.Client.Kubernetes.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list replicasets for runtime metrics: %w", err)
	}

	podIndex := map[string]corev1.Pod{}
	for _, pod := range pods.Items {
		if !a.includeNamespace(pod.Namespace) {
			continue
		}
		podIndex[namespacedName(pod.Namespace, pod.Name)] = pod
	}
	replicaSetOwners := collectReplicaSetOwners(replicaSets.Items, a.includeNamespace)

	workloads := map[workloadKey]*workloadRuntime{}
	for _, item := range podMetricsList.Items {
		ns := item.GetNamespace()
		if !a.includeNamespace(ns) {
			continue
		}
		name := item.GetName()
		pod, ok := podIndex[namespacedName(ns, name)]
		if !ok {
			continue
		}
		key := resolveWorkloadKey(pod, replicaSetOwners)
		usageCPU, usageMem := usageFromMetricsItem(item)
		reqCPU, reqMem := podRequests(pod)

		r := workloads[key]
		if r == nil {
			r = &workloadRuntime{Key: key}
			workloads[key] = r
		}
		r.Pods++
		r.CPUUsageMilli += usageCPU
		r.MemoryUsageBytes += usageMem
		r.CPURequestMilli += reqCPU
		r.MemoryRequestBytes += reqMem
		if reqCPU == 0 {
			r.PodsWithoutRequests++
		}
		if reqMem == 0 {
			r.PodsWithoutMemReq++
		}
	}

	scored := make([]scoredRuntime, 0, len(workloads))
	maxScore := 0
	for _, runtime := range workloads {
		score, reasons := scoreRuntime(*runtime)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredRuntime{Runtime: *runtime, Score: score, Reasons: reasons})
		if score > maxScore {
			maxScore = score
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].Runtime.Key.Namespace != scored[j].Runtime.Key.Namespace {
			return scored[i].Runtime.Key.Namespace < scored[j].Runtime.Key.Namespace
		}
		if scored[i].Runtime.Key.Kind != scored[j].Runtime.Key.Kind {
			return scored[i].Runtime.Key.Kind < scored[j].Runtime.Key.Kind
		}
		return scored[i].Runtime.Key.Name < scored[j].Runtime.Key.Name
	})

	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("runtime metrics adapter: queried pod metrics for %d pods across %d workload(s) maxHotspotScore=%d", len(podMetricsList.Items), len(workloads), maxScore),
	})

	if len(scored) == 0 {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "runtime metrics adapter: no request-backed CPU/memory hotspots detected in current snapshot"})
		res.Unknowns = append(res.Unknowns, "Runtime metrics are point-in-time snapshots; bursts and trends still need time-series confirmation when a metrics backend is available.")
		return res, nil
	}

	limit := 5
	if len(scored) < limit {
		limit = len(scored)
	}
	for i := 0; i < limit; i++ {
		item := scored[i]
		r := item.Runtime
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf(
				"runtime hotspot: %s/%s/%s score=%d pods=%d cpu=%s cpuRequests=%s memory=%s memoryRequests=%s podsMissingCPURequests=%d podsMissingMemoryRequests=%d reasons=%s",
				r.Key.Namespace,
				r.Key.Kind,
				r.Key.Name,
				item.Score,
				r.Pods,
				formatCPU(r.CPUUsageMilli),
				formatCPU(r.CPURequestMilli),
				formatBytes(r.MemoryUsageBytes),
				formatBytes(r.MemoryRequestBytes),
				r.PodsWithoutRequests,
				r.PodsWithoutMemReq,
				strings.Join(item.Reasons, "; "),
			),
		})
	}

	res.Findings = append(res.Findings, model.Finding{
		Title:                 fmt.Sprintf("%d workload(s) are running hot against live CPU/memory requests", len(scored)),
		Severity:              runtimeSeverity(maxScore),
		Urgency:               runtimeUrgency(maxScore),
		Confidence:            model.ConfidenceHigh,
		AffectedScope:         "workloads/runtime-metrics",
		WhyItMatters:          "This is live runtime pressure, not only static config drift. Workloads already running close to or above declared requests are more likely to degrade during bursts, node pressure, or rollouts.",
		AutomationSuitability: model.SuitabilityAdvisoryOnly,
	})

	res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
		Rank:        1,
		Description: "At least one workload is already near or beyond its declared runtime envelope, so scaling or request sizing decisions should be driven by confirmed live metrics rather than static manifest heuristics alone.",
		Probability: model.ConfidenceHigh,
		SupportingEvidence: []model.Evidence{
			{Signal: fmt.Sprintf("runtimeHotspots=%d maxHotspotScore=%d", len(scored), maxScore)},
		},
		ContradictoryOrMissing: []model.Evidence{
			{Signal: "metrics.k8s.io is a short-window snapshot and does not replace time-series history"},
			{Signal: "Workloads without requests reduce the precision of saturation analysis"},
		},
		WhatToVerifyNext: []string{
			"Inspect the top hotspot pods with `kubectl top pods -A --containers` and compare against declared requests",
			"Confirm whether pressure is steady or bursty before changing replica count, HPA, or requests",
			"Prefer fixing the smallest dominant bottleneck first: request sizing, HPA policy, or dependency latency",
		},
	})

	res.Recommended.Immediate = append(res.Recommended.Immediate, model.Action{
		Title:               "Validate the top runtime hotspots before scaling or restarting workloads",
		ExpectedBenefit:     "Prevents blind remediation by separating true saturation from a transient or underspecified request baseline.",
		RiskTradeoff:        "Low; read-only validation first. The main risk is reacting too quickly to a single metrics snapshot.",
		Priority:            "P0",
		ExecutionSafety:     model.SafetySafeAutoCandidate,
		ApprovalRequirement: model.ApprovalNone,
		RollbackOutline:     "N/A (no change yet)",
	})

	res.HiddenRisks = append(res.HiddenRisks, "Point-in-time runtime pressure often appears before user-visible failure, but a single snapshot can also miss burst patterns; confirm with trends when available.")
	res.Unknowns = append(res.Unknowns, "Live runtime enrichment currently uses the Kubernetes resource metrics API. If Prometheus or another time-series backend exists, long-window trend confirmation should still be added for stronger confidence.")

	return res, nil
}

func collectReplicaSetOwners(items []appsv1.ReplicaSet, include func(string) bool) map[string]workloadKey {
	owners := map[string]workloadKey{}
	for _, rs := range items {
		if !include(rs.Namespace) {
			continue
		}
		key := namespacedName(rs.Namespace, rs.Name)
		ref := controllerOwner(rs.OwnerReferences)
		if ref != nil && ref.Kind == "Deployment" {
			owners[key] = workloadKey{Namespace: rs.Namespace, Kind: ref.Kind, Name: ref.Name}
			continue
		}
		owners[key] = workloadKey{Namespace: rs.Namespace, Kind: "ReplicaSet", Name: rs.Name}
	}
	return owners
}

func resolveWorkloadKey(pod corev1.Pod, replicaSetOwners map[string]workloadKey) workloadKey {
	ref := controllerOwner(pod.OwnerReferences)
	if ref == nil {
		return workloadKey{Namespace: pod.Namespace, Kind: "Pod", Name: pod.Name}
	}
	if ref.Kind == "ReplicaSet" {
		if owner, ok := replicaSetOwners[namespacedName(pod.Namespace, ref.Name)]; ok {
			return owner
		}
	}
	return workloadKey{Namespace: pod.Namespace, Kind: ref.Kind, Name: ref.Name}
}

func controllerOwner(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		ref := refs[i]
		if ref.Controller != nil && *ref.Controller {
			return &ref
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return &refs[0]
}

func usageFromMetricsItem(item unstructured.Unstructured) (int64, int64) {
	containers, _, err := unstructured.NestedSlice(item.Object, "containers")
	if err != nil {
		return 0, 0
	}
	var cpuMilli int64
	var memBytes int64
	for _, container := range containers {
		obj, ok := container.(map[string]any)
		if !ok {
			continue
		}
		usage, ok := obj["usage"].(map[string]any)
		if !ok {
			continue
		}
		if cpu, ok := usage["cpu"].(string); ok {
			q, err := resource.ParseQuantity(cpu)
			if err == nil {
				cpuMilli += q.MilliValue()
			}
		}
		if memory, ok := usage["memory"].(string); ok {
			q, err := resource.ParseQuantity(memory)
			if err == nil {
				memBytes += q.Value()
			}
		}
	}
	return cpuMilli, memBytes
}

func podRequests(pod corev1.Pod) (int64, int64) {
	var cpuMilli int64
	var memBytes int64
	for _, c := range pod.Spec.Containers {
		req := c.Resources.Requests
		if req == nil {
			continue
		}
		cpuMilli += req.Cpu().MilliValue()
		memBytes += req.Memory().Value()
	}
	return cpuMilli, memBytes
}

func scoreRuntime(runtime workloadRuntime) (int, []string) {
	score := 0
	reasons := []string{}

	if ratio := safeRatio(runtime.CPUUsageMilli, runtime.CPURequestMilli); ratio >= 1.2 {
		score += 35
		reasons = append(reasons, fmt.Sprintf("CPU usage is %.0f%% of declared requests", ratio*100))
	} else if ratio >= 0.9 && runtime.CPUUsageMilli >= 100 {
		score += 18
		reasons = append(reasons, fmt.Sprintf("CPU usage is %.0f%% of declared requests", ratio*100))
	}

	if ratio := safeRatio(runtime.MemoryUsageBytes, runtime.MemoryRequestBytes); ratio >= 0.95 {
		score += 35
		reasons = append(reasons, fmt.Sprintf("memory usage is %.0f%% of declared requests", ratio*100))
	} else if ratio >= 0.85 && runtime.MemoryUsageBytes >= 128*1024*1024 {
		score += 18
		reasons = append(reasons, fmt.Sprintf("memory usage is %.0f%% of declared requests", ratio*100))
	}

	if runtime.CPURequestMilli == 0 && runtime.CPUUsageMilli >= 500 {
		score += 8
		reasons = append(reasons, "high CPU usage without declared CPU requests")
	}
	if runtime.MemoryRequestBytes == 0 && runtime.MemoryUsageBytes >= 512*1024*1024 {
		score += 8
		reasons = append(reasons, "high memory usage without declared memory requests")
	}

	return score, reasons
}

func runtimeSeverity(maxScore int) model.Severity {
	if maxScore >= 60 {
		return model.SeverityHigh
	}
	return model.SeverityMedium
}

func runtimeUrgency(maxScore int) model.Urgency {
	if maxScore >= 60 {
		return model.UrgencyImmediate
	}
	return model.UrgencyToday
}

func safeRatio(usage, request int64) float64 {
	if usage <= 0 || request <= 0 {
		return 0
	}
	return float64(usage) / float64(request)
}

func formatCPU(milli int64) string {
	if milli <= 0 {
		return "0"
	}
	return fmt.Sprintf("%dm", milli)
}

func formatBytes(bytes int64) string {
	if bytes <= 0 {
		return "0"
	}
	q := resource.NewQuantity(bytes, resource.BinarySI)
	return q.String()
}

func namespacedName(ns, name string) string {
	return ns + "/" + name
}

func (a *Analyzer) includeNamespace(ns string) bool {
	return a.IncludeSystemNamespaces || !capability.IsSystemNamespace(ns)
}
