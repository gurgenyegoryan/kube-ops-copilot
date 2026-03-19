package infra

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BackendType string

const (
	BackendTerraform  BackendType = "terraform"
	BackendOpenTofu   BackendType = "opentofu"
	BackendTerragrunt BackendType = "terragrunt"
)

type PRPlan struct {
	APIVersion    string            `json:"apiVersion"`
	Kind          string            `json:"kind"`
	Backend       BackendType       `json:"backend"`
	CreatedAt     time.Time         `json:"createdAt"`
	ApprovalID    string            `json:"approvalId,omitempty"`
	Summary       string            `json:"summary"`
	AgentPrompt   string            `json:"agentPrompt,omitempty"`
	BranchName    string            `json:"branchName"`
	CommitMessage string            `json:"commitMessage"`
	PRTitle       string            `json:"prTitle"`
	PRBody        string            `json:"prBody"`
	Edits         []Edit            `json:"edits"`
	Verify        Verify            `json:"verify"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type EditType string

const (
	EditTypeSearchReplace      EditType = "search_replace"
	EditTypeHCLSetAttribute    EditType = "hcl_set_attribute"
	EditTypeHCLDeleteAttribute EditType = "hcl_delete_attribute"
	EditTypeHCLReplaceBlock    EditType = "hcl_replace_block"
	EditTypeHCLAppendBlockBody EditType = "hcl_append_block_body"
)

type Edit struct {
	Type      EditType `json:"type,omitempty"`
	Path      string   `json:"path"`
	Search    string   `json:"search,omitempty"`
	Replace   string   `json:"replace,omitempty"`
	BlockType string   `json:"blockType,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Attribute string   `json:"attribute,omitempty"`
	ValueHCL  string   `json:"valueHCL,omitempty"`
	BlockHCL  string   `json:"blockHCL,omitempty"`
}

type Verify struct {
	Commands []string `json:"commands"`
	Notes    []string `json:"notes,omitempty"`
}

type TerraformPRPlan = PRPlan
type TerraformEdit = Edit
type TerraformVerify = Verify

func LoadPlan(path string) (PRPlan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return PRPlan{}, err
	}
	var p PRPlan
	if err := json.Unmarshal(b, &p); err != nil {
		return PRPlan{}, err
	}
	if p.APIVersion == "" {
		p.APIVersion = "kube-ops-copilot/v1alpha1"
	}
	if p.Kind == "" {
		p.Kind = "InfraPRPlan"
	}
	if p.Backend == "" {
		p.Backend = BackendTerraform
	}
	return p, nil
}

func LoadTerraformPRPlan(path string) (TerraformPRPlan, error) {
	return LoadPlan(path)
}

func (p PRPlan) Validate() error {
	if p.Kind != "" && p.Kind != "InfraPRPlan" && p.Kind != "TerraformPRPlan" {
		return fmt.Errorf("unsupported kind: %q", p.Kind)
	}
	switch p.Backend {
	case BackendTerraform, BackendOpenTofu, BackendTerragrunt:
	case "":
		return fmt.Errorf("plan.backend is required")
	default:
		return fmt.Errorf("unsupported backend: %q", p.Backend)
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
		editType := e.Type
		if editType == "" {
			if strings.TrimSpace(e.BlockType) != "" && strings.TrimSpace(e.Attribute) != "" && strings.TrimSpace(e.ValueHCL) != "" {
				editType = EditTypeHCLSetAttribute
			} else {
				editType = EditTypeSearchReplace
			}
		}
		switch editType {
		case EditTypeSearchReplace:
			if strings.TrimSpace(e.Search) == "" {
				return fmt.Errorf("plan.edits[%d].search is required", i)
			}
			if e.Search == e.Replace {
				return fmt.Errorf("plan.edits[%d] has identical search/replace", i)
			}
		case EditTypeHCLSetAttribute:
			if strings.TrimSpace(e.BlockType) == "" {
				return fmt.Errorf("plan.edits[%d].blockType is required for hcl_set_attribute", i)
			}
			if strings.TrimSpace(e.Attribute) == "" {
				return fmt.Errorf("plan.edits[%d].attribute is required for hcl_set_attribute", i)
			}
			if strings.TrimSpace(e.ValueHCL) == "" {
				return fmt.Errorf("plan.edits[%d].valueHCL is required for hcl_set_attribute", i)
			}
		case EditTypeHCLDeleteAttribute:
			if strings.TrimSpace(e.BlockType) == "" {
				return fmt.Errorf("plan.edits[%d].blockType is required for hcl_delete_attribute", i)
			}
			if strings.TrimSpace(e.Attribute) == "" {
				return fmt.Errorf("plan.edits[%d].attribute is required for hcl_delete_attribute", i)
			}
		case EditTypeHCLReplaceBlock:
			if strings.TrimSpace(e.BlockType) == "" {
				return fmt.Errorf("plan.edits[%d].blockType is required for hcl_replace_block", i)
			}
			if strings.TrimSpace(e.BlockHCL) == "" {
				return fmt.Errorf("plan.edits[%d].blockHCL is required for hcl_replace_block", i)
			}
		case EditTypeHCLAppendBlockBody:
			if strings.TrimSpace(e.BlockType) == "" {
				return fmt.Errorf("plan.edits[%d].blockType is required for hcl_append_block_body", i)
			}
			if strings.TrimSpace(e.BlockHCL) == "" {
				return fmt.Errorf("plan.edits[%d].blockHCL is required for hcl_append_block_body", i)
			}
		default:
			return fmt.Errorf("unsupported edit type: %q", editType)
		}
	}
	return nil
}

type Result struct {
	Backend          BackendType `json:"backend"`
	BranchName       string      `json:"branchName"`
	CommitSHA        string      `json:"commitSHA"`
	Pushed           bool        `json:"pushed"`
	PullRequestURL   string      `json:"pullRequestURL,omitempty"`
	AppliedFiles     []string    `json:"appliedFiles"`
	StartedAt        time.Time   `json:"startedAt"`
	EndedAt          time.Time   `json:"endedAt"`
	ValidationOutput []string    `json:"validationOutput,omitempty"`
}

func ResultPathForPlan(planPath string) string {
	return planPath + ".result.json"
}

func WriteResult(path string, result Result) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func LoadResult(path string) (Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, err
	}
	return r, nil
}
