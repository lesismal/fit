package kubernetes

import (
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider"
)

func init() {
	provider.RegisterKind("kubernetes", func(name string, pc *config.Provider, log *logs.Logger) (provider.Provider, string) {
		return New(name, pc.Kubernetes, log), "watching Kubernetes for GatewayClasses of " + pc.Kubernetes.Controller()
	})
}
