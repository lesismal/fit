package node

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/lesismal/fit/agent/internal/model"
)

// pushDelay coalesces the changes of NodeInfo into one push.
// pushDelay 把 NodeInfo 的多次变化合并为一次推送。
var pushDelay = 200 * time.Millisecond

// pusher sends MethodNodeUpdate to the connected hubs and clients when NodeInfo changed (ignoring Seq and
// UpdatedAt).
// pusher 在 NodeInfo 变化（忽略 Seq 与 UpdatedAt）时向已连接的 hub 与 client 发送 MethodNodeUpdate。
type pusher struct {
	n     *Node
	dirty chan struct{}

	mu   sync.Mutex
	seq  int64
	last []byte // the last pushed snapshot / 最近一次推送的快照
}

func newPusher(n *Node) *pusher {
	return &pusher{n: n, dirty: make(chan struct{}, 1), seq: time.Now().UnixMilli()}
}

// nextSeq starts from the start time, so a restarted node's infos are newer than those of its previous run.
// nextSeq 从启动时间开始，因此重启后的节点信息比上次运行的新。
func (p *pusher) nextSeq() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	return p.seq
}

func (p *pusher) mark() {
	select {
	case p.dirty <- struct{}{}:
	default:
	}
}

func (p *pusher) loop() {
	defer p.n.wg.Done()
	for {
		select {
		case <-p.n.stopCh:
			return
		case <-p.dirty:
		}
		select {
		case <-p.n.stopCh:
			return
		case <-time.After(pushDelay):
		}
		p.flush()
	}
}

// flush pushes the info to every hub and client if it changed since the last push.
// flush 若信息自上次推送后有变化，则推送给每个 hub 与 client。
func (p *pusher) flush() {
	snap := p.n.snapshot()
	b, _ := json.Marshal(snap)
	p.mu.Lock()
	same := string(b) == string(p.last)
	p.last = b
	p.mu.Unlock()
	if same {
		return
	}
	m := p.n.Mesh
	info := snap
	info.Seq, info.UpdatedAt = p.nextSeq(), time.Now().Unix()
	for _, role := range []string{model.RoleHub, model.RoleClient} {
		for _, id := range m.PeersOf(role) {
			_ = m.Push(id.ID, model.MethodNodeUpdate, info)
		}
	}
}

// sendTo pushes the current info to one peer (it just connected).
// sendTo 把当前信息推送给一个对端（刚连接）。
func (p *pusher) sendTo(id string) {
	_ = p.n.Mesh.Push(id, model.MethodNodeUpdate, p.n.Info())
}
