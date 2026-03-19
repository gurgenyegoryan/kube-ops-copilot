package runtimemetrics

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/testutil/fakekube"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestAnalyzerRunProducesRuntimeHotspotFinding(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/metrics.k8s.io/v1beta1/pods":
			fakekube.WriteJSON(w, http.StatusOK, map[string]any{
				"apiVersion": "metrics.k8s.io/v1beta1",
				"kind":       "PodMetricsList",
				"items": []any{
					map[string]any{
						"apiVersion": "metrics.k8s.io/v1beta1",
						"kind":       "PodMetrics",
						"metadata": map[string]any{
							"name":      "checkout-api-7f8d9",
							"namespace": "payments",
						},
						"containers": []any{
							map[string]any{
								"name": "api",
								"usage": map[string]any{
									"cpu":    "650m",
									"memory": "820Mi",
								},
							},
						},
					},
				},
			})
		case "/api/v1/pods":
			fakekube.WriteJSON(w, http.StatusOK, corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "checkout-api-7f8d9",
							Namespace: "payments",
							OwnerReferences: []metav1.OwnerReference{
								{Name: "checkout-api-7f8d9", Kind: "ReplicaSet", Controller: ptr.To(true)},
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "api",
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU:    mustParseQuantity(t, "500m"),
											corev1.ResourceMemory: mustParseQuantity(t, "768Mi"),
										},
									},
								},
							},
						},
					},
				},
			})
		case "/apis/apps/v1/replicasets":
			fakekube.WriteJSON(w, http.StatusOK, appsv1.ReplicaSetList{
				Items: []appsv1.ReplicaSet{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "checkout-api-7f8d9",
							Namespace: "payments",
							OwnerReferences: []metav1.OwnerReference{
								{Name: "checkout-api", Kind: "Deployment", Controller: ptr.To(true)},
							},
						},
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})

	client, closeFn, err := fakekube.NewClient(handler)
	if err != nil {
		t.Fatalf("new fake kube client: %v", err)
	}
	defer closeFn()

	an := Analyzer{Client: client}
	res, err := an.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !containsRuntimeEvidence(res.Evidence, "runtime metrics adapter: queried pod metrics for 1 pods across 1 workload(s)") {
		t.Fatalf("missing runtime metrics summary evidence: %+v", res.Evidence)
	}
	if !containsRuntimeEvidence(res.Evidence, "runtime hotspot: payments/Deployment/checkout-api score=") {
		t.Fatalf("missing runtime hotspot evidence: %+v", res.Evidence)
	}
	if !containsRuntimeEvidence(res.Evidence, "cpu=650m cpuRequests=500m") {
		t.Fatalf("missing CPU saturation evidence: %+v", res.Evidence)
	}
	if !containsRuntimeFinding(res.Findings, "1 workload(s) are running hot against live CPU/memory requests") {
		t.Fatalf("missing runtime finding: %+v", res.Findings)
	}
	if len(res.Recommended.Immediate) == 0 {
		t.Fatalf("expected immediate recommendation, got %+v", res.Recommended.Immediate)
	}
	if len(res.HiddenRisks) == 0 || !strings.Contains(res.HiddenRisks[0], "Point-in-time runtime pressure") {
		t.Fatalf("missing hidden risk: %+v", res.HiddenRisks)
	}
}

func mustParseQuantity(t *testing.T, raw string) resource.Quantity {
	t.Helper()
	parsed, err := resource.ParseQuantity(raw)
	if err != nil {
		t.Fatalf("parse quantity %q: %v", raw, err)
	}
	return parsed
}

func containsRuntimeEvidence(evidence []model.Evidence, want string) bool {
	for _, item := range evidence {
		if strings.Contains(item.Signal, want) {
			return true
		}
	}
	return false
}

func containsRuntimeFinding(findings []model.Finding, want string) bool {
	for _, item := range findings {
		if strings.Contains(item.Title, want) {
			return true
		}
	}
	return false
}
