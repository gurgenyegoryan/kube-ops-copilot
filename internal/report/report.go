package report

import (
	"sort"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

func Build(results []analyzer.Result) model.Report {
	r := model.Report{GeneratedAt: time.Now()}

	for _, res := range results {
		r.KeyFindings = append(r.KeyFindings, res.Findings...)
		r.Evidence = append(r.Evidence, res.Evidence...)
		r.Hypotheses = append(r.Hypotheses, res.Hypotheses...)
		r.Recommended.Immediate = append(r.Recommended.Immediate, res.Recommended.Immediate...)
		r.Recommended.ShortTerm = append(r.Recommended.ShortTerm, res.Recommended.ShortTerm...)
		r.Recommended.Preventive = append(r.Recommended.Preventive, res.Recommended.Preventive...)
		if r.ExecutionPlan == nil && res.ExecutionPlan != nil {
			r.ExecutionPlan = res.ExecutionPlan
		}
		r.Unknowns = append(r.Unknowns, res.Unknowns...)
		r.HiddenRisks = append(r.HiddenRisks, res.HiddenRisks...)
	}

	sort.SliceStable(r.KeyFindings, func(i, j int) bool {
		return severityRank(r.KeyFindings[i].Severity) < severityRank(r.KeyFindings[j].Severity)
	})

	sort.SliceStable(r.Hypotheses, func(i, j int) bool { return r.Hypotheses[i].Rank < r.Hypotheses[j].Rank })

	r.Assessment = assessment(r)
	r.ExecutiveSummary = executiveSummary(r)
	r.ProposedOperatorMessage = operatorMessage(r)
	r.FinalVerdict = finalVerdict(r)

	return r
}

func assessment(r model.Report) model.SnapshotAssessment {
	score := 100
	topRisk := model.SeverityInfo
	themeCounts := map[string]int{}
	limiters := []string{}
	observabilityCoverage := "confirmed"

	for _, f := range r.KeyFindings {
		switch f.Severity {
		case model.SeverityCritical:
			score -= 25
		case model.SeverityHigh:
			score -= 18
		case model.SeverityMedium:
			score -= 10
		case model.SeverityLow:
			score -= 4
		}
		if severityRank(f.Severity) < severityRank(topRisk) {
			topRisk = f.Severity
		}
		if f.AffectedScope != "" {
			themeCounts[f.AffectedScope]++
		}
		lowerTitle := strings.ToLower(f.Title)
		lowerWhy := strings.ToLower(f.WhyItMatters)
		if strings.Contains(lowerTitle, "not confirmed") || strings.Contains(lowerTitle, "coverage") || strings.Contains(lowerWhy, "cannot produce high-confidence") {
			observabilityCoverage = "unconfirmed"
			limiters = append(limiters, "Telemetry coverage is not confirmed enough for high-confidence trend analysis.")
		}
	}

	if len(r.KeyFindings) == 0 {
		topRisk = model.SeverityInfo
	}

	if len(r.Unknowns) > 0 {
		score -= minInt(len(r.Unknowns)*3, 12)
		limiters = append(limiters, "There are unresolved unknowns in the current snapshot.")
	}
	if len(r.Evidence) < 5 {
		score -= 6
		limiters = append(limiters, "Evidence density is still light; recommendations may need extra verification.")
	}
	if len(r.Hypotheses) > 0 {
		for _, h := range r.Hypotheses {
			if len(h.ContradictoryOrMissing) > 0 {
				score -= 2
				limiters = append(limiters, "Some leading hypotheses still have contradictory or missing evidence.")
				break
			}
		}
	}

	if score < 0 {
		score = 0
	}

	automationConfidence := model.ConfidenceHigh
	switch {
	case observabilityCoverage == "unconfirmed" || len(r.Unknowns) >= 3 || topRisk == model.SeverityCritical:
		automationConfidence = model.ConfidenceLow
	case len(r.Unknowns) > 0 || topRisk == model.SeverityHigh || topRisk == model.SeverityMedium:
		automationConfidence = model.ConfidenceMedium
	}

	return model.SnapshotAssessment{
		ProductionReadinessScore: score,
		OperationalRisk:          topRisk,
		AutomationConfidence:     automationConfidence,
		ObservabilityCoverage:    observabilityCoverage,
		ConfidenceLimiters:       uniqueStrings(limiters),
		TopRiskThemes:            topThemes(themeCounts, 4),
	}
}

func topThemes(counts map[string]int, limit int) []string {
	type pair struct {
		name  string
		count int
	}
	items := make([]pair, 0, len(counts))
	for name, count := range counts {
		items = append(items, pair{name: name, count: count})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].name < items[j].name
		}
		return items[i].count > items[j].count
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.name)
	}
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func severityRank(s model.Severity) int {
	switch s {
	case model.SeverityCritical:
		return 0
	case model.SeverityHigh:
		return 1
	case model.SeverityMedium:
		return 2
	case model.SeverityLow:
		return 3
	case model.SeverityInfo:
		return 4
	default:
		return 9
	}
}

func executiveSummary(r model.Report) string {
	if len(r.KeyFindings) == 0 {
		return "No critical anomalies detected in current snapshot; continue monitoring and validate with time-series metrics."
	}
	f := r.KeyFindings[0]
	return f.Title + ": " + f.WhyItMatters
}

func operatorMessage(r model.Report) string {
	if len(r.KeyFindings) == 0 {
		return "Kube Ops Copilot: no urgent issues detected in this snapshot."
	}
	f := r.KeyFindings[0]
	msg := "Kube Ops Copilot: " + f.Title + " (severity=" + string(f.Severity) + "). " + f.WhyItMatters
	if r.ExecutionPlan != nil && r.ExecutionPlan.ApprovalRequirement != model.ApprovalNone {
		msg += "\nProposed action is approval-gated. If you approve, reply with: 'approve' and provide an approval id (ticket/Slack thread)."
	}
	msg += "\nNext: run `kube-ops-copilot diagnose --output markdown` and review evidence + actions."
	return msg
}

func finalVerdict(r model.Report) string {
	if len(r.KeyFindings) == 0 {
		return "Healthy snapshot. Keep SLO monitoring and periodically re-run diagnostics."
	}
	return "Address highest-severity finding first; validate impact and avoid broad remediation without confirming root cause."
}
