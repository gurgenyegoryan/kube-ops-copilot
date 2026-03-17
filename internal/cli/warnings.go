package cli

import (
	"fmt"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

func warningResult(messages []string) analyzer.Result {
	if len(messages) == 0 {
		return analyzer.Result{}
	}
	res := analyzer.Result{}
	for _, msg := range messages {
		res.Evidence = append(res.Evidence, model.Evidence{Signal: "apiserver warning header: " + msg})
		if isDeprecationWarning(msg) {
			res.Findings = append(res.Findings, model.Finding{
				Title:                 "Kubernetes API deprecation warning observed during analysis",
				Severity:              model.SeverityMedium,
				Urgency:               model.UrgencyThisWeek,
				Confidence:            model.ConfidenceHigh,
				AffectedScope:         "platform/api-compatibility",
				WhyItMatters:          "Deprecated Kubernetes APIs can stop working after cluster upgrades, turning routine diagnostics or automation into production-impacting failures.",
				AutomationSuitability: model.SuitabilityAdvisoryOnly,
			})
			res.Recommended.ShortTerm = append(res.Recommended.ShortTerm, model.Action{
				Title:               "Audit and replace deprecated Kubernetes API usage before the next cluster upgrade",
				ExpectedBenefit:     "Prevents future breakage in diagnostics, automation, and operational workflows when deprecated endpoints are removed.",
				RiskTradeoff:        "Requires code and tooling updates but reduces upgrade risk.",
				Priority:            "P1",
				ExecutionSafety:     model.SafetyNeedsOperatorApproval,
				ApprovalRequirement: model.ApprovalNeedsOperator,
				RollbackOutline:     "Keep the previous implementation behind a compatibility path until the new API path is verified.",
			})
			res.HiddenRisks = append(res.HiddenRisks, fmt.Sprintf("Deprecation warning observed: %s", msg))
		}
	}
	return res
}

func isDeprecationWarning(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "deprecated") || strings.Contains(s, "removed in")
}
