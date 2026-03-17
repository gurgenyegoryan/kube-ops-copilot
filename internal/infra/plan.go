package infra

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type TerraformPRPlan struct {
	APIVersion    string            `json:"apiVersion"`
	Kind          string            `json:"kind"`
	CreatedAt     time.Time         `json:"createdAt"`
	ApprovalID    string            `json:"approvalId,omitempty"`
	Summary       string            `json:"summary"`
	BranchName    string            `json:"branchName"`
	CommitMessage string            `json:"commitMessage"`
	PRTitle       string            `json:"prTitle"`
	PRBody        string            `json:"prBody"`
	Edits         []TerraformEdit   `json:"edits"`
	Verify        TerraformVerify   `json:"verify"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type TerraformEdit struct {
	Path    string `json:"path"`
	Search  string `json:"search"`
	Replace string `json:"replace"`
}

type TerraformVerify struct {
	Commands []string `json:"commands"`
	Notes    []string `json:"notes,omitempty"`
}

func LoadTerraformPRPlan(path string) (TerraformPRPlan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return TerraformPRPlan{}, err
	}
	var p TerraformPRPlan
	if err := json.Unmarshal(b, &p); err != nil {
		return TerraformPRPlan{}, err
	}
	if p.APIVersion == "" {
		p.APIVersion = "kube-ops-copilot/v1alpha1"
	}
	if p.Kind == "" {
		p.Kind = "TerraformPRPlan"
	}
	return p, nil
}

func (p TerraformPRPlan) Validate() error {
	if p.Kind != "" && p.Kind != "TerraformPRPlan" {
		return fmt.Errorf("unsupported kind: %q", p.Kind)
	}
	if strings.TrimSpace(p.Summary) == "" {
		return fmt.Errorf("plan.summary is required")
	}
	if strings.TrimSpace(p.BranchName) == "" {
		return fmt.Errorf("plan.branchName is required")
	}
	if strings.TrimSpace(p.CommitMessage) == "" {
		return fmt.Errorf("plan.commitMessage is required")
	}
	if strings.TrimSpace(p.PRTitle) == "" {
		return fmt.Errorf("plan.prTitle is required")
	}
	if len(p.Edits) == 0 {
		return fmt.Errorf("plan.edits must contain at least one edit")
	}
	for i, e := range p.Edits {
		if strings.TrimSpace(e.Path) == "" {
			return fmt.Errorf("plan.edits[%d].path is required", i)
		}
		if filepath.IsAbs(e.Path) {
			return fmt.Errorf("plan.edits[%d].path must be relative", i)
		}
		clean := filepath.Clean(e.Path)
		if clean == "." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
			return fmt.Errorf("plan.edits[%d].path must stay within repo", i)
		}
		if strings.TrimSpace(e.Search) == "" {
			return fmt.Errorf("plan.edits[%d].search is required", i)
		}
		if e.Search == e.Replace {
			return fmt.Errorf("plan.edits[%d] has identical search/replace", i)
		}
	}
	return nil
}
