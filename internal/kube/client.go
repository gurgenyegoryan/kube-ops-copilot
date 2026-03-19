package kube

import (
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Config struct {
	Kubeconfig string
	Context    string
}

type Client struct {
	RESTConfig       *rest.Config
	Kubernetes       *kubernetes.Clientset
	WarningCollector *WarningCollector
}

func NewClient(cfg Config) (*Client, error) {
	restCfg, err := loadRESTConfig(cfg)
	if err != nil {
		return nil, err
	}
	warnings := NewWarningCollector()
	restCfg.WarningHandler = warnings
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return &Client{RESTConfig: restCfg, Kubernetes: cs, WarningCollector: warnings}, nil
}

func loadRESTConfig(cfg Config) (*rest.Config, error) {
	// If kubeconfig is not specified, prefer in-cluster config. If not in-cluster,
	// fall back to kubectl-like loading rules (respects $KUBECONFIG and ~/.kube/config).
	if cfg.Kubeconfig == "" {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, nil
		}
	}

	var loader *clientcmd.ClientConfigLoadingRules
	if cfg.Kubeconfig != "" {
		loader = &clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.Kubeconfig}
	} else {
		loader = clientcmd.NewDefaultClientConfigLoadingRules()
	}
	overrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		overrides.CurrentContext = cfg.Context
	}

	restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return restCfg, nil
}
