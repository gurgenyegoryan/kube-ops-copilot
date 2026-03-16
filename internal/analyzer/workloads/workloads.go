package workloads

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client                  *kubernetes.Clientset
	IncludeSystemNamespaces bool
}

func (a *Analyzer) Name() string { return "workloads" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}

	res := analyzer.Result{}

	deps, err := a.Client.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list deployments: %w", err)
	}
	res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d deployments (all namespaces)", len(deps.Items))})

	degraded := 0
	rolloutStuck := 0
	for _, d := range deps.Items {
		if !a.IncludeSystemNamespaces && isSystemNamespace(d.Namespace) {
			continue
		}

		if d.Status.UnavailableReplicas > 0 {
			degraded++
			res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("deployment degraded: %s/%s unavailableReplicas=%d", d.Namespace, d.Name, d.Status.UnavailableReplicas)})
		}
		if progressingIsFalse(d) {
			rolloutStuck++
			res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("deployment rollout issue: %s/%s Progressing=False", d.Namespace, d.Name)})
		}
	}

	if degraded > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d deployment(s) have unavailable replicas", degraded),
			Severity:              model.SeverityHigh,
			Urgency:               model.UrgencyImmediate,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "workloads/deployments",
			WhyItMatters:          "Unavailable replicas reduce service capacity and increase tail latency/error rates during load or failures; they are often a direct SLO risk.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})

		res.Recommended.Immediate = append(res.Recommended.Immediate, model.Action{
			Title:               "Identify why replicas are unavailable (describe + events + pod logs)",
			ExpectedBenefit:     "Pinpoints whether the issue is image pull, probe failure, crash loops, scheduling, or dependency outage—preventing blind scaling/restarts.",
			RiskTradeoff:        "Low; read-only investigation.",
			Priority:            "P0",
			ExecutionSafety:     model.SafetySafeAutoCandidate,
			ApprovalRequirement: model.ApprovalNone,
			RollbackOutline:     "N/A (no changes)",
		})

		res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
			Rank:        1,
			Description: "One or more deployments are currently degraded due to failing pods (crash loops), failing probes, or scheduling constraints.",
			Probability: model.ConfidenceHigh,
			SupportingEvidence: []model.Evidence{
				{Signal: fmt.Sprintf("%d deployments with unavailable replicas", degraded)},
			},
			ContradictoryOrMissing: []model.Evidence{
				{Signal: "Need per-deployment events/logs to distinguish crash vs scheduling vs dependency issues"},
			},
			WhatToVerifyNext: []string{
				"For an affected deployment: `kubectl -n <ns> describe deploy <name>`",
				"Check replica set/pod events and probe failures: `kubectl -n <ns> get events --sort-by=.lastTimestamp`",
				"Inspect failing pod logs: `kubectl -n <ns> logs <pod> --previous`",
			},
		})
	}

	if rolloutStuck > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d deployment(s) report Progressing=False", rolloutStuck),
			Severity:              model.SeverityHigh,
			Urgency:               model.UrgencyImmediate,
			Confidence:            model.ConfidenceMedium,
			AffectedScope:         "workloads/deployments",
			WhyItMatters:          "A stuck rollout can leave the service in a partial state and can repeatedly churn pods during retries, amplifying load on dependencies.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.HiddenRisks = append(res.HiddenRisks, "Rollout failures are often caused by probe strictness, missing secrets/configmaps, or dependency readiness; automatic restarts can hide the true cause.")
	}

	return res, nil
}

func isSystemNamespace(ns string) bool {
	s := strings.ToLower(ns)
	systemPrefixes := []string{"kube-", "amazon-", "aws-", "coredns", "gatekeeper", "istio-", "ingress-", "cert-manager", "monitoring"}
	if s == "kube-system" || s == "kube-public" || s == "kube-node-lease" {
		return true
	}
	for _, p := range systemPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func progressingIsFalse(d appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Status == "False" {
			// make it more robust against ancient conditions by focusing on recent transitions
			if !c.LastUpdateTime.IsZero() && time.Since(c.LastUpdateTime.Time) > 30*24*time.Hour {
				return false
			}
			return true
		}
	}
	return false
}
