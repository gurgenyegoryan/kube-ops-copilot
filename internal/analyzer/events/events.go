package events

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Analyzer struct {
	Client *kubernetes.Clientset
	Since  time.Duration
	Now    func() time.Time
}

func (a *Analyzer) Name() string { return "events" }

func (a *Analyzer) Run(ctx context.Context) (analyzer.Result, error) {
	if a.Client == nil {
		return analyzer.Result{}, fmt.Errorf("kubernetes client is nil")
	}

	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	since := a.Since
	if since == 0 {
		since = 60 * time.Minute
	}
	cutoff := now().Add(-since)

	events, err := a.Client.CoreV1().Events("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return analyzer.Result{}, fmt.Errorf("list events: %w", err)
	}

	res := analyzer.Result{}
	res.Evidence = append(res.Evidence, model.Evidence{Signal: fmt.Sprintf("observed %d events (all namespaces)", len(events.Items))})

	warnByReason := map[string]int{}
	warnSamples := []string{}
	warningsRecent := 0

	for _, e := range events.Items {
		if e.Type != corev1.EventTypeWarning {
			continue
		}

		last := eventLastSeen(e)
		if !last.IsZero() && last.Before(cutoff) {
			continue
		}
		warningsRecent++
		warnByReason[e.Reason]++
		if len(warnSamples) < 10 {
			warnSamples = append(warnSamples, fmt.Sprintf("Warning %s %s/%s: %s", e.Reason, e.InvolvedObject.Namespace, e.InvolvedObject.Name, e.Message))
		}
	}

	if warningsRecent == 0 {
		return res, nil
	}

	res.Findings = append(res.Findings, model.Finding{
		Title:                 fmt.Sprintf("%d Warning event(s) in last %s", warningsRecent, since.String()),
		Severity:              model.SeverityMedium,
		Urgency:               model.UrgencyToday,
		Confidence:            model.ConfidenceMedium,
		AffectedScope:         "cluster/events",
		WhyItMatters:          "Warning events are often early indicators of scheduling, image pull, probe, or storage failures that can degrade reliability before user impact is obvious.",
		AutomationSuitability: model.SuitabilityAdvisoryOnly,
	})

	for _, s := range warnSamples {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: s})
	}

	top := topReasons(warnByReason, 5)
	res.HiddenRisks = append(res.HiddenRisks, fmt.Sprintf("Top Warning event reasons (recent): %s", top))

	res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
		Title:               "Triage recent Warning events by reason and affected object",
		ExpectedBenefit:     "Faster identification of pre-incident failure modes (probe flapping, image pull errors, failed scheduling, PVC issues).",
		RiskTradeoff:        "Low risk; read-only investigation.",
		Priority:            "P1",
		ExecutionSafety:     model.SafetySafeAutoCandidate,
		ApprovalRequirement: model.ApprovalNone,
		RollbackOutline:     "N/A (no changes)",
	})

	res.Hypotheses = append(res.Hypotheses, model.Hypothesis{
		Rank:        1,
		Description: "Some workloads may be experiencing intermittent failures (scheduling, storage, image pull, or probe issues) reflected as Warning events.",
		Probability: model.ConfidenceMedium,
		SupportingEvidence: []model.Evidence{
			{Signal: fmt.Sprintf("%d recent Warning event(s)", warningsRecent)},
		},
		ContradictoryOrMissing: []model.Evidence{
			{Signal: "Event stream is best-effort; not all components emit events reliably"},
		},
		WhatToVerifyNext: []string{
			"Filter warnings by reason: `kubectl get events -A --field-selector type=Warning --sort-by=.lastTimestamp`",
			"Describe the top affected pods/nodes to confirm the precise failure mode",
		},
	})

	return res, nil
}

func eventLastSeen(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		return e.Series.LastObservedTime.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	if !e.FirstTimestamp.IsZero() {
		return e.FirstTimestamp.Time
	}
	return time.Time{}
}

type reasonCount struct {
	reason string
	count  int
}

func topReasons(m map[string]int, n int) string {
	var rc []reasonCount
	for k, v := range m {
		rc = append(rc, reasonCount{reason: k, count: v})
	}
	sort.SliceStable(rc, func(i, j int) bool { return rc[i].count > rc[j].count })
	if len(rc) > n {
		rc = rc[:n]
	}
	out := ""
	for i, x := range rc {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%s=%d", x.reason, x.count)
	}
	if out == "" {
		return "none"
	}
	return out
}
