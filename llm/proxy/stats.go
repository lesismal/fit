package client

import (
	"sort"
	"sync"
	"time"
)

// ModelStats are the in-memory counters of one model (since the process started).
// ModelStats 一个模型的内存计数（自进程启动起）。
type ModelStats struct {
	Model    string `json:"model"`
	Calls    int64  `json:"calls"`
	Errors   int64  `json:"errors"`
	Bytes    int64  `json:"bytes"` // response bytes relayed / 已转发的响应字节数
	Inflight int64  `json:"inflight"`
	LastAt   int64  `json:"last_at,omitempty"`
}

// StatsResp is the reply of MethodClientStats.
// StatsResp MethodClientStats 的应答。
type StatsResp struct {
	Since  int64        `json:"since"`
	Total  ModelStats   `json:"total"`
	Models []ModelStats `json:"models"`
}

type stats struct {
	mu     sync.Mutex
	since  int64
	models map[string]*ModelStats
}

func newStats() *stats { return &stats{since: time.Now().Unix(), models: map[string]*ModelStats{}} }

// maxStatModels bounds the models counted; calls to more are counted
// under "other".
const maxStatModels = 1024

func (s *stats) get(name string) *ModelStats {
	m := s.models[name]
	if m == nil && len(s.models) >= maxStatModels {
		name = "other"
		m = s.models[name]
	}
	if m == nil {
		m = &ModelStats{Model: name}
		s.models[name] = m
	}
	return m
}

// begin counts a call to name; the returned function ends it with the bytes relayed and whether it failed.
// begin 记录一次对 name 的调用；返回的函数以转发字节数与是否失败结束它。
func (s *stats) begin(name string) func(bytes int64, failed bool) {
	s.mu.Lock()
	m := s.get(name)
	m.Calls++
	m.Inflight++
	m.LastAt = time.Now().Unix()
	s.mu.Unlock()
	return func(bytes int64, failed bool) {
		s.mu.Lock()
		m.Inflight--
		m.Bytes += bytes
		if failed {
			m.Errors++
		}
		s.mu.Unlock()
	}
}

func (s *stats) snapshot() StatsResp {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := StatsResp{Since: s.since, Models: make([]ModelStats, 0, len(s.models))}
	for _, m := range s.models {
		out.Models = append(out.Models, *m)
		out.Total.Calls += m.Calls
		out.Total.Errors += m.Errors
		out.Total.Bytes += m.Bytes
		out.Total.Inflight += m.Inflight
		out.Total.LastAt = max(out.Total.LastAt, m.LastAt)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Model < out.Models[j].Model })
	return out
}
