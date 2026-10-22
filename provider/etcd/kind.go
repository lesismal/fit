package etcd

import (
	"fmt"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
)

func init() {
	provider.RegisterKind("etcd", func(name string, pc *config.Provider, logf func(string, ...any)) (provider.Provider, string) {
		return New(name, pc.Etcd, logf), fmt.Sprintf("watching etcd's keys under %q at %s", pc.Etcd.KeyPrefix(), strings.Join(pc.Etcd.URLs(), ", "))
	})
}
