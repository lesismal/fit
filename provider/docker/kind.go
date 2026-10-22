package docker

import (
	"fmt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
)

func init() {
	provider.RegisterKind("docker", func(name string, pc *config.Provider, logf func(string, ...any)) (provider.Provider, string) {
		network, address := pc.Docker.Dial()
		return New(name, pc.Docker, logf), fmt.Sprintf("watching Docker at %s:%s", network, address)
	})
}
