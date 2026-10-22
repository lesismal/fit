// Package standard imports every provider fit ships with, so that importing
// it registers them all.
package standard

import (
	_ "github.com/lesismal/fit/provider/consul"
	_ "github.com/lesismal/fit/provider/docker"
	_ "github.com/lesismal/fit/provider/etcd"
	_ "github.com/lesismal/fit/provider/kubernetes"
)
