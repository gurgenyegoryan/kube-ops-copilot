package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	kexec "github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/testutil/fakekube"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/spf13/cobra"
)

func TestExecuteSmartCompoundPlanAppliesLiveAndInfraPhases(t *testing.T) {
	if _, err := osexec.LookPath("git"); err != nil {
		t.Skip("git is required for compound workflow integration test")
	}

	repoPath := initTestRepo(t)
	mainTF := filepath.Join(repoPath, "main.tf")
	if err := os.WriteFile(mainTF, []byte("replicas = 1\n"), 0o644); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	runGit(t, repoPath, "add", "main.tf")
	runGit(t, repoPath, "commit", "-m", "add main.tf")

	currentReplicas := int32(1)
	generation := int64(1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/apps/v1/namespaces/payments/deployments/payments-api":
			currentReplicas = 2
			generation = 2
			fakekube.WriteJSON(w, http.StatusOK, deploymentForTest(currentReplicas, generation))
		case r.Method == http.MethodGet && r.URL.Path == "/apis/apps/v1/namespaces/payments/deployments/payments-api":
			fakekube.WriteJSON(w, http.StatusOK, deploymentForTest(currentReplicas, generation))
		default:
			http.NotFound(w, r)
		}
	})

	kclient, closeFn, err := fakekube.NewClient(handler)
	if err != nil {
		t.Fatalf("new fake kube client: %v", err)
	}
	defer closeFn()

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	progress := newLiveProgress(&out, "smart-remediate")
	defer progress.Close()

	planPath := filepath.Join(t.TempDir(), "compound-plan.json")
	flags := smartRemediateFlags{
		InfraRepoPath:    repoPath,
		PlanOut:          planPath,
		Apply:            true,
		ApprovalProvider: "manual",
		WaitApproval:     false,
		SkipFmt:          true,
		RunValidate:      false,
		RequireClean:     true,
	}

	replicas := int32(2)
	plan := compoundPlan{
		APIVersion: "kube-ops-copilot/v1alpha1",
		Kind:       "CompoundRemediationPlan",
		Summary:    "Scale the live deployment and codify the replica floor",
		Live: &kexec.Plan{
			APIVersion: "kube-ops-copilot/v1alpha1",
			Kind:       "ExecutionPlan",
			Operation: kexec.Operation{
				Type:      kexec.OpScaleDeployment,
				Namespace: "payments",
				Name:      "payments-api",
				Replicas:  &replicas,
				Reason:    "remove a single-replica SPOF",
			},
			Verify: kexec.VerifyPlan{TimeoutSeconds: 1},
		},
		Infra: &infra.PRPlan{
			APIVersion:    "kube-ops-copilot/v1alpha1",
			Kind:          "InfraPRPlan",
			Backend:       infra.BackendTerraform,
			Summary:       "Persist the replica floor in IaC",
			BranchName:    "koc/payments-api-replica-floor",
			CommitMessage: "Set payments-api replicas to 2",
			PRTitle:       "Set payments-api replicas to 2",
			PRBody:        "Keeps the live fix durable.",
			Edits: []infra.Edit{
				{
					Type:    infra.EditTypeSearchReplace,
					Path:    "main.tf",
					Search:  "replicas = 1",
					Replace: "replicas = 2",
				},
			},
			Verify: infra.Verify{Commands: []string{"terraform validate"}},
		},
	}

	if err := executeSmartCompoundPlan(context.Background(), progress, cmd, flags, "manual", "approval-123", "compound remediation requested", plan, kclient.Kubernetes, "repo inventory snapshot"); err != nil {
		t.Fatalf("executeSmartCompoundPlan: %v", err)
	}

	output := out.String()
	for _, want := range []string{
		"approval requested: provider=manual approval-id=approval-123",
		"compound live mitigation applied: op=scale_deployment target=deployment/payments/payments-api verified=true",
		"compound infra remediation applied: backend=terraform branch=koc/payments-api-replica-floor",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected command output to contain %q, got:\n%s", want, output)
		}
	}

	if got, err := os.ReadFile(mainTF); err != nil {
		t.Fatalf("read main.tf: %v", err)
	} else if !strings.Contains(string(got), "replicas = 2") {
		t.Fatalf("expected infra phase to update repo file, got:\n%s", string(got))
	}

	if branch := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "--abbrev-ref", "HEAD")); branch != "koc/payments-api-replica-floor" {
		t.Fatalf("unexpected current branch: %s", branch)
	}

	result, err := infra.LoadResult(infra.ResultPathForPlan(planPath))
	if err != nil {
		t.Fatalf("load infra result: %v", err)
	}
	if result.BranchName != "koc/payments-api-replica-floor" || len(result.AppliedFiles) != 1 || result.AppliedFiles[0] != "main.tf" {
		t.Fatalf("unexpected infra result: %+v", result)
	}

	execResult := compoundExecutionResult{}
	b, err := os.ReadFile(compoundResultPath(planPath))
	if err != nil {
		t.Fatalf("read compound result: %v", err)
	}
	if err := json.Unmarshal(b, &execResult); err != nil {
		t.Fatalf("unmarshal compound result: %v", err)
	}
	if execResult.Live == nil || execResult.Live.Status != compoundPhaseSucceeded {
		t.Fatalf("expected live phase succeeded, got %+v", execResult.Live)
	}
	if execResult.Infra == nil || execResult.Infra.Status != compoundPhaseSucceeded {
		t.Fatalf("expected infra phase succeeded, got %+v", execResult.Infra)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	repoPath := t.TempDir()
	runGit(t, repoPath, "init")
	runGit(t, repoPath, "config", "user.name", "Kube Ops Copilot Test")
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte(""), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runGit(t, repoPath, "add", ".gitignore")
	runGit(t, repoPath, "commit", "-m", "init repo")
	return repoPath
}

func runGit(t *testing.T, workdir string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

func deploymentForTest(replicas int32, generation int64) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "payments-api",
			Namespace:  "payments",
			Generation: generation,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: generation,
			UpdatedReplicas:    replicas,
			AvailableReplicas:  replicas,
		},
	}
}
