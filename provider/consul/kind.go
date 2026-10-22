package consul

import (
	"fmt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
)

func init() {
	provider.RegisterKind("consul", func(name string, pc *config.Provider, logf func(string, ...any)) (provider.Provider, string) {
		return NewCatalog(name, pc.Consul, logf), "watching Consul's catalog at " + pc.Consul.URL()
	})
	provider.RegisterKind("consul_kv", func(name string, pc *config.Provider, logf func(string, ...any)) (provider.Provider, string) {
		return NewKV(name, pc.ConsulKV, logf), fmt.Sprintf("watching Consul's keys under %q at %s", pc.ConsulKV.KeyPrefix(), pc.ConsulKV.URL())
	})
}
