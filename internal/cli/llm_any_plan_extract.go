package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
)

type anyPlan struct {
	Exec     *exec.Plan
	Infra    *infra.PRPlan
	Compound *compoundPlan
}

func extractAnyPlan(llmText string) (anyPlan, error) {
	m := jsonFenceRe.FindStringSubmatch(llmText)
	if len(m) < 2 {
		return anyPlan{}, fmt.Errorf("LLM response did not contain a ```json fenced plan")
	}
	payload := strings.TrimSpace(m[1])
	if payload == "null" {
		return anyPlan{}, nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return anyPlan{}, fmt.Errorf("parse plan json: %w", err)
	}
	kind, _ := raw["kind"].(string)
	switch kind {
	case "ExecutionPlan":
		var p exec.Plan
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return anyPlan{}, err
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		if err := p.Validate(); err != nil {
			return anyPlan{}, err
		}
		return anyPlan{Exec: &p}, nil
	case "InfraPRPlan", "TerraformPRPlan":
		var p infra.PRPlan
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return anyPlan{}, err
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		if err := p.Validate(); err != nil {
			return anyPlan{}, err
		}
		return anyPlan{Infra: &p}, nil
	case "CompoundRemediationPlan":
		var p compoundPlan
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return anyPlan{}, err
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		if err := p.Validate(); err != nil {
			return anyPlan{}, err
		}
		return anyPlan{Compound: &p}, nil
	default:
		return anyPlan{}, fmt.Errorf("unsupported plan kind from LLM: %q", kind)
	}
}
