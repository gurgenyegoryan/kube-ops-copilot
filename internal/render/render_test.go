package render

import (
	"strings"
	"testing"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

func TestMarkdownIncludesRequiredSections(t *testing.T) {
	r := model.Report{
		GeneratedAt:             time.Unix(1700000000, 0),
		ExecutiveSummary:        "something important",
		ProposedOperatorMessage: "hello ops",
		FinalVerdict:            "do this next",
	}
	out := Markdown(r)

	req := []string{
		"### Executive Summary",
		"### Key Findings",
		"### Evidence",
		"### Likely Root Cause Hypotheses",
		"### Recommended Actions",
		"### Proposed Operator Message",
		"### Hidden Risks / What Humans Might Miss",
		"### Unknowns",
		"### Final Operator Verdict",
	}
	for _, s := range req {
		if !strings.Contains(out, s) {
			t.Fatalf("missing section %q", s)
		}
	}
}
