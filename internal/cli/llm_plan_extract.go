package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
)

var jsonFenceRe = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\}|null)\\s*```")

func extractAndValidatePlan(llmText string) (*exec.Plan, error) {
	m := jsonFenceRe.FindStringSubmatch(llmText)
	if len(m) < 2 {
		return nil, fmt.Errorf("LLM response did not contain a ```json fenced ExecutionPlan")
	}
	payload := strings.TrimSpace(m[1])
	if payload == "null" {
		return nil, nil
	}
	var p exec.Plan
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, fmt.Errorf("parse plan json: %w", err)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid plan from LLM: %w", err)
	}
	return &p, nil
}

func stripJSONPlanBlock(llmText string) string {
	return strings.TrimSpace(jsonFenceRe.ReplaceAllString(llmText, ""))
}
