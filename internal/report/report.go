package report

import (
	"sort"
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

	r.ExecutiveSummary = executiveSummary(r)
	r.ProposedOperatorMessage = operatorMessage(r)
	r.FinalVerdict = finalVerdict(r)

	return r
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
