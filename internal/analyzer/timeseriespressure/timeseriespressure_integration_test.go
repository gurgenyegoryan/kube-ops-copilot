package timeseriespressure

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/capability"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/model"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/testutil/fakekube"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestAnalyzerRunProducesCorrelatedHotspotFromRuntimeAndStructuralSignals(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/pods":
			fakekube.WriteJSON(w, http.StatusOK, corev1.PodList{
				Items: []corev1.Pod{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "payments-api-7f8d9",
							Namespace: "payments",
							OwnerReferences: []metav1.OwnerReference{
								{Name: "payments-api-7f8d9", Kind: "ReplicaSet", Controller: ptr.To(true)},
							},
						},
					},
				},
			})
		case r.URL.Path == "/apis/apps/v1/replicasets":
			fakekube.WriteJSON(w, http.StatusOK, appsv1.ReplicaSetList{
				Items: []appsv1.ReplicaSet{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "payments-api-7f8d9",
							Namespace: "payments",
							OwnerReferences: []metav1.OwnerReference{
								{Name: "payments-api", Kind: "Deployment", Controller: ptr.To(true)},
							},
						},
					},
				},
			})
		case r.URL.Path == "/apis/apps/v1/deployments":
			fakekube.WriteJSON(w, http.StatusOK, appsv1.DeploymentList{
				Items: []appsv1.Deployment{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: "payments"},
						Spec: appsv1.DeploymentSpec{
							Replicas: ptr.To[int32](1),
							Template: corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "payments-api"}},
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{
										{Name: "api"},
									},
								},
							},
						},
					},
				},
			})
		case r.URL.Path == "/apis/apps/v1/statefulsets":
			fakekube.WriteJSON(w, http.StatusOK, appsv1.StatefulSetList{})
		case r.URL.Path == "/api/v1/services":
			fakekube.WriteJSON(w, http.StatusOK, corev1.ServiceList{
				Items: []corev1.Service{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "thanos-query", Namespace: "monitoring"},
						Spec: corev1.ServiceSpec{
							Ports: []corev1.ServicePort{{Name: "http", Port: 9090}},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "payments-api", Namespace: "payments"},
						Spec: corev1.ServiceSpec{
							Selector: map[string]string{"app": "payments-api"},
							Ports:    []corev1.ServicePort{{Name: "http", Port: 80}},
						},
					},
				},
			})
		case r.URL.Path == "/apis/networking.k8s.io/v1/ingresses":
			fakekube.WriteJSON(w, http.StatusOK, networkingv1.IngressList{
				Items: []networkingv1.Ingress{
					{
						ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "payments"},
						Spec: networkingv1.IngressSpec{
							Rules: []networkingv1.IngressRule{
								{
									IngressRuleValue: networkingv1.IngressRuleValue{
										HTTP: &networkingv1.HTTPIngressRuleValue{
											Paths: []networkingv1.HTTPIngressPath{
												{
													Backend: networkingv1.IngressBackend{
														Service: &networkingv1.IngressServiceBackend{Name: "payments-api", Port: networkingv1.ServiceBackendPort{Number: 80}},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			})
		case r.URL.Path == "/apis/autoscaling/v2/horizontalpodautoscalers":
			fakekube.WriteJSON(w, http.StatusOK, autoscalingv2.HorizontalPodAutoscalerList{})
		case r.URL.Path == "/apis/policy/v1/poddisruptionbudgets":
			fakekube.WriteJSON(w, http.StatusOK, policyv1.PodDisruptionBudgetList{})
		case r.URL.Path == "/apis/discovery.k8s.io/v1/endpointslices":
			fakekube.WriteJSON(w, http.StatusOK, discoveryv1.EndpointSliceList{
				Items: []discoveryv1.EndpointSlice{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "payments-api-1",
							Namespace: "payments",
							Labels:    map[string]string{"kubernetes.io/service-name": "payments-api"},
						},
						Endpoints: []discoveryv1.Endpoint{
							{Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)}},
						},
					},
				},
			})
		case strings.Contains(r.URL.Path, "thanos-query") && strings.HasSuffix(r.URL.Path, "/query"):
			query := r.URL.Query().Get("query")
			switch {
			case strings.Contains(query, "kube_pod_container_status_restarts_total") && strings.Contains(query, "exported_namespace,exported_pod"):
				fakekube.WriteJSON(w, http.StatusOK, vectorResult("exported_namespace", "exported_pod", "payments", "payments-api-7f8d9", "7"))
			case strings.Contains(query, "OOMKilled") && strings.Contains(query, "exported_namespace,exported_pod"):
				fakekube.WriteJSON(w, http.StatusOK, vectorResult("exported_namespace", "exported_pod", "payments", "payments-api-7f8d9", "1"))
			default:
				fakekube.WriteJSON(w, http.StatusOK, map[string]any{"status": "error", "error": "unsupported query"})
			}
		case strings.Contains(r.URL.Path, "thanos-query") && strings.HasSuffix(r.URL.Path, "/query_range"):
			query := r.URL.Query().Get("query")
			switch {
			case strings.Contains(query, "container_cpu_usage_seconds_total") && strings.Contains(query, "exported_namespace,exported_pod"):
				fakekube.WriteJSON(w, http.StatusOK, matrixResult("exported_namespace", "exported_pod", "payments", "payments-api-7f8d9", []string{"0.9", "1.4"}))
			case strings.Contains(query, "container_memory_working_set_bytes") && strings.Contains(query, "exported_namespace,exported_pod"):
				fakekube.WriteJSON(w, http.StatusOK, matrixResult("exported_namespace", "exported_pod", "payments", "payments-api-7f8d9", []string{"1073741824", "1610612736"}))
			case strings.Contains(query, "container_cpu_cfs_throttled_seconds_total") && strings.Contains(query, "exported_namespace,exported_pod"):
				fakekube.WriteJSON(w, http.StatusOK, matrixResult("exported_namespace", "exported_pod", "payments", "payments-api-7f8d9", []string{"0.08", "0.25"}))
			default:
				fakekube.WriteJSON(w, http.StatusOK, map[string]any{"status": "error", "error": "unsupported query"})
			}
		default:
			http.NotFound(w, r)
		}
	})

	client, closeFn, err := fakekube.NewClient(handler)
	if err != nil {
		t.Fatalf("new fake kube client: %v", err)
	}
	defer closeFn()

	an := Analyzer{
		Client: client,
		Inventory: capability.Inventory{
			"time_series_metrics": capability.Detected,
		},
	}

	res, err := an.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if !containsEvidenceSignal(res.Evidence, "time-series adapter selected: vendor=thanos service=monitoring/thanos-query port=http") {
		t.Fatalf("missing adapter selection evidence: %+v", res.Evidence)
	}
	if !containsEvidenceSignal(res.Evidence, "time-series query selected for restarts: topk(10, sum by (exported_namespace,exported_pod)") {
		t.Fatalf("missing exported-label restart query evidence: %+v", res.Evidence)
	}
	if !containsEvidenceSignal(res.Evidence, "time-series correlated hotspot: payments/Deployment/payments-api") {
		t.Fatalf("missing correlated hotspot evidence: %+v", res.Evidence)
	}
	if !containsFindingTitle(res.Findings, "1 workload(s) combine long-window runtime pressure with structural fragility") {
		t.Fatalf("missing correlation finding: %+v", res.Findings)
	}
	if len(res.Recommended.Immediate) == 0 {
		t.Fatalf("expected immediate recommendation, got %+v", res.Recommended.Immediate)
	}
	if len(res.HiddenRisks) == 0 || !strings.Contains(res.HiddenRisks[0], "single kubectl snapshot") {
		t.Fatalf("missing hidden risk: %+v", res.HiddenRisks)
	}
}

func vectorResult(nsLabel, podLabel, namespace, pod, value string) map[string]any {
	return map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "vector",
			"result": []any{
				map[string]any{
					"metric": map[string]any{nsLabel: namespace, podLabel: pod},
					"value":  []any{1710000000, value},
				},
			},
		},
	}
}

func matrixResult(nsLabel, podLabel, namespace, pod string, values []string) map[string]any {
	points := make([][]any, 0, len(values))
	ts := int64(1710000000)
	for _, value := range values {
		points = append(points, []any{ts, value})
		ts += 300
	}
	return map[string]any{
		"status": "success",
		"data": map[string]any{
			"resultType": "matrix",
			"result": []any{
				map[string]any{
					"metric": map[string]any{nsLabel: namespace, podLabel: pod},
					"values": points,
				},
			},
		},
	}
}

func containsEvidenceSignal(evidence []model.Evidence, want string) bool {
	for _, item := range evidence {
		if strings.Contains(item.Signal, want) {
			return true
		}
	}
	return false
}

func containsFindingTitle(findings []model.Finding, want string) bool {
	for _, item := range findings {
		if strings.Contains(item.Title, want) {
			return true
		}
	}
	return false
}
