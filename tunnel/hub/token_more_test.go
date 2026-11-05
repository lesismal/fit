package hub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/tunnel/internal/proto"
)

// A registration past the hub's bounds lets go of its token's use.
func TestTokenRegisterPastBounds(t *testing.T) {
	a := linktoken.New()
	v := newHub(t, `{"listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}], "max_peers": 1, "tokens": [`+tokenJSON(a, "a", "")+`]}`)
	if err := v.dial(v.listen()).registerToken(proto.TypeNode, "lab", a); err != nil {
		t.Fatal(err)
	}
	if err := v.dial(v.listen()).registerToken(proto.TypeProxy, "me", a); err == nil || !strings.Contains(err.Error(), "as many peers") {
		t.Fatalf("past max_peers: %v", err)
	}
	if vs := v.s.LinkTokens().List(); len(vs[0].Links) != 1 {
		t.Fatalf("links %+v", vs[0].Links)
	}
	// A peer that names no name is refused before its token is looked at.
	p := v.dial(v.listen())
	if err := p.call(proto.MethodRegister, &proto.RegisterReq{Token: a.ID}, nil); err == nil || !strings.Contains(err.Error(), "needs a name") {
		t.Fatalf("no name: %v", err)
	}
}

// A change of the tokens waits for its apply no longer than its context; a
// binding the config cannot take is logged, not applied.
func TestTokenEdits(t *testing.T) {
	v := newHub(t, tcpMember(""))
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := v.s.editTokens(ctx, func(ts []config.LinkToken) ([]config.LinkToken, error) {
		<-release
		return ts, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("edit: %v", err)
	}
	close(release)
	_ = v.waitEdit()
	if err := v.s.editTokens(context.Background(), func([]config.LinkToken) ([]config.LinkToken, error) {
		return nil, errors.New("boom")
	}); err == nil || err.Error() != "boom" {
		t.Fatalf("a change that fails: %v", err)
	}
	_ = v.waitEdit()
	cur := v.current()
	v.setMember(`{"tokens": 5}`)
	if err := v.s.editTokens(context.Background(), func(ts []config.LinkToken) ([]config.LinkToken, error) { return ts, nil }); err == nil {
		t.Fatal("tokens that do not decode")
	}
	_ = v.waitEdit()
	v.s.bind("0123456789abcdef", "node/x")
	if err := v.waitEdit(); err == nil {
		t.Fatal("a binding into tokens that do not decode")
	}
	v.setMember(cur)
}
