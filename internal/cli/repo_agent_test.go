package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/llm"
)

func TestMaybeNewRepoAgentWorker_CodexCLIExternalDirectEdits(t *testing.T) {
	repo := t.TempDir()
	target := filepath.Join(repo, "values.yaml")
	if err := os.WriteFile(target, []byte("replicaCount: 1\n"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	script := filepath.Join(repo, "fake-codex.sh")
	body := `#!/bin/sh
set -eu
out=""
repo=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -C)
      repo="$2"
      shift 2
      ;;
    -o)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
if [ -n "$repo" ]; then
  cd "$repo"
fi
cat >/tmp/koc-repo-agent-prompt.txt
printf 'replicaCount: 2\n' > values.yaml
if [ -n "$out" ]; then
  printf 'external codex summary' > "$out"
fi
printf 'stdout summary\n'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	worker, err := maybeNewRepoAgentWorker(context.Background(), nil, repoAgentFlags{
		Provider: "codex-cli",
		Command:  script,
	}, llm.Config{})
	if err != nil {
		t.Fatalf("maybeNewRepoAgentWorker: %v", err)
	}
	if worker == nil {
		t.Fatalf("expected external worker")
	}

	result, err := worker.Refine(context.Background(), infra.RefineRequest{
		RepoPath:      repo,
		RepoInventory: "values.yaml",
		Plan: infra.PRPlan{
			APIVersion:    "kube-ops-copilot/v1alpha1",
			Kind:          "InfraPRPlan",
			Backend:       infra.BackendTerraform,
			CreatedAt:     time.Now().UTC(),
			Summary:       "Increase replicas",
			BranchName:    "repo-agent/test",
			CommitMessage: "Increase replicas",
			PRTitle:       "Increase replicas",
			PRBody:        "Body",
			Edits: []infra.Edit{{
				Type:    infra.EditTypeSearchReplace,
				Path:    "values.yaml",
				Search:  "replicaCount: 1",
				Replace: "replicaCount: 2",
			}},
			Verify: infra.Verify{Commands: []string{"terraform fmt"}},
		},
	})
	if err != nil {
		t.Fatalf("Refine: %v", err)
	}
	if !result.DirectEdits {
		t.Fatalf("expected direct edits result")
	}
	if strings.TrimSpace(result.Narrative) != "external codex summary" {
		t.Fatalf("unexpected narrative: %q", result.Narrative)
	}
	edited, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if !strings.Contains(string(edited), "replicaCount: 2") {
		t.Fatalf("expected edited file, got %q", string(edited))
	}
}
