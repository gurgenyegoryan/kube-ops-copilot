package exec

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type Executor struct {
	Client *kubernetes.Clientset
}

type Result struct {
	Operation OperationType
	Target    string
	StartedAt time.Time
	EndedAt   time.Time
	Verified  bool
}

func (e *Executor) Apply(ctx context.Context, plan Plan) (Result, error) {
	if e.Client == nil {
		return Result{}, fmt.Errorf("kubernetes client is nil")
	}
	if err := plan.Validate(); err != nil {
		return Result{}, err
	}

	started := time.Now()
	switch plan.Operation.Type {
	case OpRolloutRestartDeployment:
		if err := e.rolloutRestartDeployment(ctx, plan.Operation.Namespace, plan.Operation.Name); err != nil {
			return Result{}, err
		}
		verified, err := e.verifyDeploymentRollout(ctx, plan.Operation.Namespace, plan.Operation.Name, plan.Verify.TimeoutSeconds)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Operation: plan.Operation.Type,
			Target:    fmt.Sprintf("deployment/%s/%s", plan.Operation.Namespace, plan.Operation.Name),
			StartedAt: started,
			EndedAt:   time.Now(),
			Verified:  verified,
		}, nil
	case OpScaleDeployment:
		replicas := int32(0)
		if plan.Operation.Replicas != nil {
			replicas = *plan.Operation.Replicas
		}
		if err := e.scaleDeployment(ctx, plan.Operation.Namespace, plan.Operation.Name, replicas); err != nil {
			return Result{}, err
		}
		verified, err := e.verifyDeploymentRollout(ctx, plan.Operation.Namespace, plan.Operation.Name, plan.Verify.TimeoutSeconds)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Operation: plan.Operation.Type,
			Target:    fmt.Sprintf("deployment/%s/%s", plan.Operation.Namespace, plan.Operation.Name),
			StartedAt: started,
			EndedAt:   time.Now(),
			Verified:  verified,
		}, nil
	default:
		return Result{}, fmt.Errorf("unsupported operation type: %q", plan.Operation.Type)
	}
}

func (e *Executor) rolloutRestartDeployment(ctx context.Context, namespace, name string) error {
	t := time.Now().UTC().Format(time.RFC3339)
	patch := []byte(fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kube-ops-copilot.io/restartedAt":"%s"}}}}}`, t))
	_, err := e.Client.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch deployment for restart: %w", err)
	}
	return nil
}

func (e *Executor) scaleDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas))
	_, err := e.Client.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch deployment replicas: %w", err)
	}
	return nil
}

func (e *Executor) verifyDeploymentRollout(ctx context.Context, namespace, name string, timeoutSeconds int) (bool, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 180
	}
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)

	for time.Now().Before(deadline) {
		d, err := e.Client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("get deployment for verify: %w", err)
		}
		if deploymentRolledOut(*d) {
			return true, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return false, fmt.Errorf("verification timeout waiting for deployment rollout")
}

func deploymentRolledOut(d appsv1.Deployment) bool {
	// Basic rollout gate: observed generation matches, desired replicas are updated & available.
	if d.Generation > d.Status.ObservedGeneration {
		return false
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	if d.Status.UpdatedReplicas < desired {
		return false
	}
	if d.Status.AvailableReplicas < desired {
		return false
	}
	return true
}
