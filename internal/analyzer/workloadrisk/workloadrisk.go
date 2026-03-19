package workloadrisk

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

type workloadKey struct {
	Namespace string
	Kind      string
	Name      string
}

type workloadProfile struct {
	Key              workloadKey
	DesiredReplicas  int32
	ReadyReplicas    int32
	Labels           map[string]string
	ServiceCount     int
	ExternalServices int
	IngressCount     int
	ReadyEndpoints   int
	HasHPA           bool
	HasPDB           bool
	MissingRequests  int
	MissingLimits    int
	MissingReadiness int
	MissingLiveness  int
	Stateful         bool
	PVCBacked        bool
}

type scoredWorkload struct {
	Profile workloadProfile
	Score   int
	Reasons []string
}

func (a *Analyzer) Name() string { return "workloadrisk" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}

	res := analyzer.Result{}

	deployments, err := a.Client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list deployments: %w", err)
	}
	statefulSets, err := a.Client.AppsV1().StatefulSets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list statefulsets: %w", err)
	}
	services, err := a.Client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list services: %w", err)
	}
	ingresses, err := a.Client.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list ingresses: %w", err)
	}
	endpointSlices, err := a.Client.DiscoveryV1().EndpointSlices("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list endpointslices: %w", err)
	}
	hpas, err := a.Client.AutoscalingV2().HorizontalPodAutoscalers("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list hpas: %w", err)
	}
	pdbs, err := a.Client.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list pdbs: %w", err)
	}

	profiles := map[workloadKey]*workloadProfile{}
	for _, d := range deployments.Items {
		if !a.includeNamespace(d.Namespace) {
			continue
		}
		key := workloadKey{Namespace: d.Namespace, Kind: "Deployment", Name: d.Name}
		profiles[key] = newDeploymentProfile(d)
	}
	for _, s := range statefulSets.Items {
		if !a.includeNamespace(s.Namespace) {
			continue
		}
		key := workloadKey{Namespace: s.Namespace, Kind: "StatefulSet", Name: s.Name}
		profiles[key] = newStatefulSetProfile(s)
	}

	hpaTargets := collectHPATargets(hpas.Items, a.includeNamespace)
	for key := range hpaTargets {
		if p, ok := profiles[key]; ok {
			p.HasHPA = true
		}
	}

	serviceIngressRefs := collectIngressBackends(ingresses.Items, a.includeNamespace)
	serviceReadyEndpoints := countServiceReadyEndpoints(endpointSlices.Items, a.includeNamespace)
	for _, svc := range services.Items {
		if !a.includeNamespace(svc.Namespace) || len(svc.Spec.Selector) == 0 {
			continue
		}
		external := isExternallyExposedService(svc, serviceIngressRefs)
		ready := serviceReadyEndpoints[namespacedName(svc.Namespace, svc.Name)]
		ingressCount := serviceIngressRefs[namespacedName(svc.Namespace, svc.Name)]
		for _, p := range profiles {
			if p.Key.Namespace != svc.Namespace {
				continue
			}
			if !selectorMatches(svc.Spec.Selector, p.Labels) {
				continue
			}
			p.ServiceCount++
			p.ReadyEndpoints += ready
			p.IngressCount += ingressCount
			if external {
				p.ExternalServices++
			}
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
		for _, p := range profiles {
			if p.Key.Namespace != pdb.Namespace {
				continue
			}
			if sel.Matches(labels.Set(p.Labels)) {
				p.HasPDB = true
			}
		}
	}

	scored := make([]scoredWorkload, 0, len(profiles))
	maxScore := 0
	highRisk := 0
	exposedZeroReady := 0
	for _, p := range profiles {
		score, reasons := scoreProfile(*p)
		if score <= 0 {
			continue
		}
		item := scoredWorkload{Profile: *p, Score: score, Reasons: reasons}
		scored = append(scored, item)
		if score >= 30 {
			highRisk++
		}
		if p.ExternalServices > 0 && p.ReadyEndpoints == 0 {
			exposedZeroReady++
		}
		if score > maxScore {
			maxScore = score
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].Profile.Key.Namespace != scored[j].Profile.Key.Namespace {
			return scored[i].Profile.Key.Namespace < scored[j].Profile.Key.Namespace
		}
		if scored[i].Profile.Key.Kind != scored[j].Profile.Key.Kind {
			return scored[i].Profile.Key.Kind < scored[j].Profile.Key.Kind
		}
		return scored[i].Profile.Key.Name < scored[j].Profile.Key.Name
	})

	res.Evidence = append(res.Evidence, model.Evidence{
		Signal: fmt.Sprintf("workload risk analysis: workloads=%d profiled=%d highRisk=%d maxScore=%d exposedZeroReady=%d", len(profiles), len(scored), highRisk, maxScore, exposedZeroReady),
	})

	limit := 5
	if len(scored) < limit {
		limit = len(scored)
	}
	for i := 0; i < limit; i++ {
		item := scored[i]
		p := item.Profile
		res.Evidence = append(res.Evidence, model.Evidence{
			Signal: fmt.Sprintf(
				"top workload risk: %s/%s/%s score=%d services=%d externalServices=%d ingresses=%d replicas=%d readyReplicas=%d readyEndpoints=%d hpa=%t pdb=%t missingReadiness=%d missingLiveness=%d missingRequests=%d missingLimits=%d stateful=%t pvcBacked=%t reasons=%s",
				p.Key.Namespace,
				p.Key.Kind,
				p.Key.Name,
				item.Score,
				p.ServiceCount,
				p.ExternalServices,
				p.IngressCount,
				p.DesiredReplicas,
				p.ReadyReplicas,
				p.ReadyEndpoints,
				p.HasHPA,
				p.HasPDB,
				p.MissingReadiness,
				p.MissingLiveness,
				p.MissingRequests,
				p.MissingLimits,
				p.Stateful,
				p.PVCBacked,
				strings.Join(item.Reasons, "; "),
			),
		})
	}

	if len(scored) == 0 || maxScore < 20 {
		return res, nil
	}

	res.Findings = append(res.Findings, model.Finding{
		Title:                 fmt.Sprintf("Compound workload risk is concentrated in %d workload(s)", highRiskOrScored(highRisk, len(scored))),
		Severity:              workloadSeverity(maxScore, exposedZeroReady),
		Urgency:               workloadUrgency(maxScore, exposedZeroReady),
		Confidence:            workloadConfidence(maxScore),
		AffectedScope:         "workloads/risk-profile",
		WhyItMatters:          "These workloads combine multiple weak signals such as traffic exposure, low replica count, missing autoscaling/disruption controls, or incomplete health/resource guardrails. That combination often stays quiet until a drain, rollout, burst, or dependency flap turns it into a user-facing incident.",
		AutomationSuitability: model.SuitabilityAdvisoryOnly,
	})

	res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
		Rank:        2,
		Description: "The highest near-term reliability risk is concentrated in a small set of workloads with compound resilience gaps rather than a single cluster-wide outage pattern.",
		Probability: workloadConfidence(maxScore),
		SupportingEvidence: []model.Evidence{
			{Signal: fmt.Sprintf("highRiskWorkloads=%d maxRiskScore=%d", highRisk, maxScore)},
			{Signal: fmt.Sprintf("exposedZeroReadyWorkloads=%d", exposedZeroReady)},
		},
		ContradictoryOrMissing: []model.Evidence{
			{Signal: "Kubernetes objects do not reveal business criticality, request volume, or SLO importance for each workload"},
			{Signal: "Confirmed metrics/logs/traces are still needed to validate whether these risk concentrations are already producing latency, errors, or saturation"},
		},
		WhatToVerifyNext: []string{
			"For the top workload: `kubectl -n <ns> describe deploy/<name>` or `kubectl -n <ns> describe statefulset/<name>`",
			"Trace traffic and readiness path: `kubectl -n <ns> get svc,endpointslices,ingress`",
			"Check whether the right durable fix is replica floor, HPA, PDB, probe hardening, or resource sizing before changing anything live",
		},
	})

	res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
		Title:               "Prioritize the top correlated workload risks instead of treating each missing control in isolation",
		ExpectedBenefit:     "Focuses operator time on the small set of workloads most likely to produce user-visible incidents during drains, rollouts, or bursts.",
		RiskTradeoff:        "Requires workload-level validation so the wrong guardrail is not added to a stateful or dependency-sensitive service.",
		Priority:            "P1",
		ExecutionSafety:     model.SafetyNeedsOperatorApproval,
		ApprovalRequirement: model.ApprovalNeedsOperator,
		RollbackOutline:     "Apply only one guardrail change at a time and revert the specific replica/HPA/PDB/probe/resource change if it degrades behavior.",
	})

	res.HiddenRisks = append(res.HiddenRisks, "Compound workload risk is easy to miss in dashboards because each individual signal can look non-critical until a rollout, node drain, or traffic burst lines them up.")
	res.Unknowns = append(res.Unknowns, "This workload risk model is structural: it knows cluster shape and guardrails, but it still needs confirmed telemetry to rank user impact with higher precision.")

	return res, nil
}

func newDeploymentProfile(d appsv1.Deployment) *workloadProfile {
	replicas := int32(1)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	missingRequests, missingLimits, missingReadiness, missingLiveness, pvcBacked := summarizeTemplate(d.Spec.Template)
	return &workloadProfile{
		Key: workloadKey{
			Namespace: d.Namespace,
			Kind:      "Deployment",
			Name:      d.Name,
		},
		DesiredReplicas:  replicas,
		ReadyReplicas:    d.Status.ReadyReplicas,
		Labels:           copyMap(d.Spec.Template.Labels),
		MissingRequests:  missingRequests,
		MissingLimits:    missingLimits,
		MissingReadiness: missingReadiness,
		MissingLiveness:  missingLiveness,
		PVCBacked:        pvcBacked,
	}
}

func newStatefulSetProfile(s appsv1.StatefulSet) *workloadProfile {
	replicas := int32(1)
	if s.Spec.Replicas != nil {
		replicas = *s.Spec.Replicas
	}
	missingRequests, missingLimits, missingReadiness, missingLiveness, pvcBacked := summarizeTemplate(s.Spec.Template)
	if len(s.Spec.VolumeClaimTemplates) > 0 {
		pvcBacked = true
	}
	return &workloadProfile{
		Key: workloadKey{
			Namespace: s.Namespace,
			Kind:      "StatefulSet",
			Name:      s.Name,
		},
		DesiredReplicas:  replicas,
		ReadyReplicas:    s.Status.ReadyReplicas,
		Labels:           copyMap(s.Spec.Template.Labels),
		MissingRequests:  missingRequests,
		MissingLimits:    missingLimits,
		MissingReadiness: missingReadiness,
		MissingLiveness:  missingLiveness,
		Stateful:         true,
		PVCBacked:        pvcBacked,
	}
}

func summarizeTemplate(t corev1.PodTemplateSpec) (missingRequests, missingLimits, missingReadiness, missingLiveness int, pvcBacked bool) {
	for _, c := range t.Spec.Containers {
		req := c.Resources.Requests
		lim := c.Resources.Limits
		if req == nil || req.Cpu().IsZero() || req.Memory().IsZero() {
			missingRequests++
		}
		if lim == nil || lim.Cpu().IsZero() || lim.Memory().IsZero() {
			missingLimits++
		}
		if c.ReadinessProbe == nil {
			missingReadiness++
		}
		if c.LivenessProbe == nil {
			missingLiveness++
		}
	}
	for _, v := range t.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			pvcBacked = true
			break
		}
	}
	return missingRequests, missingLimits, missingReadiness, missingLiveness, pvcBacked
}

func collectHPATargets(items []autoscalingv2.HorizontalPodAutoscaler, include func(string) bool) map[workloadKey]struct{} {
	targets := map[workloadKey]struct{}{}
	for _, hpa := range items {
		if !include(hpa.Namespace) {
			continue
		}
		targets[workloadKey{
			Namespace: hpa.Namespace,
			Kind:      hpa.Spec.ScaleTargetRef.Kind,
			Name:      hpa.Spec.ScaleTargetRef.Name,
		}] = struct{}{}
	}
	return targets
}

func collectIngressBackends(items []networkingv1.Ingress, include func(string) bool) map[string]int {
	refs := map[string]int{}
	for _, ing := range items {
		if !include(ing.Namespace) {
			continue
		}
		addIngressService(refs, ing.Namespace, ing.Spec.DefaultBackend)
		for _, rule := range ing.Spec.Rules {
			if rule.HTTP == nil {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				backend := path.Backend
				addIngressService(refs, ing.Namespace, &backend)
			}
		}
	}
	return refs
}

func addIngressService(refs map[string]int, ns string, backend *networkingv1.IngressBackend) {
	if backend == nil || backend.Service == nil || backend.Service.Name == "" {
		return
	}
	refs[namespacedName(ns, backend.Service.Name)]++
}

func countServiceReadyEndpoints(items []discoveryv1.EndpointSlice, include func(string) bool) map[string]int {
	ready := map[string]int{}
	for _, slice := range items {
		if !include(slice.Namespace) {
			continue
		}
		serviceName := slice.Labels["kubernetes.io/service-name"]
		if serviceName == "" {
			continue
		}
		key := namespacedName(slice.Namespace, serviceName)
		for _, ep := range slice.Endpoints {
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				ready[key]++
			}
		}
	}
	return ready
}

func scoreProfile(p workloadProfile) (int, []string) {
	score := 0
	reasons := []string{}

	if p.ReadyReplicas < p.DesiredReplicas {
		score += 30
		reasons = append(reasons, fmt.Sprintf("ready replicas below desired (%d/%d)", p.ReadyReplicas, p.DesiredReplicas))
	}
	if p.ExternalServices > 0 && p.ReadyEndpoints == 0 {
		score += 45
		reasons = append(reasons, "externally exposed with zero ready endpoints")
	} else if p.ExternalServices > 0 && p.ReadyEndpoints < int(max32(1, p.DesiredReplicas)) {
		score += 12
		reasons = append(reasons, fmt.Sprintf("externally exposed with low endpoint headroom (%d ready endpoints)", p.ReadyEndpoints))
	}
	if p.ServiceCount > 0 && p.DesiredReplicas == 1 {
		score += 8
		reasons = append(reasons, "single replica behind a service")
	}
	if p.ExternalServices > 0 && p.DesiredReplicas == 1 {
		score += 18
		reasons = append(reasons, "external traffic depends on a single replica")
	}
	if p.ExternalServices > 0 && !p.HasHPA {
		score += 12
		reasons = append(reasons, "externally exposed without HPA")
	}
	if p.ServiceCount > 0 && p.DesiredReplicas > 1 && !p.HasPDB {
		score += 10
		reasons = append(reasons, "multi-replica service has no PDB")
	}
	if p.MissingReadiness > 0 {
		score += min(14, 6+(p.MissingReadiness-1)*2)
		reasons = append(reasons, fmt.Sprintf("missing readiness probes on %d container(s)", p.MissingReadiness))
	}
	if p.MissingLiveness > 0 {
		score += min(10, 4+(p.MissingLiveness-1)*2)
		reasons = append(reasons, fmt.Sprintf("missing liveness probes on %d container(s)", p.MissingLiveness))
	}
	if p.MissingRequests > 0 {
		score += min(10, 4+(p.MissingRequests-1)*2)
		reasons = append(reasons, fmt.Sprintf("missing resource requests on %d container(s)", p.MissingRequests))
	}
	if p.MissingLimits > 0 {
		score += min(8, 2+(p.MissingLimits-1)*2)
		reasons = append(reasons, fmt.Sprintf("missing resource limits on %d container(s)", p.MissingLimits))
	}
	if p.Stateful && p.PVCBacked && p.ExternalServices > 0 && p.DesiredReplicas == 1 {
		score += 8
		reasons = append(reasons, "stateful single-replica exposure increases failover/change risk")
	}

	return score, reasons
}

func workloadSeverity(maxScore int, exposedZeroReady int) model.Severity {
	switch {
	case exposedZeroReady > 0 || maxScore >= 80:
		return model.SeverityHigh
	case maxScore >= 50:
		return model.SeverityMedium
	default:
		return model.SeverityLow
	}
}

func workloadUrgency(maxScore int, exposedZeroReady int) model.Urgency {
	switch {
	case exposedZeroReady > 0 || maxScore >= 80:
		return model.UrgencyImmediate
	case maxScore >= 50:
		return model.UrgencyToday
	default:
		return model.UrgencyThisWeek
	}
}

func workloadConfidence(maxScore int) model.Confidence {
	if maxScore >= 50 {
		return model.ConfidenceHigh
	}
	return model.ConfidenceMedium
}

func highRiskOrScored(highRisk, scored int) int {
	if highRisk > 0 {
		return highRisk
	}
	return scored
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

func isExternallyExposedService(svc corev1.Service, ingressRefs map[string]int) bool {
	if svc.Spec.Type == corev1.ServiceTypeLoadBalancer || svc.Spec.Type == corev1.ServiceTypeNodePort {
		return true
	}
	return ingressRefs[namespacedName(svc.Namespace, svc.Name)] > 0
}

func namespacedName(ns, name string) string {
	return ns + "/" + name
}

func max32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (a *Analyzer) includeNamespace(ns string) bool {
	return a.IncludeSystemNamespaces || !capability.IsSystemNamespace(ns)
}
