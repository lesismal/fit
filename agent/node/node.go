// Package node is the agent's node role, agent.node in fit's config: it
// reports its system and GPUs, runs fixed management actions and GPU
// settings as jobs, and serves its models, upstream API endpoints, to the
// hubs and clients it links with, forwarding each call byte for byte on
// fib's asynchronous HTTP client. See docs/agent.md.
//
// Its models, settings, name and description live in its member of the
// config: what its pages change, and what a hub changes when the node lets
// it, is written back there, and the node follows the config.
package node

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/pool"
	"github.com/lesismal/fit/agent/internal/role"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

//go:embed ui
var uiFS embed.FS

func init() {
	app.Register(app.Info{
		ID: "agent.node",
		Member: func(c *config.Config) any {
			if c.Agent == nil || c.Agent.Node == nil {
				return nil
			}
			return c.Agent.Node
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
	})
}

// The pools of a node: work runs its hardware and GPU queries, jobs its
// jobs, at most maxRunning at once.
const (
	workWorkers = 2
	workQueue   = 16
)

// settings are a node's settings, defaults filled in.
type settings struct {
	AllowHubManage  bool
	GPUSudo         bool
	RefreshInterval time.Duration
	JobTimeout      time.Duration
	GPUDataDir      string
}

func settingsOf(s *config.AgentNodeSettings) settings {
	out := settings{AllowHubManage: s != nil && s.AllowHubManage, GPUSudo: s.Sudo(), RefreshInterval: s.Refresh(), JobTimeout: s.Job()}
	if s != nil {
		out.GPUDataDir = s.GPUDataDir
	}
	return out
}

// limits bound the calls a node forwards.
type limits struct {
	connect, firstByte, idle, total time.Duration
	inflight                        int
	body                            int64
}

// Node is a running node.
type Node struct {
	*role.Base
	log  *logs.Logger
	work *pool.Pool
	jobs *Jobs

	client *fibhttp.Client // the calls, their bodies streamed
	probe  *fibhttp.Client // the checks, small and whole

	mu          sync.Mutex
	models      []model.Model
	set         settings
	lim         limits
	description string
	rt          map[string]*modelRT // runtime state per model key
	total       int                 // proxy calls in flight
	hw          model.HWInfo

	gpuMu  sync.Mutex // serializes gpu.Collect
	gpu    *gpuCache
	push   *pusher
	stopCh chan struct{}
	wg     sync.WaitGroup
	start  sync.Once
	once   sync.Once
}

// New returns a node on h, started by its first Apply.
func New(h *app.Host) *Node {
	pages, _ := fs.Sub(uiFS, "ui")
	b := role.New(h, role.Spec{Role: model.RoleNode, Pages: pages})
	n := &Node{Base: b, log: b.Log, work: pool.New(workWorkers, workQueue), rt: map[string]*modelRT{},
		gpu: &gpuCache{}, stopCh: make(chan struct{}), set: settingsOf(nil), lim: limitsOf(nil)}
	n.jobs = NewJobs(pool.New(maxRunning, maxRunning))
	n.client, n.probe = newClients(h, func() time.Duration {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.lim.connect
	})
	n.push = newPusher(n)
	b.Register(n)
	n.jobs.OnChange = func() {
		n.gpu.invalidate()
		n.changed()
	}
	n.register()
	b.Mesh.OnPeerUp(func(p model.PeerConn) {
		if p.Role != model.RoleHub && p.Role != model.RoleClient {
			return
		}
		n.push.sendTo(p.ID)
	})
	return n
}

func limitsOf(p *config.AgentNodeProxy) limits {
	return limits{connect: p.Connect(), firstByte: p.FirstByte(), idle: p.Idle(), total: p.Total(), inflight: p.Inflight(), body: p.Body()}
}

// Apply moves the node to member, a *config.AgentNode: its models, settings
// and profile, and the base's identity, mesh and pages. A model whose
// endpoint, key or upstream ID changed loses its last check; calls in
// flight go on with the model they started with.
func (n *Node) Apply(member any) error {
	m, ok := member.(*config.AgentNode)
	if !ok || m == nil {
		return errors.New("agent.node: no member")
	}
	models := make([]model.Model, 0, len(m.Models))
	for _, c := range m.Models {
		models = append(models, model.Model{Name: c.Name, API: c.API, BaseURL: c.BaseURL, Upstream: c.Upstream, APIKey: c.APIKey,
			Disabled: c.Disabled, Description: c.Description, MaxInflight: c.MaxInflight})
	}
	n.mu.Lock()
	old := n.models
	for _, nm := range models {
		k := key(nm.Name, nm.API)
		i := slices.IndexFunc(old, func(o model.Model) bool { return o.Name == nm.Name && o.API == nm.API })
		r := n.rt[k]
		switch {
		case r == nil:
			n.rt[k] = &modelRT{}
		case i >= 0 && (old[i].BaseURL != nm.BaseURL || old[i].APIKey != nm.APIKey || old[i].Upstream != nm.Upstream):
			r.err, r.checkedAt = "", 0
		}
	}
	for k, r := range n.rt {
		if r.inflight == 0 && !slices.ContainsFunc(models, func(x model.Model) bool { return key(x.Name, x.API) == k }) {
			delete(n.rt, k)
		}
	}
	was := n.set.AllowHubManage
	n.models, n.set, n.lim, n.description = models, settingsOf(m.Settings), limitsOf(m.Proxy), m.Description
	now := n.set.AllowHubManage
	n.mu.Unlock()
	if was != now {
		n.log.Infof("hub management is now %v", now)
	}
	err := n.Base.Apply(role.Common{ID: m.ID, Name: m.Name, Console: m.Console, Listen: m.Listen, Peers: m.Peers, Limits: m.Limits})
	n.start.Do(func() {
		n.wg.Add(3)
		go n.hwLoop()
		go n.checkLoop()
		go n.push.loop()
	})
	n.changed()
	return err
}

// Stop ends the node: its jobs are cancelled, its links drain until ctx
// ends, for 10s at most, and its clients are closed.
func (n *Node) Stop(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n.once.Do(func() {
		close(n.stopCh)
		for _, j := range n.jobs.List() {
			if j.Status == model.JobRunning {
				n.jobs.Cancel(j.ID)
			}
		}
	})
	n.Base.Stop(ctx)
	n.wg.Wait()
	n.client.Close()
	n.probe.Close()
	n.work.Stop(ctx)
	n.jobs.pool.Stop(ctx)
}

func (n *Node) settings() settings {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.set
}

func (n *Node) jobTimeout() time.Duration { return n.settings().JobTimeout }

// changed marks the node info as changed; a push follows (coalesced, only
// when the content differs).
func (n *Node) changed() { n.push.mark() }

// Info returns the current NodeInfo with a new Seq.
func (n *Node) Info() model.NodeInfo {
	info := n.snapshot()
	info.Seq = n.push.nextSeq()
	info.UpdatedAt = time.Now().Unix()
	return info
}

// snapshot is NodeInfo without Seq and UpdatedAt.
func (n *Node) snapshot() model.NodeInfo {
	self := n.Self()
	n.mu.Lock()
	info := model.NodeInfo{ID: self.ID, Name: self.Name, Description: n.description, HW: n.hw,
		Models: n.viewsLocked(), Inflight: n.total, HubManage: n.set.AllowHubManage}
	n.mu.Unlock()
	info.Jobs = n.jobs.Running()
	return info
}

// ---- hardware ----

// refreshHW reads the hardware on the work pool, for 30s at most.
func (n *Node) refreshHW() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hw, err := pool.Do(ctx, n.work, func(context.Context) (model.HWInfo, error) { return CollectHW(), nil })
	if err != nil {
		return
	}
	n.mu.Lock()
	n.hw = hw
	n.mu.Unlock()
	n.changed()
}

func (n *Node) hwLoop() {
	defer n.wg.Done()
	n.refreshHW()
	for {
		t := time.NewTimer(n.settings().RefreshInterval)
		select {
		case <-n.stopCh:
			t.Stop()
			return
		case <-t.C:
			n.refreshHW()
		}
	}
}

// ---- profile and settings ----

func (n *Node) setProfile(ctx context.Context, caller model.Identity, req *model.ProfileReq) (any, error) {
	name, desc := strings.TrimSpace(req.Name), strings.TrimSpace(req.Description)
	if name == "" || len([]rune(name)) > 64 {
		return nil, mesh.BadRequest("the name needs 1-64 characters")
	}
	if len(desc) > 1024 {
		return nil, mesh.BadRequest("the description is longer than 1024 bytes")
	}
	err := n.Edit(ctx, func(m map[string]json.RawMessage) error {
		if err := role.Set(m, "name", name); err != nil {
			return err
		}
		return role.Set(m, "description", desc)
	})
	if err != nil {
		return nil, err
	}
	n.log.Infof("profile set by %s", caller.Role)
	return struct{}{}, nil
}

func (n *Node) getSettings() model.NodeSettings {
	s := n.settings()
	return model.NodeSettings{AllowHubManage: s.AllowHubManage, GPUSudo: s.GPUSudo, RefreshInterval: s.RefreshInterval.String(),
		JobTimeout: s.JobTimeout.String(), GPUDataDir: s.GPUDataDir}
}

func (n *Node) setSettings(ctx context.Context, _ model.Identity, req *model.NodeSettings) (any, error) {
	parse := func(name, v string, lo, hi time.Duration) (string, error) {
		if v == "" {
			return "", nil
		}
		d, err := time.ParseDuration(v)
		if err != nil || d < lo || d > hi {
			return "", mesh.BadRequest("%s must be a duration from %s to %s", name, lo, hi)
		}
		return d.String(), nil
	}
	refresh, err := parse("refresh_interval", req.RefreshInterval, 5*time.Second, time.Hour)
	if err != nil {
		return nil, err
	}
	job, err := parse("job_timeout", req.JobTimeout, 10*time.Second, 7*24*time.Hour)
	if err != nil {
		return nil, err
	}
	if len(req.GPUDataDir) > 4096 {
		return nil, mesh.BadRequest("gpu_data_dir is too long")
	}
	s := map[string]any{"allow_hub_manage": req.AllowHubManage}
	if !req.GPUSudo {
		s["gpu_sudo"] = false
	}
	if refresh != "" && refresh != config.DefaultAgentRefreshInterval.String() {
		s["refresh_interval"] = refresh
	}
	if job != "" && job != config.DefaultAgentJobTimeout.String() {
		s["job_timeout"] = job
	}
	if req.GPUDataDir != "" {
		s["gpu_data_dir"] = req.GPUDataDir
	}
	if !req.AllowHubManage {
		delete(s, "allow_hub_manage")
	}
	if err := n.Edit(ctx, func(m map[string]json.RawMessage) error { return role.Set(m, "settings", s) }); err != nil {
		return nil, err
	}
	return n.getSettings(), nil
}
