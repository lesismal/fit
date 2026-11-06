package role

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
)

// The methods of the listener's link tokens, for the role's pages only.
const (
	MethodTokensList   = "/mesh/tokens/list"
	MethodTokensCreate = "/mesh/tokens/create"
	MethodTokensExpiry = "/mesh/tokens/expiry"
	MethodTokensRevoke = "/mesh/tokens/revoke"
)

// TokenIDReq names a token, with its new expiry for MethodTokensExpiry.
type TokenIDReq struct {
	ID string `json:"id"`
	linktoken.ExpiryReq
}

// LinkTokens is the role's link tokens, for its pages, the Admin API and fit
// token: an app.TokenIssuer.
func (b *Base) LinkTokens() *linktoken.Manager { return b.tokens }

// setupTokens makes the role's token manager and registers its methods.
func (b *Base) setupTokens() {
	b.Mesh.Tokens().OnBind = b.bind
	b.tokens = &linktoken.Manager{Role: "llm." + b.Role, Reg: b.Mesh.Tokens(), Edit: b.editTokens, Log: b.H.Admin}
	// tokenErr is a manager's error as the pages get it.
	tokenErr := func(err error) error {
		switch {
		case errors.Is(err, linktoken.ErrNotFound):
			return mesh.Errorf(model.CodeNotFound, "%v", err)
		case errors.Is(err, linktoken.ErrBadRequest):
			return mesh.BadRequest("%v", err)
		}
		return err
	}
	mesh.Handle(b.Router, MethodTokensList, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return map[string]any{"tokens": b.tokens.List()}, nil
	})
	mesh.Handle(b.Router, MethodTokensCreate, nil, EditTimeout, func(ctx context.Context, _ model.Identity, req *linktoken.IssueReq) (any, error) {
		is, err := b.tokens.Issue(ctx, *req)
		return is, tokenErr(err)
	})
	mesh.Handle(b.Router, MethodTokensExpiry, nil, EditTimeout, func(ctx context.Context, _ model.Identity, req *TokenIDReq) (any, error) {
		v, err := b.tokens.SetExpiry(ctx, req.ID, req.ExpiryReq)
		return v, tokenErr(err)
	})
	mesh.Handle(b.Router, MethodTokensRevoke, nil, EditTimeout, func(ctx context.Context, _ model.Identity, req *TokenIDReq) (any, error) {
		if err := b.tokens.Revoke(ctx, req.ID); err != nil {
			return nil, tokenErr(err)
		}
		return map[string]bool{"ok": true}, nil
	})
}

// editTokens changes the role's tokens in its member, and waits until the
// config has been applied.
func (b *Base) editTokens(ctx context.Context, change func([]config.LinkToken) ([]config.LinkToken, error)) error {
	return b.Edit(ctx, func(m map[string]json.RawMessage) error {
		return changeTokens(m, change)
	})
}

// changeTokens changes the tokens of m, a member's members, with change.
func changeTokens(m map[string]json.RawMessage, change func([]config.LinkToken) ([]config.LinkToken, error)) error {
	var ts []config.LinkToken
	if raw, ok := m["tokens"]; ok {
		if err := json.Unmarshal(raw, &ts); err != nil {
			return err
		}
	}
	ts, err := change(ts)
	if err != nil {
		return err
	}
	return Set(m, "tokens", ts)
}

// bind writes into the config that member, a peer's ID, first linked with
// token, so that no other token takes the ID over after a restart.
func (b *Base) bind(token, member string) {
	b.H.Edit(func(cur []byte) ([]byte, error) {
		return EditJSON(cur, func(m map[string]json.RawMessage) error {
			return changeTokens(m, func(ts []config.LinkToken) ([]config.LinkToken, error) {
				return linktoken.Bind(ts, token, member), nil
			})
		})
	}, func(_ []string, err error) {
		if err != nil {
			b.Log.Warnf("keeping in the config that %s links with token %s: %v", member, token, err)
		}
	})
}
