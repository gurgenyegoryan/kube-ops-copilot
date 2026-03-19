package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kexec "github.com/gurgenyegoryan/kube-ops-copilot/internal/exec"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/notify"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/testutil/fakekube"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestExecuteCommandFlowWithProgressAndNotification(t *testing.T) {
	prevKube := cliNewKubeClient
	prevNotifier := cliNewNotifierFromEnv
	defer func() {
		cliNewKubeClient = prevKube
		cliNewNotifierFromEnv = prevNotifier
	}()

	currentReplicas := int32(1)
	generation := int64(1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/apis/apps/v1/namespaces/payments/deployments/payments-api":
			currentReplicas = 2
			generation = 2
			fakekube.WriteJSON(w, http.StatusOK, executeTestDeployment(currentReplicas, generation))
		case r.Method == http.MethodGet && r.URL.Path == "/apis/apps/v1/namespaces/payments/deployments/payments-api":
			fakekube.WriteJSON(w, http.StatusOK, executeTestDeployment(currentReplicas, generation))
		default:
			http.NotFound(w, r)
		}
	})
	kclient, closeFn, err := fakekube.NewClient(handler)
	if err != nil {
		t.Fatalf("new fake kube client: %v", err)
	}
	defer closeFn()
	cliNewKubeClient = func(cfg kube.Config) (*kube.Client, error) {
		return kclient, nil
	}

	rec := &recordingNotifier{}
	cliNewNotifierFromEnv = func() notify.Notifier { return rec }

	replicas := int32(2)
	plan := kexec.Plan{
		APIVersion: "kube-ops-copilot/v1alpha1",
		Kind:       "ExecutionPlan",
		ApprovalID: "approval-exec-1",
		Operation: kexec.Operation{
			Type:      kexec.OpScaleDeployment,
			Namespace: "payments",
			Name:      "payments-api",
			Replicas:  &replicas,
			Reason:    "remove single replica SPOF",
		},
		Verify: kexec.VerifyPlan{TimeoutSeconds: 1},
	}
	planPath := writeJSONPlan(t, plan)

	var out bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"execute",
		"--plan", planPath,
		"--approval-id", "approval-exec-1",
		"--approve",
		"--dry-run=false",
		"--notify",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute command failed: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"[progress] execute: loading execution plan",
		"[progress] execute: connecting to cluster",
		"[progress] execute: executing approved remediation",
		"[event] execute: patching deployment scale to 2",
		"[event] execute: verifying deployment rollout",
		"[done] execute: approved remediation executed",
		"executed: op=scale_deployment target=deployment/payments/payments-api verified=true",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, output)
		}
	}

	if len(rec.Messages) != 1 {
		t.Fatalf("expected one notification, got %+v", rec.Messages)
	}
	if rec.Messages[0].Title != "kube-ops-copilot execute (applied)" {
		t.Fatalf("unexpected notification title: %+v", rec.Messages[0])
	}
	if !strings.Contains(rec.Messages[0].Body, "approvalId=approval-exec-1") || !strings.Contains(rec.Messages[0].Body, "verified=true") {
		t.Fatalf("unexpected notification body: %q", rec.Messages[0].Body)
	}
}

func TestInfraExecuteCommandFlowWithProgress(t *testing.T) {
	repoPath := initTestRepo(t)
	mainTF := filepath.Join(repoPath, "main.tf")
	if err := os.WriteFile(mainTF, []byte("replicas = 1\n"), 0o644); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	runGit(t, repoPath, "add", "main.tf")
	runGit(t, repoPath, "commit", "-m", "add main.tf")

	plan := infra.PRPlan{
		APIVersion:    "kube-ops-copilot/v1alpha1",
		Kind:          "InfraPRPlan",
		Backend:       infra.BackendTerraform,
		ApprovalID:    "approval-infra-1",
		Summary:       "Persist replica floor",
		BranchName:    "koc/payments-api-replica-floor",
		CommitMessage: "Set payments-api replicas to 2",
		PRTitle:       "Set payments-api replicas to 2",
		PRBody:        "Keeps replica floor durable.",
		Edits: []infra.Edit{
			{
				Type:    infra.EditTypeSearchReplace,
				Path:    "main.tf",
				Search:  "replicas = 1",
				Replace: "replicas = 2",
			},
		},
		Verify: infra.Verify{Commands: []string{"terraform validate"}},
	}
	planPath := writeJSONPlan(t, plan)

	var out bytes.Buffer
	cmd := NewRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"infra", "execute",
		"--plan", planPath,
		"--infra-repo-path", repoPath,
		"--approval-id", "approval-infra-1",
		"--apply",
		"--skip-fmt",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("infra execute command failed: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"[progress] infra-execute: loading infrastructure plan",
		"[progress] infra-execute: executing infrastructure plan",
		"[event] infra-execute: checking git worktree cleanliness",
		"[event] infra-execute: applying 1 repo edit(s)",
		"[event] infra-execute: creating branch koc/payments-api-replica-floor",
		"[done] infra-execute: infrastructure plan executed",
		"infra execute: branch=koc/payments-api-replica-floor commit=",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, output)
		}
	}

	content, err := os.ReadFile(mainTF)
	if err != nil {
		t.Fatalf("read main.tf: %v", err)
	}
	if !strings.Contains(string(content), "replicas = 2") {
		t.Fatalf("expected repo file to be updated, got:\n%s", string(content))
	}

	result, err := infra.LoadResult(infra.ResultPathForPlan(planPath))
	if err != nil {
		t.Fatalf("load infra result: %v", err)
	}
	if result.BranchName != "koc/payments-api-replica-floor" || result.CommitSHA == "" {
		t.Fatalf("unexpected infra result: %+v", result)
	}
}

type recordingNotifier struct {
	Messages []notify.Message
}

func (r *recordingNotifier) Send(ctx context.Context, msg notify.Message) error {
	_ = ctx
	r.Messages = append(r.Messages, msg)
	return nil
}

func writeJSONPlan(t *testing.T, payload any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.json")
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	return path
}

func executeTestDeployment(replicas int32, generation int64) appsv1.Deployment {
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
