package resources

import (
	"context"
	"fmt"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

func (a *Analyzer) Name() string { return "resources" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}

	res := analyzer.Result{}

	deps, err := a.Client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list deployments: %w", err)
	}

	missingRequests := 0
	missingLimits := 0
	missingProbes := 0

	for _, d := range deps.Items {
		if !a.IncludeSystemNamespaces && isSystemNamespace(d.Namespace) {
			continue
		}

		for _, c := range d.Spec.Template.Spec.Containers {
			req := c.Resources.Requests
			lim := c.Resources.Limits
			if req == nil || req.Cpu().IsZero() || req.Memory().IsZero() {
				missingRequests++
				res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("missing CPU/mem requests: deploy %s/%s container=%s", d.Namespace, d.Name, c.Name)})
			}
			if lim == nil || lim.Cpu().IsZero() || lim.Memory().IsZero() {
				missingLimits++
				res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("missing CPU/mem limits: deploy %s/%s container=%s", d.Namespace, d.Name, c.Name)})
			}
			if c.ReadinessProbe == nil || c.LivenessProbe == nil {
				missingProbes++
				res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("missing probe(s): deploy %s/%s container=%s readiness=%t liveness=%t", d.Namespace, d.Name, c.Name, c.ReadinessProbe != nil, c.LivenessProbe != nil)})
			}
		}
	}

	if missingRequests > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d container(s) missing CPU/memory requests", missingRequests),
			Severity:              model.SeverityMedium,
			Urgency:               model.UrgencyThisWeek,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "workloads/resources",
			WhyItMatters:          "Missing requests reduce scheduling predictability and can cause noisy-neighbor resource contention; autoscaling and capacity planning become unreliable.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.Recommended.Preventive = append(res.Recommended.Preventive, model.Action{
			Title:               "Add realistic CPU/memory requests for containers (based on metrics)",
			ExpectedBenefit:     "Improves scheduling stability, reduces contention, and makes HPA/VPA decisions safer.",
			RiskTradeoff:        "May reduce bin-packing if requests are set too high; requires metric-based sizing.",
			Priority:            "P2",
			ExecutionSafety:     model.SafetyNeedsOperatorApproval,
			ApprovalRequirement: model.ApprovalNeedsOperator,
			RollbackOutline:     "Revert resource request values to previous manifests.",
		})
	}

	if missingLimits > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d container(s) missing CPU/memory limits", missingLimits),
			Severity:              model.SeverityLow,
			Urgency:               model.UrgencyBacklog,
			Confidence:            model.ConfidenceMedium,
			AffectedScope:         "workloads/resources",
			WhyItMatters:          "Missing limits can increase blast radius of runaway resource usage; however, strict CPU limits can also induce throttling if mis-sized.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})

		res.Recommended.Preventive = append(res.Recommended.Preventive, model.Action{
			Title:               "Define conservative memory limits for containers that lack them (CPU limits optional)",
			ExpectedBenefit:     "Caps blast radius of memory leaks/runaway processes and reduces node-level pressure/evictions.",
			RiskTradeoff:        "Overly tight limits can cause OOMKills; CPU limits can cause throttling—size using metrics and stage changes.",
			Priority:            "P3",
			ExecutionSafety:     model.SafetyNeedsOperatorApproval,
			ApprovalRequirement: model.ApprovalNeedsOperator,
			RollbackOutline:     "Revert limits to previous values or remove limits if they cause instability.",
		})
	}

	if missingProbes > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d container(s) missing readiness/liveness probes", missingProbes),
			Severity:              model.SeverityMedium,
			Urgency:               model.UrgencyThisWeek,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "workloads/probes",
			WhyItMatters:          "Without probes, Kubernetes can route traffic to unhealthy pods or fail to self-heal; this increases MTTR and hides partial failures.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.HiddenRisks = append(res.HiddenRisks, "Probe misconfiguration can be worse than no probe; validate against real startup and dependency behavior.")

		res.Recommended.Preventive = append(res.Recommended.Preventive, model.Action{
			Title:               "Add readiness + liveness probes for user-facing/critical workloads (stage carefully)",
			ExpectedBenefit:     "Prevents traffic to unhealthy pods and enables self-healing; reduces incident duration and partial-failure impact.",
			RiskTradeoff:        "Incorrect probes can cause self-inflicted outages; validate timeouts/thresholds and dependency behavior.",
			Priority:            "P2",
			ExecutionSafety:     model.SafetyNeedsOperatorApproval,
			ApprovalRequirement: model.ApprovalNeedsOperator,
			RollbackOutline:     "Revert probe configuration or relax thresholds if rollouts flap.",
		})
	}

	// Add a gentle hypothesis to encourage metric-backed sizing.
	if missingRequests > 0 || missingProbes > 0 {
		res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
			Rank:        3,
			Description: "Some workloads may be under-specified (requests/probes), increasing risk of instability during load or node pressure events.",
			Probability: model.ConfidenceMedium,
			SupportingEvidence: []model.Evidence{
				{Signal: fmt.Sprintf("missingRequests=%d missingProbes=%d", missingRequests, missingProbes)},
			},
			ContradictoryOrMissing: []model.Evidence{
				{Signal: "Need runtime metrics (CPU/memory, latency, restarts) to size requests and validate probes"},
			},
			WhatToVerifyNext: []string{
				"Check container p95 CPU/memory and throttling/oom trends in Prometheus",
				"Validate readiness/liveness semantics with dependency graphs and startup time",
			},
		})
	}

	return res, nil
}

func isSystemNamespace(ns string) bool {
	s := strings.ToLower(ns)
	if s == "kube-system" || s == "kube-public" || s == "kube-node-lease" {
		return true
	}
	for _, p := range []string{"kube-", "amazon-", "aws-", "istio-", "ingress-", "cert-manager", "monitoring"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
