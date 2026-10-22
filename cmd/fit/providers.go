package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
	"github.com/lesismal/fit/provider/consul"
	"github.com/lesismal/fit/provider/docker"
	"github.com/lesismal/fit/provider/etcd"
	"github.com/lesismal/fit/provider/kubernetes"
)

// startProviders runs the providers c lists, in their order of precedence,
// applying what they provide through configs, until the function it
// returns is called; that waits for them to stop. A part refused is
// reported once, until the provider's part applies again.
func startProviders(c *config.Config, configs *admin.Configs, stdout, stderr io.Writer) (stop func(), err error) {
	var ps []provider.Provider
	for _, pc := range c.Providers {
		name := pc.ID()
		logf := func(format string, args ...any) { fmt.Fprintf(stderr, "fit: "+format+"\n", args...) }
		switch {
		case pc.Docker != nil:
			ps = append(ps, docker.New(name, pc.Docker, logf))
			network, address := pc.Docker.Dial()
			fmt.Fprintf(stdout, "fit: provider %s watching Docker at %s:%s\n", name, network, address)
		case pc.Consul != nil:
			ps = append(ps, consul.NewCatalog(name, pc.Consul, logf))
			fmt.Fprintf(stdout, "fit: provider %s watching Consul's catalog at %s\n", name, pc.Consul.URL())
		case pc.ConsulKV != nil:
			ps = append(ps, consul.NewKV(name, pc.ConsulKV, logf))
			fmt.Fprintf(stdout, "fit: provider %s watching Consul's keys under %q at %s\n", name, pc.ConsulKV.KeyPrefix(), pc.ConsulKV.URL())
		case pc.Etcd != nil:
			ps = append(ps, etcd.New(name, pc.Etcd, logf))
			fmt.Fprintf(stdout, "fit: provider %s watching etcd's keys under %q at %s\n", name, pc.Etcd.KeyPrefix(), strings.Join(pc.Etcd.URLs(), ", "))
		case pc.Kubernetes != nil:
			ps = append(ps, kubernetes.New(name, pc.Kubernetes, logf))
			fmt.Fprintf(stdout, "fit: provider %s watching Kubernetes for GatewayClasses of %s\n", name, pc.Kubernetes.Controller())
		default:
			return nil, fmt.Errorf("provider %s: no kind is set", name)
		}
		if err := configs.AddProvider(name); err != nil {
			return nil, err
		}
	}
	if len(ps) == 0 {
		return func() {}, nil
	}
	refused := map[string]string{} // the error last reported for each provider; used by Run's one goroutine
	apply := func(name string, data []byte) error {
		before := configs.Current().Number
		r, err := configs.Provide(name, data)
		if err != nil {
			return err
		}
		delete(refused, name)
		if r.Version != before {
			printApplied(stdout, stderr, r)
			fmt.Fprintf(stdout, "fit: applied what provider %s provides (config version %d)\n", name, r.Version)
		}
		return nil
	}
	report := func(name string, err error) {
		if refused[name] == err.Error() {
			return
		}
		refused[name] = err.Error()
		fmt.Fprintf(stderr, "fit: what provider %s provides was refused; the running config stays\n", name)
		printConfigError(stderr, "fit run", "provider "+name, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		provider.Run(ctx, ps, apply, report)
	}()
	return func() {
		cancel()
		<-done
	}, nil
}
