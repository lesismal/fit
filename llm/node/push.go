package node

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
)

// pushDelay coalesces the changes of NodeInfo into one push.
// pushDelay 把 NodeInfo 的多次变化合并为一次推送。
var pushDelay = 200 * time.Millisecond

// pusher sends MethodNodeUpdate to the connected hubs and proxies when NodeInfo changed (ignoring Seq and
// UpdatedAt).
// pusher 在 NodeInfo 变化（忽略 Seq 与 UpdatedAt）时向已连接的 hub 与 proxy 发送 MethodNodeUpdate。
type pusher struct {
	n     *Node
	dirty chan struct{}

	// mu orders the infos: a snapshot and its Seq are taken under it, so a later Seq never carries an older
	// state, and a hub keeping the highest Seq keeps the newest info.
	// mu 为信息定序：快照与其 Seq 在其下一起取得，因此更大的 Seq 不会带着更旧的状态，保留最大 Seq 的 hub 即保留最新信息。
	mu   sync.Mutex
	seq  int64
	last []byte // the last pushed snapshot / 最近一次推送的快照

	// snapped, when set (tests), runs between taking a snapshot and its Seq.
	// snapped 若设置（测试），在取得快照与其 Seq 之间执行。
	snapped func()
}

func newPusher(n *Node) *pusher {
	return &pusher{n: n, dirty: make(chan struct{}, 1), seq: time.Now().UnixMilli()}
}

// nextSeqLocked starts from the start time, so a restarted node's infos are newer than those of its previous
// run. p.mu is held.
// nextSeqLocked 从启动时间开始，因此重启后的节点信息比上次运行的新。调用时持有 p.mu。
func (p *pusher) nextSeqLocked() int64 {
	p.seq++
	return p.seq
}

// snapLocked takes the snapshot that the next Seq goes with. p.mu is held.
// snapLocked 取得与下一个 Seq 配对的快照。调用时持有 p.mu。
func (p *pusher) snapLocked() model.NodeInfo {
	snap := p.n.snapshot()
	if p.snapped != nil {
		p.snapped()
	}
	return snap
}

// info returns the current NodeInfo with a new Seq.
// info 返回带新 Seq 的当前 NodeInfo。
func (p *pusher) info() model.NodeInfo {
	p.mu.Lock()
	info := p.snapLocked()
	info.Seq = p.nextSeqLocked()
	p.mu.Unlock()
	info.UpdatedAt = time.Now().Unix()
	return info
}

// next returns the info to push, and false when it is the same as the last one pushed.
// next 返回要推送的信息；与上次推送的相同时返回 false。
func (p *pusher) next() (model.NodeInfo, bool) {
	p.mu.Lock()
	info := p.snapLocked()
	b, _ := json.Marshal(info)
	if string(b) == string(p.last) {
		p.mu.Unlock()
		return info, false
	}
	p.last = b
	info.Seq = p.nextSeqLocked()
	p.mu.Unlock()
	info.UpdatedAt = time.Now().Unix()
	return info, true
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

// flush pushes the info to every hub and proxy if it changed since the last push.
// flush 若信息自上次推送后有变化，则推送给每个 hub 与 proxy。
func (p *pusher) flush() {
	info, ok := p.next()
	if !ok {
		return
	}
	m := p.n.Mesh
	for _, role := range []string{model.RoleHub, model.RoleProxy} {
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
