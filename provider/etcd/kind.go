package etcd

import (
	"fmt"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider"
)

func init() {
	provider.RegisterKind("etcd", func(name string, pc *config.Provider, log *logs.Logger) (provider.Provider, string) {
		return New(name, pc.Etcd, log), fmt.Sprintf("watching etcd's keys under %q at %s", pc.Etcd.KeyPrefix(), strings.Join(pc.Etcd.URLs(), ", "))
	})
}
