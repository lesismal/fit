package docker

import (
	"fmt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider"
)

func init() {
	provider.RegisterKind("docker", func(name string, pc *config.Provider, log *logs.Logger) (provider.Provider, string) {
		network, address := pc.Docker.Dial()
		return New(name, pc.Docker, log), fmt.Sprintf("watching Docker at %s:%s", network, address)
	})
}
