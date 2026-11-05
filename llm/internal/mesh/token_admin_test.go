package mesh

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/llm/internal/model"
)

// The pages' dial takes a token in place of a password, either replacing the
// other, and refuses a token that is not one; the listener's edit can clear
// every password.
// 页面的拨号接受令牌代替密码，二者互相替换，并拒绝格式不对的令牌；修改监听可以清除全部密码。
func TestAdminTokens(t *testing.T) {
	node, r := newTestMesh(t, model.RoleNode, "n1", []string{model.RoleHub, model.RoleProxy})
	var cur Settings
	RegisterAdmin(r, node, func(_ context.Context, fn func(*Settings) error) error {
		next := Settings{Peers: append([]model.DialReq(nil), cur.Peers...)}
		if cur.Listen != nil {
			l := *cur.Listen
			next.Listen = &l
		}
		if err := fn(&next); err != nil {
			return err
		}
		cur = next
		return nil
	})
	invoke := func(method string, req any) int {
		t.Helper()
		raw, _ := json.Marshal(req)
		if _, err := r.Invoke(context.Background(), model.Identity{Role: model.RoleLocal}, method, raw); err != nil {
			code, _ := ErrCode(err)
			return code
		}
		return model.CodeOK
	}
	tok := linktoken.New().String()
	if c := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:9", Token: "nope", Role: model.RoleHub}); c != model.CodeBadRequest {
		t.Fatalf("a bad token: %d", c)
	}
	if c := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:9", Token: tok, Password: "pw", Role: model.RoleHub}); c != model.CodeOK {
		t.Fatalf("a token: %d", c)
	}
	if p := cur.Peers[0]; p.Token != tok || p.Password != "" {
		t.Fatalf("peer %+v", p)
	}
	if c := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:9", Password: "pw", Role: model.RoleHub}); c != model.CodeOK {
		t.Fatalf("a password: %d", c)
	}
	if p := cur.Peers[0]; p.Token != "" || p.Password != "pw" {
		t.Fatalf("peer %+v", p)
	}
	if c := invoke(model.MethodDial, model.DialReq{Addr: "127.0.0.1:9", Token: tok, Role: model.RoleHub}); c != model.CodeOK || cur.Peers[0].Token != tok {
		t.Fatalf("a token again: %d %+v", c, cur.Peers[0])
	}
	if c := invoke(model.MethodListen, model.ListenReq{Addr: "127.0.0.1:0", Password: "a", NodePassword: "b"}); c != model.CodeOK {
		t.Fatalf("listen: %d", c)
	}
	if c := invoke(model.MethodListen, model.ListenReq{Addr: "127.0.0.1:0", ClearPasswords: true}); c != model.CodeOK {
		t.Fatalf("clear: %d", c)
	}
	if l := cur.Listen; l.HasPassword() || l.ClearPasswords {
		t.Fatalf("listen %+v", l)
	}
}
