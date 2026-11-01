package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
)

// A node of the list shows its hardware without disks and addresses, at
// most 16 GPUs and model.MaxNodeListModels models, its links and traffic.
func TestNodeSummary(t *testing.T) {
	info := model.NodeInfo{ID: "n", Name: "nn", Description: "d", Inflight: 2, Jobs: 1,
		HW: model.HWInfo{Arch: "amd64", Disks: []model.DiskInfo{{Mount: "/"}}, IPs: []string{"10.0.0.1"}, GPUs: make([]model.GPUInfo, 20)}}
	for i := range model.MaxNodeListModels + 6 {
		info.Models = append(info.Models, model.ModelView{Name: fmt.Sprint("m", i), API: "openai", State: model.ModelReady})
	}
	hn := model.HubNode{Info: info, Online: true, LastSeen: 7,
		Conns: []model.PeerConn{{Direction: "in", ConnectedAt: 5, Link: model.LinkStats{BytesIn: 3, RTT: model.RTT{Samples: 1}}}}}
	s := summary(hn, model.NodeTraffic{Calls: 4})
	if s.HW.Disks != nil || s.HW.IPs != nil || len(s.HW.GPUs) != maxListGPUs || s.HW.Arch != "amd64" ||
		len(s.Models) != model.MaxNodeListModels || s.ModelsTotal != model.MaxNodeListModels+6 || s.Traffic.Calls != 4 ||
		len(s.Links) != 1 || s.Links[0].Link.BytesIn != 3 || s.Links[0].ConnectedAt != 5 || !s.Online || s.Inflight != 2 || s.Jobs != 1 {
		t.Fatalf("%+v", s)
	}
	if info.HW.Disks == nil || len(info.HW.GPUs) != 20 {
		t.Fatal("the summary changed the node's info")
	}
	if s := summary(model.HubNode{}, model.NodeTraffic{}); s.Models == nil {
		t.Fatal("no models is an empty list")
	}
}

// A list past model.MaxNodeListBytes leaves out the nodes at its end.
func TestEncodeListBound(t *testing.T) {
	desc := strings.Repeat("d", 1024)
	nodes := make([]model.NodeSummary, 6000)
	for i := range nodes {
		nodes[i] = model.NodeSummary{ID: fmt.Sprint("n", i), Description: desc}
	}
	kept, enc := encodeList(nodes)
	if len(enc) > model.MaxNodeListBytes || len(kept) == 0 || len(kept) >= len(nodes) || kept[0].ID != "n0" {
		t.Fatalf("%d nodes kept, %d bytes", len(kept), len(enc))
	}
	var back []model.NodeSummary
	if err := json.Unmarshal(enc, &back); err != nil || len(back) != len(kept) {
		t.Fatalf("%d %v", len(back), err)
	}
	if kept, enc := encodeList(nodes[:3]); len(kept) != 3 || len(enc) == 0 {
		t.Fatal("a small list is kept whole")
	}
}

// The list changes its Seq only when it changed, the pushes keep their
// pace, and none is scheduled after close.
func TestNodeListSeqAndPace(t *testing.T) {
	h := newHub(newMesh(t, model.RoleHub, "h1", 1, nil, mesh.NewRouter()), func() model.Identity { return model.Identity{ID: "h1"} }, nil)
	a := h.NodeList()
	if b := h.NodeList(); b.Seq != a.Seq || b.Hub != "h1" || b.Total != 0 || b.Nodes == nil {
		t.Fatalf("unchanged: %+v %+v", a, b)
	}
	h.apply(model.Identity{ID: "n1", Name: "one", StartedAt: 1}, model.NodeInfo{Seq: 1})
	c := h.NodeList()
	if c.Seq <= a.Seq || c.Total != 1 || len(c.Nodes) != 1 || c.Nodes[0].Name != "one" || c.Nodes[0].Online {
		t.Fatalf("changed: %+v", c)
	}
	// A push just went out: the next waits for listGap.
	h.mu.Lock()
	if h.listTimer != nil {
		h.listTimer.Stop()
		h.listTimer = nil
	}
	h.listAt = time.Now()
	h.mu.Unlock()
	h.listChanged()
	h.mu.Lock()
	pending := h.listTimer != nil
	h.mu.Unlock()
	if !pending {
		t.Fatal("no push scheduled")
	}
	h.close()
	h.mu.Lock()
	h.listTimer = nil
	h.mu.Unlock()
	h.listChanged()
	h.refresh()
	h.mu.Lock()
	pending = h.listTimer != nil
	h.mu.Unlock()
	if pending {
		t.Fatal("a push was scheduled after close")
	}
}
