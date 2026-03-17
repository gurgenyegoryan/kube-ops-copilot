package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
)

func extractAndValidateTerraformPRPlan(llmText string) (*infra.TerraformPRPlan, error) {
	m := jsonFenceRe.FindStringSubmatch(llmText)
	if len(m) < 2 {
		return nil, fmt.Errorf("LLM response did not contain a ```json fenced TerraformPRPlan")
	}
	payload := strings.TrimSpace(m[1])
	if payload == "null" {
		return nil, nil
	}
	var p infra.TerraformPRPlan
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, fmt.Errorf("parse terraform plan json: %w", err)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid terraform plan from LLM: %w", err)
	}
	return &p, nil
}
