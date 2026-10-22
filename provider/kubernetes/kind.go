package kubernetes

import (
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
)

func init() {
	provider.RegisterKind("kubernetes", func(name string, pc *config.Provider, logf func(string, ...any)) (provider.Provider, string) {
		return New(name, pc.Kubernetes, logf), "watching Kubernetes for GatewayClasses of " + pc.Kubernetes.Controller()
	})
}
