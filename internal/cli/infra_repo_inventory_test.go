package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
)

func TestBuildTerraformRepoInventoryPrioritizesSemanticAppTargets(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	write("Makefile", `
.PHONY: fmt
fmt:
	terragrunt hclfmt --recursive -check .
`)
	write("stacks/stage/services/analytics-service/terragrunt.hcl", `
terraform {
  source = "../../../../modules//deploy/helm_release"
}

include "root" {
  path   = find_in_parent_folders()
  expose = true
}

locals {
  service_name = "analytics-service"
}

inputs = {
  name        = "acme-analytics-service"
  chart_path  = "${get_original_terragrunt_dir()}/../../../../charts/${local.service_name}"
  values_file = "${get_original_terragrunt_dir()}/../../../../charts/${local.service_name}/values-stage.yaml"
}
`)
	write("stacks/stage/services/analytics-service/.terraform.lock.hcl", `
provider "registry.terraform.io/hashicorp/aws" {}
`)
	write("charts/analytics-service/values-stage.yaml", `
replicaCount: 2
resources:
  requests:
    cpu: 200m
`)
	write("modules/deploy/helm_release/main.tf", `
resource "helm_release" "this" {
  name      = var.name
  chart     = var.chart_path
  namespace = var.cluster_namespace
  values    = [file(var.values_file)]
}
`)
	write(".terragrunt-cache/hidden/stacks/stage/services/analytics-service/terragrunt.hcl", `
terraform {
  source = "../../../../modules//deploy/helm_release"
}
`)
	write(".terraform/modules/generated/main.tf", `
resource "null_resource" "generated" {}
`)
	write(".helm-cache/analytics-service/values-stage.yaml", `
replicaCount: 99
`)

	hint := `workload pressure observed for acme-stage/analytics-service and acme-analytics-service in stage environment`
	out, err := buildTerraformRepoInventory(repo, hint, 8, 16000)
	if err != nil {
		t.Fatalf("buildTerraformRepoInventory: %v", err)
	}

	if !strings.Contains(out, "stacks/stage/services/analytics-service/terragrunt.hcl") {
		t.Fatalf("expected analytics-service terragrunt file in inventory output:\n%s", out)
	}
	if !strings.Contains(out, "charts/analytics-service/values-stage.yaml") {
		t.Fatalf("expected values-stage.yaml in inventory output:\n%s", out)
	}
	if !strings.Contains(out, "modules/deploy/helm_release/main.tf") {
		t.Fatalf("expected helm_release module file in inventory output:\n%s", out)
	}
	if strings.Contains(out, ".terragrunt-cache/") {
		t.Fatalf("did not expect .terragrunt-cache content in inventory output:\n%s", out)
	}
	if strings.Contains(out, ".terraform/") {
		t.Fatalf("did not expect .terraform content in inventory output:\n%s", out)
	}
	if strings.Contains(out, ".helm-cache/") {
		t.Fatalf("did not expect .helm-cache content in inventory output:\n%s", out)
	}

	idxApp := strings.Index(out, "- stacks/stage/services/analytics-service/terragrunt.hcl")
	idxMake := strings.Index(out, "- Makefile")
	if idxApp == -1 {
		t.Fatalf("expected candidate list to include app terragrunt file:\n%s", out)
	}
	if idxMake != -1 && idxMake < idxApp {
		t.Fatalf("expected app terragrunt file to rank ahead of Makefile:\n%s", out)
	}
	if strings.Contains(out, "--- FILE: stacks/stage/services/analytics-service/.terraform.lock.hcl ---") {
		t.Fatalf("did not expect .terraform.lock.hcl to be sampled into file contents:\n%s", out)
	}
}

func TestExtractSemanticHintsFromReportJSONPrefersRealWorkloads(t *testing.T) {
	rep := model.Report{
		ExecutiveSummary: "Runtime pressure observed on acme-frontend and acme-app-service.",
		KeyFindings: []model.Finding{
			{
				Title:         "Runtime pressure exceeds requests",
				AffectedScope: "workloads/runtime-metrics",
				WhyItMatters:  "Short-window metrics show acme-analytics-service and acme-read-service near memory requests.",
			},
		},
		Evidence: []model.Evidence{
			{Signal: "namespace/workload: acme-test/acme-frontend"},
			{Signal: "namespace/workload: acme-test/acme-app-service"},
			{Signal: "time-series confirmation not available in this point-in-time snapshot"},
		},
		HiddenRisks: []string{
			"Production-readiness narrative should not outrank real workloads like acme-frontend.",
		},
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}

	hints := extractSemanticHints(string(raw))
	joinedWorkloads := strings.Join(hints.Workloads, ",")
	joinedAliases := strings.Join(hints.Aliases, ",")

	if !strings.Contains(joinedWorkloads, "acme-frontend") {
		t.Fatalf("expected acme-frontend in workloads, got %v", hints.Workloads)
	}
	if !strings.Contains(joinedWorkloads, "acme-app-service") {
		t.Fatalf("expected acme-app-service in workloads, got %v", hints.Workloads)
	}
	if !strings.Contains(joinedAliases, "frontend") {
		t.Fatalf("expected frontend alias, got %v", hints.Aliases)
	}
	if !strings.Contains(joinedAliases, "app-service") {
		t.Fatalf("expected app-service alias, got %v", hints.Aliases)
	}
	if strings.Contains(joinedWorkloads, "production-readiness") || strings.Contains(joinedWorkloads, "point-in-time") {
		t.Fatalf("unexpected narrative noise in workloads: %v", hints.Workloads)
	}
}
