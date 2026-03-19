package fakekube

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"k8s.io/client-go/kubernetes"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

func NewClient(handler http.Handler) (*kube.Client, func(), error) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Result(), nil
	})

	cfg := &rest.Config{
		Host:      "http://fake-kube.local",
		QPS:       1000,
		Burst:     1000,
		Transport: transport,
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: kubescheme.Codecs.WithoutConversion(),
		},
		UserAgent: rest.DefaultKubernetesUserAgent(),
	}

	httpClient := &http.Client{
		Transport: transport,
	}

	clientset, err := kubernetes.NewForConfigAndClient(cfg, httpClient)
	if err != nil {
		return nil, nil, fmt.Errorf("create kubernetes clientset: %w", err)
	}

	return &kube.Client{
		RESTConfig:       cfg,
		Kubernetes:       clientset,
		WarningCollector: kube.NewWarningCollector(),
	}, func() {}, nil
}

func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
