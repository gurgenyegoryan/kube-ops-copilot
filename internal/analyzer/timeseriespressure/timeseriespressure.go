package timeseriespressure

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/telemetry"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

type Analyzer struct {
	Client                  *kube.Client
	Inventory               capability.Inventory
	IncludeSystemNamespaces bool
}

type workloadKey struct {
	Namespace string
	Kind      string
	Name      string
}

type structuralProfile struct {
	Key              workloadKey
	DesiredReplicas  int32
	ReadyEndpoints   int
	ServiceCount     int
	ExternalServices int
	HasHPA           bool
	HasPDB           bool
	MissingReadiness int
	MissingLiveness  int
	Labels           map[string]string
}

type workloadTrend struct {
	Key             workloadKey
	RestartIncrease float64
	CPUPeak         float64
	MemoryPeak      float64
	CPUThrottlePeak float64
	OOMIncrease     float64
}

type scoredWorkload struct {
	Trend   workloadTrend
	Profile structuralProfile
	Score   int
	Reasons []string
}

func (a *Analyzer) Name() string { return "timeseriespressure" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	res := analyzer.Result{}
	if a.Client == nil || a.Client.Kubernetes == nil {
		return res, fmt.Errorf("kubernetes client is nil")
	}

	backends, err := telemetry.Discover(ctx, a.Client, a.Inventory)
	if err != nil {
		return res, err
	}
	promBackends := telemetry.PrometheusBackends(backends)
	if len(promBackends) == 0 {
		res.Unknowns = append(res.Unknowns, "Time-series capability is inferred, but no Prometheus-compatible query service was discovered via Kubernetes Services in this run.")
		return res, nil
	}
	backend := promBackends[0]
	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("time-series adapter selected: vendor=%s service=%s/%s port=%s", backend.Vendor, backend.Namespace, backend.Service, backend.Port),
	})

	pods, err := a.Client.Kubernetes.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list pods: %w", err)
	}
	replicaSets, err := a.Client.Kubernetes.AppsV1().ReplicaSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list replicasets: %w", err)
	}
	profiles, err := a.structuralProfiles(ctx)
	if err != nil {
		return res, err
	}

	podOwners := buildPodOwners(pods.Items, replicaSets.Items, a.includeNamespace)
	trends := map[workloadKey]*workloadTrend{}
	now := time.Now().UTC()

	restartsQuery, err := telemetry.QueryPrometheusVectorFirst(ctx, a.Client, backend, restartExpressions())
	if err != nil {
		res.Unknowns = append(res.Unknowns, "Prometheus-compatible backend is reachable, but 24h restart trend query was not usable in this run.")
	} else {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "time-series query selected for restarts: " + restartsQuery.Expression})
		for _, sample := range restartsQuery.Samples {
			key := workloadForMetric(sample.Metric, podOwners)
			trend := ensureTrend(trends, key)
			if sample.Value > trend.RestartIncrease {
				trend.RestartIncrease = sample.Value
			}
		}
	}

	start := now.Add(-6 * time.Hour)
	cpuQuery, err := telemetry.QueryPrometheusRangeFirst(ctx, a.Client, backend, cpuExpressions(), start, now, 15*time.Minute)
	if err != nil {
		res.Unknowns = append(res.Unknowns, "Prometheus-compatible backend is reachable, but 6h CPU trend query was not usable in this run.")
	} else {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "time-series query selected for cpu: " + cpuQuery.Expression})
		for _, series := range cpuQuery.Series {
			key := workloadForMetric(series.Metric, podOwners)
			trend := ensureTrend(trends, key)
			if peak := peakMatrix(series); peak > trend.CPUPeak {
				trend.CPUPeak = peak
			}
		}
	}

	memQuery, err := telemetry.QueryPrometheusRangeFirst(ctx, a.Client, backend, memoryExpressions(), start, now, 15*time.Minute)
	if err != nil {
		res.Unknowns = append(res.Unknowns, "Prometheus-compatible backend is reachable, but 6h memory trend query was not usable in this run.")
	} else {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "time-series query selected for memory: " + memQuery.Expression})
		for _, series := range memQuery.Series {
			key := workloadForMetric(series.Metric, podOwners)
			trend := ensureTrend(trends, key)
			if peak := peakMatrix(series); peak > trend.MemoryPeak {
				trend.MemoryPeak = peak
			}
		}
	}

	throttleQuery, err := telemetry.QueryPrometheusRangeFirst(ctx, a.Client, backend, throttleExpressions(), start, now, 15*time.Minute)
	if err == nil {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "time-series query selected for throttling: " + throttleQuery.Expression})
		for _, series := range throttleQuery.Series {
			key := workloadForMetric(series.Metric, podOwners)
			trend := ensureTrend(trends, key)
			if peak := peakMatrix(series); peak > trend.CPUThrottlePeak {
				trend.CPUThrottlePeak = peak
			}
		}
	}

	oomQuery, err := telemetry.QueryPrometheusVectorFirst(ctx, a.Client, backend, oomExpressions())
	if err == nil {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "time-series query selected for oom: " + oomQuery.Expression})
		for _, sample := range oomQuery.Samples {
			key := workloadForMetric(sample.Metric, podOwners)
			trend := ensureTrend(trends, key)
			if sample.Value > trend.OOMIncrease {
				trend.OOMIncrease = sample.Value
			}
		}
	}

	scored := []scoredWorkload{}
	maxScore := 0
	for _, trend := range trends {
		profile := profiles[trend.Key]
		score, reasons := scoreTrend(*trend, profile)
		if score <= 0 {
			continue
		}
		item := scoredWorkload{Trend: *trend, Profile: profile, Score: score, Reasons: reasons}
		scored = append(scored, item)
		if score > maxScore {
			maxScore = score
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].Trend.Key.Namespace != scored[j].Trend.Key.Namespace {
			return scored[i].Trend.Key.Namespace < scored[j].Trend.Key.Namespace
		}
		if scored[i].Trend.Key.Kind != scored[j].Trend.Key.Kind {
			return scored[i].Trend.Key.Kind < scored[j].Trend.Key.Kind
		}
		return scored[i].Trend.Key.Name < scored[j].Trend.Key.Name
	})

	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("time-series pressure correlation: workloads=%d correlatedHotspots=%d maxScore=%d", len(trends), len(scored), maxScore),
	})
	limit := 5
	if len(scored) < limit {
		limit = len(scored)
	}
	for i := 0; i < limit; i++ {
		item := scored[i]
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf(
				"time-series correlated hotspot: %s/%s/%s score=%d restarts24h=%.0f cpuPeak=%.3fcores memoryPeak=%s throttlePeak=%.3f oom24h=%.0f services=%d externalServices=%d readyEndpoints=%d hasHPA=%t hasPDB=%t missingReadiness=%d missingLiveness=%d reasons=%s",
				item.Trend.Key.Namespace,
				item.Trend.Key.Kind,
				item.Trend.Key.Name,
				item.Score,
				item.Trend.RestartIncrease,
				item.Trend.CPUPeak,
				formatBytes(item.Trend.MemoryPeak),
				item.Trend.CPUThrottlePeak,
				item.Trend.OOMIncrease,
				item.Profile.ServiceCount,
				item.Profile.ExternalServices,
				item.Profile.ReadyEndpoints,
				item.Profile.HasHPA,
				item.Profile.HasPDB,
				item.Profile.MissingReadiness,
				item.Profile.MissingLiveness,
				strings.Join(item.Reasons, "; "),
			),
		})
	}

	if len(scored) == 0 || maxScore < 20 {
		return res, nil
	}

	res.Findings = append(res.Findings, model.Finding{
		Title:                 fmt.Sprintf("%d workload(s) combine long-window runtime pressure with structural fragility", len(scored)),
		Severity:              severityForScore(maxScore),
		Urgency:               urgencyForScore(maxScore),
		Confidence:            model.ConfidenceHigh,
		AffectedScope:         "workloads/time-series-correlation",
		WhyItMatters:          "These workloads are not only structurally fragile; long-window telemetry shows restart growth or sustained resource pressure. That combination is closer to a real incident path than static misconfiguration alone.",
		AutomationSuitability: model.SuitabilityAdvisoryOnly,
	})
	res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
		Rank:        1,
		Description: "The highest operational risk is where structural fragility and time-series runtime pressure overlap in the same workload.",
		Probability: model.ConfidenceHigh,
		SupportingEvidence: []model.Evidence{
			{Signal: fmt.Sprintf("correlatedHotspots=%d maxCorrelationScore=%d", len(scored), maxScore)},
		},
		ContradictoryOrMissing: []model.Evidence{
			{Signal: "Query success depends on Prometheus-compatible scrape coverage and metric naming"},
			{Signal: "Absolute CPU/memory peaks are not identical to business saturation without traffic/SLO context"},
			{Signal: "Fallback expressions reduce query fragility, but backend-specific recording rules can still vary between clusters"},
		},
		WhatToVerifyNext: []string{
			"Inspect the top correlated workload in the confirmed metrics backend and compare trend spikes to rollout, drain, and dependency timelines",
			"Check whether the smallest safe fix is request sizing, HPA floor, PDB, or probe hardening instead of a blind restart",
			"Verify whether the workload is user-facing or dependency-critical before choosing live mitigation or infra PR mode",
		},
	})
	res.Recommended.Immediate = append(res.Recommended.Immediate, model.Action{
		Title:               "Investigate the top correlated time-series hotspot before applying a live change",
		ExpectedBenefit:     "Focuses remediation on workloads that are both structurally weak and already showing longer-window runtime pressure.",
		RiskTradeoff:        "Low if kept read-only; acting on the wrong bottleneck is the main risk.",
		Priority:            "P0",
		ExecutionSafety:     model.SafetySafeAutoCandidate,
		ApprovalRequirement: model.ApprovalNone,
		RollbackOutline:     "N/A (read-only correlation first)",
	})
	res.HiddenRisks = append(res.HiddenRisks, "A workload can look acceptable in a single kubectl snapshot while still showing multi-hour pressure and restart drift in the time-series backend.")

	return res, nil
}

func (a *Analyzer) structuralProfiles(ctx context.Context) (map[workloadKey]structuralProfile, error) {
	deployments, err := a.Client.Kubernetes.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments for time-series correlation: %w", err)
	}
	statefulSets, err := a.Client.Kubernetes.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets for time-series correlation: %w", err)
	}
	services, err := a.Client.Kubernetes.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services for time-series correlation: %w", err)
	}
	ingresses, err := a.Client.Kubernetes.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list ingresses for time-series correlation: %w", err)
	}
	hpas, err := a.Client.Kubernetes.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list hpas for time-series correlation: %w", err)
	}
	pdbs, err := a.Client.Kubernetes.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pdbs for time-series correlation: %w", err)
	}
	endpointSlices, err := a.Client.Kubernetes.DiscoveryV1().EndpointSlices("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list endpointslices for time-series correlation: %w", err)
	}

	profiles := map[workloadKey]structuralProfile{}
	for _, d := range deployments.Items {
		if !a.includeNamespace(d.Namespace) {
			continue
		}
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		profiles[workloadKey{Namespace: d.Namespace, Kind: "Deployment", Name: d.Name}] = structuralProfile{
			Key:              workloadKey{Namespace: d.Namespace, Kind: "Deployment", Name: d.Name},
			DesiredReplicas:  replicas,
			Labels:           copyMap(d.Spec.Template.Labels),
			MissingReadiness: countMissingReadiness(d.Spec.Template.Spec.Containers),
			MissingLiveness:  countMissingLiveness(d.Spec.Template.Spec.Containers),
		}
	}
	for _, s := range statefulSets.Items {
		if !a.includeNamespace(s.Namespace) {
			continue
		}
		replicas := int32(1)
		if s.Spec.Replicas != nil {
			replicas = *s.Spec.Replicas
		}
		profiles[workloadKey{Namespace: s.Namespace, Kind: "StatefulSet", Name: s.Name}] = structuralProfile{
			Key:              workloadKey{Namespace: s.Namespace, Kind: "StatefulSet", Name: s.Name},
			DesiredReplicas:  replicas,
			Labels:           copyMap(s.Spec.Template.Labels),
			MissingReadiness: countMissingReadiness(s.Spec.Template.Spec.Containers),
			MissingLiveness:  countMissingLiveness(s.Spec.Template.Spec.Containers),
		}
	}

	hpaTargets := map[workloadKey]struct{}{}
	for _, hpa := range hpas.Items {
		if !a.includeNamespace(hpa.Namespace) {
			continue
		}
		hpaTargets[workloadKey{Namespace: hpa.Namespace, Kind: hpa.Spec.ScaleTargetRef.Kind, Name: hpa.Spec.ScaleTargetRef.Name}] = struct{}{}
	}
	for key, profile := range profiles {
		if _, ok := hpaTargets[key]; ok {
			profile.HasHPA = true
			profiles[key] = profile
		}
	}

	ingressRefs := collectIngressRefs(ingresses.Items, a.includeNamespace)
	endpointCounts := countReadyEndpoints(endpointSlices.Items, a.includeNamespace)
	for _, svc := range services.Items {
		if !a.includeNamespace(svc.Namespace) || len(svc.Spec.Selector) == 0 {
			continue
		}
		external := svc.Spec.Type == corev1.ServiceTypeLoadBalancer || svc.Spec.Type == corev1.ServiceTypeNodePort || ingressRefs[namespacedName(svc.Namespace, svc.Name)] > 0
		ready := endpointCounts[namespacedName(svc.Namespace, svc.Name)]
		for key, profile := range profiles {
			if key.Namespace != svc.Namespace {
				continue
			}
			if !selectorMatches(svc.Spec.Selector, profile.Labels) {
				continue
			}
			profile.ServiceCount++
			profile.ReadyEndpoints += ready
			if external {
				profile.ExternalServices++
			}
			profiles[key] = profile
		}
	}

	for _, pdb := range pdbs.Items {
		if !a.includeNamespace(pdb.Namespace) {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || sel.Empty() {
			continue
		}
		for key, profile := range profiles {
			if key.Namespace != pdb.Namespace {
				continue
			}
			if sel.Matches(labels.Set(profile.Labels)) {
				profile.HasPDB = true
				profiles[key] = profile
			}
		}
	}

	return profiles, nil
}

func buildPodOwners(pods []corev1.Pod, replicaSets []appsv1.ReplicaSet, include func(string) bool) map[string]workloadKey {
	rsOwners := map[string]workloadKey{}
	for _, rs := range replicaSets {
		if !include(rs.Namespace) {
			continue
		}
		key := namespacedName(rs.Namespace, rs.Name)
		owner := controllerOwner(rs.OwnerReferences)
		if owner != nil && owner.Kind == "Deployment" {
			rsOwners[key] = workloadKey{Namespace: rs.Namespace, Kind: owner.Kind, Name: owner.Name}
			continue
		}
		rsOwners[key] = workloadKey{Namespace: rs.Namespace, Kind: "ReplicaSet", Name: rs.Name}
	}

	podOwners := map[string]workloadKey{}
	for _, pod := range pods {
		if !include(pod.Namespace) {
			continue
		}
		ref := controllerOwner(pod.OwnerReferences)
		switch {
		case ref == nil:
			podOwners[namespacedName(pod.Namespace, pod.Name)] = workloadKey{Namespace: pod.Namespace, Kind: "Pod", Name: pod.Name}
		case ref.Kind == "ReplicaSet":
			if owner, ok := rsOwners[namespacedName(pod.Namespace, ref.Name)]; ok {
				podOwners[namespacedName(pod.Namespace, pod.Name)] = owner
			} else {
				podOwners[namespacedName(pod.Namespace, pod.Name)] = workloadKey{Namespace: pod.Namespace, Kind: "ReplicaSet", Name: ref.Name}
			}
		default:
			podOwners[namespacedName(pod.Namespace, pod.Name)] = workloadKey{Namespace: pod.Namespace, Kind: ref.Kind, Name: ref.Name}
		}
	}
	return podOwners
}

func workloadForMetric(metric map[string]string, podOwners map[string]workloadKey) workloadKey {
	ns, pod := telemetry.NormalizeNamespacePodLabels(metric)
	if ns == "" || pod == "" {
		return workloadKey{Namespace: ns, Kind: "Workload", Name: pod}
	}
	if owner, ok := podOwners[namespacedName(ns, pod)]; ok {
		return owner
	}
	return workloadKey{Namespace: ns, Kind: "Pod", Name: pod}
}

func ensureTrend(trends map[workloadKey]*workloadTrend, key workloadKey) *workloadTrend {
	if existing, ok := trends[key]; ok {
		return existing
	}
	trend := &workloadTrend{Key: key}
	trends[key] = trend
	return trend
}

func peakMatrix(series telemetry.MatrixSeries) float64 {
	peak := 0.0
	for _, point := range series.Values {
		if point.Value > peak {
			peak = point.Value
		}
	}
	return peak
}

func scoreTrend(trend workloadTrend, profile structuralProfile) (int, []string) {
	score := 0
	reasons := []string{}
	if trend.RestartIncrease >= 5 {
		score += 20
		reasons = append(reasons, fmt.Sprintf("24h restart increase %.0f", trend.RestartIncrease))
	} else if trend.RestartIncrease >= 1 {
		score += 8
		reasons = append(reasons, fmt.Sprintf("24h restart increase %.0f", trend.RestartIncrease))
	}
	if trend.CPUPeak >= 2 {
		score += 15
		reasons = append(reasons, fmt.Sprintf("6h CPU peak %.2f cores", trend.CPUPeak))
	} else if trend.CPUPeak >= 0.8 {
		score += 8
		reasons = append(reasons, fmt.Sprintf("6h CPU peak %.2f cores", trend.CPUPeak))
	}
	if trend.MemoryPeak >= 2*1024*1024*1024 {
		score += 15
		reasons = append(reasons, fmt.Sprintf("6h memory peak %s", formatBytes(trend.MemoryPeak)))
	} else if trend.MemoryPeak >= 1*1024*1024*1024 {
		score += 8
		reasons = append(reasons, fmt.Sprintf("6h memory peak %s", formatBytes(trend.MemoryPeak)))
	}
	if trend.CPUThrottlePeak >= 0.2 {
		score += 14
		reasons = append(reasons, fmt.Sprintf("5m CPU throttling peak %.3f", trend.CPUThrottlePeak))
	} else if trend.CPUThrottlePeak >= 0.05 {
		score += 6
		reasons = append(reasons, fmt.Sprintf("5m CPU throttling peak %.3f", trend.CPUThrottlePeak))
	}
	if trend.OOMIncrease >= 1 {
		score += 20
		reasons = append(reasons, fmt.Sprintf("24h OOM terminations %.0f", trend.OOMIncrease))
	}
	if profile.ServiceCount > 0 && profile.DesiredReplicas == 1 {
		score += 8
		reasons = append(reasons, "single replica behind a service")
	}
	if profile.ExternalServices > 0 && profile.DesiredReplicas == 1 {
		score += 12
		reasons = append(reasons, "externally exposed single replica")
	}
	if profile.ExternalServices > 0 && !profile.HasHPA {
		score += 8
		reasons = append(reasons, "externally exposed without HPA")
	}
	if profile.ServiceCount > 0 && profile.DesiredReplicas > 1 && !profile.HasPDB {
		score += 6
		reasons = append(reasons, "multi-replica service has no PDB")
	}
	if profile.MissingReadiness > 0 {
		score += 6
		reasons = append(reasons, fmt.Sprintf("missing readiness on %d container(s)", profile.MissingReadiness))
	}
	if profile.MissingLiveness > 0 {
		score += 4
		reasons = append(reasons, fmt.Sprintf("missing liveness on %d container(s)", profile.MissingLiveness))
	}
	if trend.RestartIncrease > 0 && profile.ExternalServices > 0 && profile.DesiredReplicas == 1 {
		score += 15
		reasons = append(reasons, "restart growth overlaps with external single-replica exposure")
	}
	if profile.ExternalServices > 0 && profile.ReadyEndpoints == 0 {
		score += 20
		reasons = append(reasons, "external service has zero ready endpoints")
	}
	return score, reasons
}

func severityForScore(score int) model.Severity {
	if score >= 55 {
		return model.SeverityHigh
	}
	return model.SeverityMedium
}

func urgencyForScore(score int) model.Urgency {
	if score >= 55 {
		return model.UrgencyImmediate
	}
	return model.UrgencyToday
}

func collectIngressRefs(items []networkingv1.Ingress, include func(string) bool) map[string]int {
	refs := map[string]int{}
	for _, ing := range items {
		if !include(ing.Namespace) {
			continue
		}
		if ing.Spec.DefaultBackend != nil && ing.Spec.DefaultBackend.Service != nil {
			refs[namespacedName(ing.Namespace, ing.Spec.DefaultBackend.Service.Name)]++
		}
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if path.Backend.Service == nil {
					continue
				}
				refs[namespacedName(ing.Namespace, path.Backend.Service.Name)]++
			}
		}
	}
	return refs
}

func countReadyEndpoints(items []discoveryv1.EndpointSlice, include func(string) bool) map[string]int {
	ready := map[string]int{}
	for _, slice := range items {
		if !include(slice.Namespace) {
			continue
		}
		service := slice.Labels["kubernetes.io/service-name"]
		if service == "" {
			continue
		}
		key := namespacedName(slice.Namespace, service)
		for _, ep := range slice.Endpoints {
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				ready[key]++
			}
		}
	}
	return ready
}

func selectorMatches(selector map[string]string, labels map[string]string) bool {
	if len(selector) == 0 || len(labels) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
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

func countMissingReadiness(containers []corev1.Container) int {
	missing := 0
	for _, c := range containers {
		if c.ReadinessProbe == nil {
			missing++
		}
	}
	return missing
}

func countMissingLiveness(containers []corev1.Container) int {
	missing := 0
	for _, c := range containers {
		if c.LivenessProbe == nil {
			missing++
		}
	}
	return missing
}

func namespacedName(ns, name string) string {
	return ns + "/" + name
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func formatBytes(v float64) string {
	switch {
	case v >= 1024*1024*1024:
		return fmt.Sprintf("%.2fGi", v/1024/1024/1024)
	case v >= 1024*1024:
		return fmt.Sprintf("%.0fMi", v/1024/1024)
	case v > 0:
		return fmt.Sprintf("%.0fB", v)
	default:
		return "0"
	}
}

func (a *Analyzer) includeNamespace(ns string) bool {
	return a.IncludeSystemNamespaces || !capability.IsSystemNamespace(ns)
}

func restartExpressions() []string {
	out := []string{}
	for _, pair := range telemetry.NamespacePodLabelPairs() {
		out = append(out,
			fmt.Sprintf(`topk(10, sum by (%s,%s) (increase(kube_pod_container_status_restarts_total[24h])))`, pair[0], pair[1]),
			fmt.Sprintf(`topk(10, sum by (%s,%s) (changes(kube_pod_container_status_restarts_total[24h])))`, pair[0], pair[1]),
		)
	}
	return dedupeExpressions(out)
}

func cpuExpressions() []string {
	out := []string{}
	for _, pair := range telemetry.NamespacePodLabelPairs() {
		out = append(out,
			fmt.Sprintf(`sum by (%s,%s) (rate(container_cpu_usage_seconds_total{container!="",%s!=""}[5m]))`, pair[0], pair[1], pair[1]),
			fmt.Sprintf(`sum by (%s,%s) (rate(container_cpu_usage_seconds_total{image!="",%s!=""}[5m]))`, pair[0], pair[1], pair[1]),
		)
	}
	out = append(out, `sum by (namespace,pod) (node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate)`)
	return dedupeExpressions(out)
}

func memoryExpressions() []string {
	out := []string{}
	for _, pair := range telemetry.NamespacePodLabelPairs() {
		out = append(out,
			fmt.Sprintf(`sum by (%s,%s) (container_memory_working_set_bytes{container!="",%s!=""})`, pair[0], pair[1], pair[1]),
			fmt.Sprintf(`sum by (%s,%s) (container_memory_usage_bytes{container!="",%s!=""})`, pair[0], pair[1], pair[1]),
		)
	}
	out = append(out, `sum by (namespace,pod) (node_namespace_pod_container:container_memory_working_set_bytes)`)
	return dedupeExpressions(out)
}

func throttleExpressions() []string {
	out := []string{}
	for _, pair := range telemetry.NamespacePodLabelPairs() {
		out = append(out,
			fmt.Sprintf(`sum by (%s,%s) (rate(container_cpu_cfs_throttled_seconds_total{container!="",%s!=""}[5m]))`, pair[0], pair[1], pair[1]),
			fmt.Sprintf(`sum by (%s,%s) (rate(container_cpu_cfs_throttled_seconds_total{image!="",%s!=""}[5m]))`, pair[0], pair[1], pair[1]),
		)
	}
	return dedupeExpressions(out)
}

func oomExpressions() []string {
	out := []string{}
	for _, pair := range telemetry.NamespacePodLabelPairs() {
		out = append(out,
			fmt.Sprintf(`topk(10, sum by (%s,%s) (increase(kube_pod_container_status_last_terminated_reason{reason="OOMKilled"}[24h])))`, pair[0], pair[1]),
			fmt.Sprintf(`topk(10, sum by (%s,%s) (increase(kube_pod_container_status_terminated_reason{reason="OOMKilled"}[24h])))`, pair[0], pair[1]),
		)
	}
	return dedupeExpressions(out)
}

func dedupeExpressions(expressions []string) []string {
	out := make([]string, 0, len(expressions))
	seen := map[string]struct{}{}
	for _, expr := range expressions {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		if _, ok := seen[expr]; ok {
			continue
		}
		seen[expr] = struct{}{}
		out = append(out, expr)
	}
	return out
}
