package uplink

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

func tokenUplink(addr, token string) *config.TunnelUplink {
	return &config.TunnelUplink{Name: "srv", Token: token, PoolSize: 1, RetryInterval: config.Duration(20 * time.Millisecond),
		Endpoints: []config.TunnelEndpoint{{Address: "tcp://" + addr}}}
}

// A hub of before tokens is named as such.
func TestTokenOldHub(t *testing.T) {
	s := newFakeHub(t)
	sec := linktoken.New()
	u := newUplink(t, tokenUplink(s.addr, sec.String()))
	if u.Status().TokenID != sec.ID {
		t.Fatalf("status %+v", u.Status())
	}
	u.Start()
	waitFor(t, "the old hub's error", func() bool { return strings.Contains(u.Status().Error, "without link tokens") })
}

// A token that does not parse fails the uplink at once.
func TestTokenBad(t *testing.T) {
	u := newUplink(t, tokenUplink("127.0.0.1:1", "nope"))
	u.Start()
	if st := u.Status(); st.State != StateFailed || !strings.Contains(st.Error, "token") {
		t.Fatalf("status %+v", st)
	}
}

// A hub that answers the challenge but cannot prove the token is not taken,
// nor one whose nonce is not one.
func TestTokenHubProof(t *testing.T) {
	sec := linktoken.New()
	for _, tc := range []struct {
		name  string
		nonce string
		want  string
	}{
		{"wrong proof", hex.EncodeToString(linktoken.Nonce()), "did not prove it issued the token"},
		{"bad nonce", "zz", "nonce"},
		{"refused", "", "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := link.NewRouter()
			r.HandleCall(proto.MethodChallenge, func(*link.Link, []byte) (any, error) {
				if tc.nonce == "" {
					return nil, errors.New("boom")
				}
				return &proto.ChallengeRsp{Nonce: tc.nonce}, nil
			}, true)
			r.HandleCall(proto.MethodRegister, func(*link.Link, []byte) (any, error) {
				return &proto.RegisterRsp{Peer: 1, Conn: 1, Hub: "srv", Proof: "00"}, nil
			}, true)
			l, err := transport.Listen(tunneltest.Engine(t), "127.0.0.1:0", transport.ListenOptions{}, func(c transport.Conn) transport.Handler {
				return link.New(c, r, nil)
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = l.Close() })
			u := newUplink(t, tokenUplink(l.URL(), sec.String()))
			u.Start()
			waitFor(t, "the error", func() bool { return strings.Contains(u.Status().Error, tc.want) })
		})
	}
}

// With a body password, a token's uplink seals with the token's keys.
func TestTokenBodyCoder(t *testing.T) {
	sec := linktoken.New()
	cfg := tokenUplink("127.0.0.1:1", sec.String())
	cfg.BodyPassword = "bp"
	u := newUplink(t, cfg)
	if u.coder == nil || u.coder.Token() != sec.ID {
		t.Fatal("the coder is not the token's")
	}
	other := *cfg
	other.Token = linktoken.New().String()
	if Same(cfg, &other) {
		t.Fatal("uplinks of two tokens are the same")
	}
}
