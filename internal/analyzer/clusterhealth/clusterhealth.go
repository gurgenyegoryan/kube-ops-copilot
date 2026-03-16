package clusterhealth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client *kubernetes.Clientset
	Now    func() time.Time
}

func (a *Analyzer) Name() string { return "clusterhealth" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}

	res := analyzer.Result{}

	nodes, err := a.Client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return res, fmt.Errorf("list nodes: %w", err)
	}
	res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d nodes in cluster", len(nodes.Items))})

	notReady := 0
	pressure := 0
	for _, n := range nodes.Items {
		ready := nodeConditionStatus(n.Status.Conditions, v1.NodeReady)
		if ready != v1.ConditionTrue {
			notReady++
			res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("node %s Ready=%s", n.Name, ready)})
		}

		if nodeConditionStatus(n.Status.Conditions, v1.NodeMemoryPressure) == v1.ConditionTrue ||
			nodeConditionStatus(n.Status.Conditions, v1.NodeDiskPressure) == v1.ConditionTrue ||
			nodeConditionStatus(n.Status.Conditions, v1.NodePIDPressure) == v1.ConditionTrue {
			pressure++
			res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("node %s reports pressure condition(s)", n.Name)})
		}
	}

	if notReady > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d node(s) not Ready", notReady),
			Severity:              model.SeverityHigh,
			Urgency:               model.UrgencyImmediate,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "cluster/nodes",
			WhyItMatters:          "NotReady nodes reduce schedulable capacity and can cause pod evictions, service disruption, and cascading failures during rollouts.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.HiddenRisks = append(res.HiddenRisks, "NotReady nodes often correlate with underlying kubelet/runtime/network issues; treat as potential multi-tenant blast radius.")
	}

	if pressure > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d node(s) under resource pressure", pressure),
			Severity:              model.SeverityHigh,
			Urgency:               model.UrgencyToday,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "cluster/nodes",
			WhyItMatters:          "Node pressure increases risk of eviction, throttling, and noisy-neighbor impact; it can also hide as intermittent pod instability.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
	}

	pods, err := a.Client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		res.Unknowns = append(res.Unknowns, fmt.Sprintf("unable to list pods cluster-wide: %v", err))
		return res, nil
	}
	res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d pods in all namespaces", len(pods.Items))})

	pending := 0
	crashLoop := 0
	oom := 0
	recentRestarts := 0
	topRestarts := []restartSample{}
	cutoff := now().Add(-30 * time.Minute)

	for _, p := range pods.Items {
		if p.Status.Phase == v1.PodPending {
			pending++
			res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("pod pending: %s/%s", p.Namespace, p.Name)})
		}

		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				crashLoop++
				res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("CrashLoopBackOff: %s/%s container=%s", p.Namespace, p.Name, cs.Name)})
			}
			if cs.LastTerminationState.Terminated != nil && cs.LastTerminationState.Terminated.ExitCode == 137 {
				oom++
				res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("recent termination exit=137 (OOM-kill likely): %s/%s container=%s", p.Namespace, p.Name, cs.Name)})
			}
			if cs.RestartCount > 0 && p.CreationTimestamp.Time.Before(cutoff) {
				recentRestarts++
				term := mostRecentTermination(cs)
				exitCode := int32(0)
				reason := ""
				finishedAt := time.Time{}
				if term != nil {
					exitCode = term.ExitCode
					reason = term.Reason
					finishedAt = term.FinishedAt.Time
				}
				topRestarts = append(topRestarts, restartSample{
					Namespace: p.Namespace,
					Pod:       p.Name,
					Container: cs.Name,
					Restarts:  int(cs.RestartCount),
					Reason:    lastTerminationReason(cs),
					ExitCode:  exitCode,
					Class:     classifyTermination(exitCode, reason, cs.State.Waiting),
					FinishedAt: func() *time.Time {
						if finishedAt.IsZero() {
							return nil
						}
						v := finishedAt
						return &v
					}(),
				})
			}
		}
	}

	if pending > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d pod(s) Pending", pending),
			Severity:              model.SeverityMedium,
			Urgency:               model.UrgencyToday,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "cluster/pods",
			WhyItMatters:          "Pending pods indicate scheduling constraints (capacity, affinity/taints, PVC binding, quotas) and can silently degrade availability during bursts.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.HiddenRisks = append(res.HiddenRisks, "Pending pods with available cluster resources often indicate policy-constrained placement (affinity, topology spread, taints/tolerations) rather than real capacity exhaustion.")

		res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
			Title:               "Confirm pending root cause via scheduler messages (events) and constraints",
			ExpectedBenefit:     "Separates true capacity exhaustion from policy-constrained placement (taints/affinity/topology/PVC).",
			RiskTradeoff:        "Low; read-only investigation.",
			Priority:            "P1",
			ExecutionSafety:     model.SafetySafeAutoCandidate,
			ApprovalRequirement: model.ApprovalNone,
			RollbackOutline:     "N/A (no changes)",
		})
	}

	if crashLoop > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d container(s) in CrashLoopBackOff", crashLoop),
			Severity:              model.SeverityHigh,
			Urgency:               model.UrgencyImmediate,
			Confidence:            model.ConfidenceHigh,
			AffectedScope:         "cluster/pods",
			WhyItMatters:          "Crash loops reduce effective capacity and can create downstream load amplification; they are a common pre-incident signal.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})

		res.Recommended.Immediate = append(res.Recommended.Immediate, model.Action{
			Title:               "Triage crash loops using previous container logs and termination reasons",
			ExpectedBenefit:     "Identifies config regressions, missing secrets, dependency failures, or OOM/CPU starvation causing churn.",
			RiskTradeoff:        "Low; read-only.",
			Priority:            "P0",
			ExecutionSafety:     model.SafetySafeAutoCandidate,
			ApprovalRequirement: model.ApprovalNone,
			RollbackOutline:     "N/A (no changes)",
		})

		res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
			Rank:                   2,
			Description:            "Some pods are repeatedly failing to start due to application crashes, missing configuration/secrets, failing probes, or dependency unavailability.",
			Probability:            model.ConfidenceHigh,
			SupportingEvidence:     []model.Evidence{{Signal: fmt.Sprintf("%d CrashLoopBackOff container(s)", crashLoop)}},
			ContradictoryOrMissing: []model.Evidence{{Signal: "Need per-pod termination reason and logs to pinpoint exact failure mode"}},
			WhatToVerifyNext: []string{
				"`kubectl -n <ns> logs <pod> --previous`",
				"`kubectl -n <ns> describe pod <pod>` (look for probe failures / image pull / mount errors)",
			},
		})
	}

	if oom > 0 {
		res.Findings = append(res.Findings, model.Finding{
			Title:                 fmt.Sprintf("%d container(s) show exit code 137 (OOM-kill likely)", oom),
			Severity:              model.SeverityHigh,
			Urgency:               model.UrgencyToday,
			Confidence:            model.ConfidenceMedium,
			AffectedScope:         "cluster/pods",
			WhyItMatters:          "OOM-like terminations indicate memory pressure or mis-sized limits/requests; repeated OOMs can trigger cascading retries and SLO impact.",
			AutomationSuitability: model.SuitabilityAdvisoryOnly,
		})
		res.HiddenRisks = append(res.HiddenRisks, "Exit code 137 can also come from SIGKILL for other reasons; confirm via `kubectl describe pod` and node OOM logs.")

		res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
			Title:               "Confirm OOM root cause and size memory limits/requests conservatively",
			ExpectedBenefit:     "Reduces restart churn and avoids amplifying load via retries.",
			RiskTradeoff:        "Raising limits can increase node pressure; verify scheduling headroom.",
			Priority:            "P1",
			ExecutionSafety:     model.SafetyNeedsOperatorApproval,
			ApprovalRequirement: model.ApprovalNeedsOperator,
			RollbackOutline:     "Revert memory values to previous manifests.",
		})
	}

	if recentRestarts > 0 {
		res.HiddenRisks = append(res.HiddenRisks, fmt.Sprintf("%d container(s) have non-zero restart counts; investigate restart trends over time, not only absolute counts", recentRestarts))

		sort.SliceStable(topRestarts, func(i, j int) bool { return topRestarts[i].Restarts > topRestarts[j].Restarts })
		if len(topRestarts) > 10 {
			topRestarts = topRestarts[:10]
		}
		maxRestarts := 0
		maxCrashLikeRestarts := 0
		crashLike := 0
		likelyBenign := 0
		unknown := 0
		systemCrashLike := 0
		for _, s := range topRestarts {
			if s.Restarts > maxRestarts {
				maxRestarts = s.Restarts
			}
			switch s.Class {
			case terminationCrashLike:
				crashLike++
				if s.Restarts > maxCrashLikeRestarts {
					maxCrashLikeRestarts = s.Restarts
				}
				if isSystemNamespace(s.Namespace) {
					systemCrashLike++
				}
			case terminationSigterm, terminationCompleted:
				likelyBenign++
			default:
				unknown++
			}

			finished := ""
			if s.FinishedAt != nil {
				finished = fmt.Sprintf(" finishedAt=%s", s.FinishedAt.UTC().Format(time.RFC3339))
			}
			res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf(
				"top restart: %s/%s container=%s restarts=%d class=%s exit=%d lastReason=%s%s",
				s.Namespace, s.Pod, s.Container, s.Restarts, s.Class, s.ExitCode, s.Reason, finished,
			)})
		}
		res.HiddenRisks = append(res.HiddenRisks, fmt.Sprintf("restart classification (top samples): benign=%d crashLike=%d unknown=%d", likelyBenign, crashLike, unknown))

		// Findings: only escalate when restart evidence suggests crash-like behavior.
		sawCrashLikeInSystem := systemCrashLike > 0
		if sawCrashLikeInSystem {
			res.Findings = append(res.Findings, model.Finding{
				Title:                 fmt.Sprintf("Crash-like restarts observed in system components (samples=%d, max crash-like restarts=%d)", systemCrashLike, maxCrashLikeRestarts),
				Severity:              model.SeverityMedium,
				Urgency:               model.UrgencyToday,
				Confidence:            model.ConfidenceMedium,
				AffectedScope:         "cluster/pods",
				WhyItMatters:          "Restarting control-plane adjuncts (e.g., autoscaler/CNI/CSI) can surface API connectivity issues, IAM/config problems, or dependency failures and may impact scheduling/provisioning and node lifecycle.",
				AutomationSuitability: model.SuitabilityAdvisoryOnly,
			})
			res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
				Title:               "For system pods with crash-like restarts, inspect previous logs and confirm API server connectivity",
				ExpectedBenefit:     "Distinguishes benign restarts from real control-plane connectivity/config failures and prevents hidden reliability degradation.",
				RiskTradeoff:        "Low; read-only investigation.",
				Priority:            "P1",
				ExecutionSafety:     model.SafetySafeAutoCandidate,
				ApprovalRequirement: model.ApprovalNone,
				RollbackOutline:     "N/A (no changes)",
			})
		} else if maxCrashLikeRestarts >= 5 {
			res.Findings = append(res.Findings, model.Finding{
				Title:                 fmt.Sprintf("Elevated crash-like restart churn detected (max crash-like restarts=%d)", maxCrashLikeRestarts),
				Severity:              model.SeverityMedium,
				Urgency:               model.UrgencyToday,
				Confidence:            model.ConfidenceMedium,
				AffectedScope:         "cluster/pods",
				WhyItMatters:          "Crash-like restarts (non-SIGTERM/Completed) often indicate OOM/probe flapping/config regressions or dependency timeouts and can amplify downstream load and latency.",
				AutomationSuitability: model.SuitabilityAdvisoryOnly,
			})
			res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
				Title:               "Identify crash-like top restarters and confirm termination reasons (OOM/probes/dependencies)",
				ExpectedBenefit:     "Turns restart signal into confirmed root cause and a targeted fix.",
				RiskTradeoff:        "Low; read-only investigation.",
				Priority:            "P1",
				ExecutionSafety:     model.SafetySafeAutoCandidate,
				ApprovalRequirement: model.ApprovalNone,
				RollbackOutline:     "N/A (no changes)",
			})
		} else if maxRestarts >= 5 {
			res.Findings = append(res.Findings, model.Finding{
				Title:                 fmt.Sprintf("High restart counts observed (max restarts=%d) but mostly graceful terminations", maxRestarts),
				Severity:              model.SeverityLow,
				Urgency:               model.UrgencyBacklog,
				Confidence:            model.ConfidenceMedium,
				AffectedScope:         "cluster/pods",
				WhyItMatters:          "High restart counts can look alarming but are often caused by rolling updates or node drains (SIGTERM); the operational risk depends on whether restarts are crash-like and whether availability is impacted.",
				AutomationSuitability: model.SuitabilityAdvisoryOnly,
			})
			res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
				Title:               "Correlate high restart counts with node drains/rollouts and confirm they are SIGTERM/Completed",
				ExpectedBenefit:     "Reduces false alarms and highlights only restart patterns that correlate with reliability risk.",
				RiskTradeoff:        "Low; read-only investigation.",
				Priority:            "P2",
				ExecutionSafety:     model.SafetySafeAutoCandidate,
				ApprovalRequirement: model.ApprovalNone,
				RollbackOutline:     "N/A (no changes)",
			})
		}
	}

	return res, nil
}

type restartSample struct {
	Namespace  string
	Pod        string
	Container  string
	Restarts   int
	Reason     string
	ExitCode   int32
	Class      terminationClass
	FinishedAt *time.Time
}

type terminationClass string

const (
	terminationUnknown   terminationClass = "unknown"
	terminationSigterm   terminationClass = "sigterm"
	terminationCompleted terminationClass = "completed"
	terminationCrashLike terminationClass = "crashLike"
)

func mostRecentTermination(cs v1.ContainerStatus) *v1.ContainerStateTerminated {
	if cs.LastTerminationState.Terminated != nil {
		return cs.LastTerminationState.Terminated
	}
	if cs.State.Terminated != nil {
		return cs.State.Terminated
	}
	return nil
}

func classifyTermination(exitCode int32, reason string, waiting *v1.ContainerStateWaiting) terminationClass {
	// Waiting CrashLoopBackOff already generates a stronger finding; keep classification unknown here.
	if waiting != nil && strings.EqualFold(waiting.Reason, "CrashLoopBackOff") {
		return terminationUnknown
	}
	if strings.EqualFold(reason, "Completed") || exitCode == 0 {
		return terminationCompleted
	}
	// 143 = SIGTERM (graceful termination) in many container runtimes.
	if exitCode == 143 {
		return terminationSigterm
	}
	if exitCode != 0 {
		return terminationCrashLike
	}
	return terminationUnknown
}

func lastTerminationReason(cs v1.ContainerStatus) string {
	if cs.LastTerminationState.Terminated != nil {
		if cs.LastTerminationState.Terminated.Reason != "" {
			return cs.LastTerminationState.Terminated.Reason
		}
		return fmt.Sprintf("exit=%d", cs.LastTerminationState.Terminated.ExitCode)
	}
	if cs.State.Terminated != nil {
		if cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
		return fmt.Sprintf("exit=%d", cs.State.Terminated.ExitCode)
	}
	if cs.State.Waiting != nil {
		return cs.State.Waiting.Reason
	}
	return "unknown"
}

func nodeConditionStatus(conds []v1.NodeCondition, t v1.NodeConditionType) v1.ConditionStatus {
	for _, c := range conds {
		if c.Type == t {
			return c.Status
		}
	}
	return v1.ConditionUnknown
}

func isSystemNamespace(ns string) bool {
	switch ns {
	case "kube-system", "kube-public", "kube-node-lease":
		return true
	default:
		return false
	}
}
