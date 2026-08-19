package kubernetes

import (
	"errors"
	"fmt"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ClientOptions selects how the Kubernetes client authenticates.
type ClientOptions struct {
	// Kubeconfig is an explicit kubeconfig path. It is honoured only when
	// AllowKubeconfig is set.
	Kubeconfig string

	// AllowKubeconfig permits falling back to a local kubeconfig when there is
	// no in-cluster service account.
	//
	// This is off by default on purpose. A developer running the portal on
	// their laptop with an admin kubeconfig would otherwise write real Secrets
	// into whatever cluster their current context points at.
	AllowKubeconfig bool
}

// RESTConfig resolves the Kubernetes client configuration.
//
// In-cluster configuration is always preferred. Falling back to a kubeconfig
// requires an explicit opt-in, so the failure mode for a misconfigured
// deployment is a refusal to start rather than silently talking to the wrong
// cluster.
//
// It is exported so other clients built against the same cluster — the
// read-only model catalog's dynamic client, for one — resolve their connection
// exactly as the keystore does, from one place, rather than re-implementing the
// in-cluster/kubeconfig decision and drifting from it.
func RESTConfig(opts ClientOptions) (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	switch {
	case err == nil:
		// Running in the cluster: use the mounted ServiceAccount.
	case errors.Is(err, rest.ErrNotInCluster):
		if !opts.AllowKubeconfig {
			return nil, errors.New(
				"not running in a cluster and KUBERNETES_ALLOW_KUBECONFIG is not set; " +
					"refusing to guess which cluster to talk to")
		}
		cfg, err = kubeconfigREST(opts.Kubeconfig)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}

	// A portal request should fail fast rather than hang on a slow API server.
	cfg.UserAgent = "ai-account"
	cfg.QPS = 20
	cfg.Burst = 40
	return cfg, nil
}

// NewClient builds a Kubernetes clientset.
func NewClient(opts ClientOptions) (kubernetes.Interface, error) {
	cfg, err := RESTConfig(opts)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	return client, nil
}

func kubeconfigREST(path string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	return cfg, nil
}
