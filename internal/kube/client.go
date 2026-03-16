package kube

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Config struct {
	Kubeconfig string
	Context    string
}

func NewClient(cfg Config) (*kubernetes.Clientset, error) {
	restCfg, err := loadRESTConfig(cfg)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restCfg)
}

func loadRESTConfig(cfg Config) (*rest.Config, error) {
	if cfg.Kubeconfig == "" {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, nil
		}

		home, _ := os.UserHomeDir()
		candidate := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(candidate); err == nil {
			cfg.Kubeconfig = candidate
		}
	}

	if cfg.Kubeconfig == "" {
		return nil, fmt.Errorf("no kubeconfig provided and in-cluster config not available")
	}

	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: cfg.Kubeconfig}
	overrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		overrides.CurrentContext = cfg.Context
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides).ClientConfig()
}
