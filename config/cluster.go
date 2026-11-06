package config

import "time"

// The defaults and bounds of Cluster.
const (
	DefaultClusterPollInterval     = Duration(10 * time.Second)
	DefaultClusterTimeout          = Duration(10 * time.Second)
	DefaultClusterMaxNodes         = 256
	DefaultClusterMaxConcurrency   = 8
	DefaultClusterMaxResponseBytes = 4 << 20
	// MinClusterPollInterval and MinClusterTimeout keep a manager from
	// polling its members as fast as it can.
	MinClusterPollInterval = Duration(time.Second)
	MinClusterTimeout      = Duration(time.Second)
	// MinClusterResponseBytes is the least max_response_bytes: enough for
	// GET /node and a small /status.
	MinClusterResponseBytes = 16 << 10
)

// Cluster turns this fit into a manager of other fits, its members: it
// keeps their addresses and credentials in admin.store, polls each one's
// state, and serves them to the workbench as the console module cluster.
// See docs/decisions/033-cluster-management.md.
type Cluster struct {
	PollInterval     Duration `json:"poll_interval,omitempty" default:"\"10s\"" doc:"How often each member's GET /node, /status and /config/check are read. 10s when left out; 1s at least."`
	Timeout          Duration `json:"timeout,omitempty" default:"\"10s\"" doc:"The longest one call to a member may take, from its connect to the last byte of its answer; the connect and the wait for the answer's header are bounded by it too. 10s when left out; 1s at least."`
	MaxNodes         int      `json:"max_nodes,omitempty" min:"0" max:"10000" default:"256" doc:"The most members the manager keeps; one more is refused. 256 when left out or 0."`
	MaxConcurrency   int      `json:"max_concurrency,omitempty" min:"0" max:"256" default:"8" doc:"The most members polled at once; the others wait for their turn. Tests from the pages run beside them, 4 at most. 8 when left out or 0."`
	MaxResponseBytes int64    `json:"max_response_bytes,omitempty" min:"0" max:"67108864" default:"4194304" doc:"The largest answer read from a member; a larger one fails its call, and is reported as too large. 4 MiB when left out or 0; 16 KiB at least."`

	At Value `json:"-" fit:"self"`
}

// Interval, CallTimeout, Nodes, Concurrency and ResponseBytes return the
// settings, their defaults when left out.
func (c *Cluster) Interval() time.Duration {
	return time.Duration(orDefault(int64(c.PollInterval), int64(DefaultClusterPollInterval)))
}
func (c *Cluster) CallTimeout() time.Duration {
	return time.Duration(orDefault(int64(c.Timeout), int64(DefaultClusterTimeout)))
}
func (c *Cluster) Nodes() int { return int(orDefault(int64(c.MaxNodes), DefaultClusterMaxNodes)) }
func (c *Cluster) Concurrency() int {
	return int(orDefault(int64(c.MaxConcurrency), DefaultClusterMaxConcurrency))
}
func (c *Cluster) ResponseBytes() int64 {
	return orDefault(c.MaxResponseBytes, DefaultClusterMaxResponseBytes)
}

// checkCluster checks admin.cluster: it keeps its members' credentials in
// the store, and serves them in the console.
func checkCluster(a *Admin, add func(Value, string, ...any)) {
	c := a.Cluster
	if c == nil {
		return
	}
	if a.Store == nil {
		add(c.At, "a manager keeps its members' credentials in the store: set admin.store too")
	}
	if a.Console == nil {
		add(c.At, "a manager's members are served in the console's workbench: set admin.console too")
	}
	if c.PollInterval != 0 && c.PollInterval < MinClusterPollInterval {
		add(c.At.Field("poll_interval"), "%s is below %s", c.PollInterval, MinClusterPollInterval)
	}
	if c.Timeout != 0 && c.Timeout < MinClusterTimeout {
		add(c.At.Field("timeout"), "%s is below %s", c.Timeout, MinClusterTimeout)
	}
	if c.MaxResponseBytes != 0 && c.MaxResponseBytes < MinClusterResponseBytes {
		add(c.At.Field("max_response_bytes"), "%d is below %d", c.MaxResponseBytes, MinClusterResponseBytes)
	}
}
