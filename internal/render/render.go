package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

type Format string

const (
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
)

func Write(w io.Writer, format Format, report model.Report) error {
	switch format {
	case FormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	case FormatMarkdown:
		_, err := io.WriteString(w, Markdown(report))
		return err
	default:
		return fmt.Errorf("unsupported output format: %q", format)
	}
}

func Markdown(r model.Report) string {
	b := &strings.Builder{}

	fmt.Fprintf(b, "### Executive Summary\n%s\n\n", strings.TrimSpace(r.ExecutiveSummary))
	fmt.Fprintf(b, "### Snapshot Assessment\n")
	fmt.Fprintf(b, "- production readiness score: %d/100\n", r.Assessment.ProductionReadinessScore)
	fmt.Fprintf(b, "- operational risk: %s\n", r.Assessment.OperationalRisk)
	fmt.Fprintf(b, "- automation confidence: %s\n", r.Assessment.AutomationConfidence)
	fmt.Fprintf(b, "- observability coverage: %s\n", r.Assessment.ObservabilityCoverage)
	if len(r.Assessment.TopRiskThemes) == 0 {
		fmt.Fprintf(b, "- top risk themes: none\n")
	} else {
		fmt.Fprintf(b, "- top risk themes: %s\n", strings.Join(r.Assessment.TopRiskThemes, ", "))
	}
	if len(r.Assessment.ConfidenceLimiters) == 0 {
		fmt.Fprintf(b, "- confidence limiters: none\n\n")
	} else {
		fmt.Fprintf(b, "- confidence limiters:\n")
		for _, limiter := range r.Assessment.ConfidenceLimiters {
			fmt.Fprintf(b, "  - %s\n", limiter)
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### Key Findings\n")
	if len(r.KeyFindings) == 0 {
		fmt.Fprintf(b, "- none\n\n")
	} else {
		for _, f := range r.KeyFindings {
			fmt.Fprintf(b, "- title: %s\n  severity: %s\n  urgency: %s\n  confidence: %s\n  affected scope: %s\n  why it matters: %s\n  automation suitability: %s\n",
				f.Title, f.Severity, f.Urgency, f.Confidence, f.AffectedScope, f.WhyItMatters, f.AutomationSuitability)
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### Evidence\n")
	if len(r.Evidence) == 0 {
		fmt.Fprintf(b, "- none\n\n")
	} else {
		for _, e := range r.Evidence {
			fmt.Fprintf(b, "- %s\n", e.Signal)
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### Likely Root Cause Hypotheses\n")
	if len(r.Hypotheses) == 0 {
		fmt.Fprintf(b, "- none\n\n")
	} else {
		for _, h := range r.Hypotheses {
			fmt.Fprintf(b, "- rank: %d\n  probability: %s\n  description: %s\n", h.Rank, h.Probability, h.Description)
			if len(h.SupportingEvidence) > 0 {
				fmt.Fprintf(b, "  supporting evidence:\n")
				for _, e := range h.SupportingEvidence {
					fmt.Fprintf(b, "    - %s\n", e.Signal)
				}
			}
			if len(h.ContradictoryOrMissing) > 0 {
				fmt.Fprintf(b, "  contradictory/missing evidence:\n")
				for _, e := range h.ContradictoryOrMissing {
					fmt.Fprintf(b, "    - %s\n", e.Signal)
				}
			}
			if len(h.WhatToVerifyNext) > 0 {
				fmt.Fprintf(b, "  what to verify next:\n")
				for _, s := range h.WhatToVerifyNext {
					fmt.Fprintf(b, "    - %s\n", s)
				}
			}
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### Recommended Actions\n")
	writeActions := func(title string, actions []model.Action) {
		fmt.Fprintf(b, "- %s:\n", title)
		if len(actions) == 0 {
			fmt.Fprintf(b, "  - none\n")
			return
		}
		for _, a := range actions {
			fmt.Fprintf(b, "  - title: %s\n    expected benefit: %s\n    risk/tradeoff: %s\n    priority: %s\n    execution safety: %s\n    approval requirement: %s\n    rollback outline: %s\n",
				a.Title, a.ExpectedBenefit, a.RiskTradeoff, a.Priority, a.ExecutionSafety, a.ApprovalRequirement, a.RollbackOutline)
		}
	}
	writeActions("immediate actions", r.Recommended.Immediate)
	writeActions("short-term actions", r.Recommended.ShortTerm)
	writeActions("preventive improvements", r.Recommended.Preventive)
	fmt.Fprintf(b, "\n")

	fmt.Fprintf(b, "### Proposed Operator Message\n%s\n\n", strings.TrimSpace(r.ProposedOperatorMessage))

	if r.ExecutionPlan != nil {
		fmt.Fprintf(b, "### Execution Plan\n")
		fmt.Fprintf(b, "- target resource: %s\n- exact intended change: %s\n- execution safety: %s\n- approval requirement: %s\n", r.ExecutionPlan.TargetResource, r.ExecutionPlan.ExactIntendedChange, r.ExecutionPlan.ExecutionSafety, r.ExecutionPlan.ApprovalRequirement)
		if len(r.ExecutionPlan.SafeExecutionNotes) > 0 {
			fmt.Fprintf(b, "- safe execution notes:\n")
			for _, n := range r.ExecutionPlan.SafeExecutionNotes {
				fmt.Fprintf(b, "  - %s\n", n)
			}
		}
		if len(r.ExecutionPlan.RollbackPlan) > 0 {
			fmt.Fprintf(b, "- rollback plan:\n")
			for _, n := range r.ExecutionPlan.RollbackPlan {
				fmt.Fprintf(b, "  - %s\n", n)
			}
		}
		if len(r.ExecutionPlan.PostChangeVerification) > 0 {
			fmt.Fprintf(b, "- post-change verification:\n")
			for _, n := range r.ExecutionPlan.PostChangeVerification {
				fmt.Fprintf(b, "  - %s\n", n)
			}
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### Hidden Risks / What Humans Might Miss\n")
	if len(r.HiddenRisks) == 0 {
		fmt.Fprintf(b, "- none\n\n")
	} else {
		for _, s := range r.HiddenRisks {
			fmt.Fprintf(b, "- %s\n", s)
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### Unknowns\n")
	if len(r.Unknowns) == 0 {
		fmt.Fprintf(b, "- none\n\n")
	} else {
		for _, s := range r.Unknowns {
			fmt.Fprintf(b, "- %s\n", s)
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "### Final Operator Verdict\n%s\n\n", strings.TrimSpace(r.FinalVerdict))
	fmt.Fprintf(b, "_generated at %s_\n", r.GeneratedAt.UTC().Format(time.RFC3339))

	return b.String()
}
