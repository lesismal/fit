package wire

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

// Authenticator proves and checks a connection's identity in the handshake that follows the WebSocket upgrade:
// the listener sends a random challenge, the dialer answers with Prove, the listener checks it with Verify and
// either acknowledges or closes the connection. Failing it fails the dial, so a reconnecting dialer backs off.
//
// Authenticator 在 WebSocket 升级之后的握手中证明与校验连接身份：监听方发送随机挑战，拨号方用 Prove 应答，
// 监听方用 Verify 校验后确认或关闭连接。握手失败即拨号失败，重连的拨号方因此会退避。
type Authenticator interface {
	// Prove answers the listener's challenge (dialer side).
	// Prove 应答监听方的挑战（拨号方）。
	Prove(challenge []byte) ([]byte, error)
	// Verify checks the dialer's proof (listener side).
	// Verify 校验拨号方的证明（监听方）。
	Verify(challenge, proof []byte) error
}

// ErrAuth is returned by a dial that the listener rejected in the handshake.
// ErrAuth 表示拨号在握手中被监听方拒绝。
var ErrAuth = errors.New("wire: authentication rejected")

// Password is the shared-password authenticator. The proof is HMAC-SHA256 of the challenge keyed by the password,
// so the password itself never crosses the wire. An empty password accepts everyone (mesh never listens without one).
//
// Password 是共享密码认证。证明为以密码为密钥对挑战计算的 HMAC-SHA256，密码本身不在线上传输。
// 空密码接受所有连接（mesh 从不在无密码时监听）。
type Password string

func (p Password) mac(challenge []byte) []byte {
	m := hmac.New(sha256.New, []byte(p))
	m.Write(challenge)
	return m.Sum(nil)
}

func (p Password) Prove(challenge []byte) ([]byte, error) {
	if p == "" {
		return nil, nil
	}
	return p.mac(challenge), nil
}

func (p Password) Verify(challenge, proof []byte) error {
	if p == "" || hmac.Equal(proof, p.mac(challenge)) {
		return nil
	}
	return ErrAuth
}
