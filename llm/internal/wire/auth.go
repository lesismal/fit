package wire

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

// NonceSize is the size of the nonce a dialer adds after its proof when its Authenticator is Mutual.
// NonceSize 是 Authenticator 为 Mutual 时拨号方附在证明之后的随机数长度。
const NonceSize = 32

// Mutual is an Authenticator whose listener proves the secret too, so that a dialer does not take a listener that
// does not know it: the dialer sends a nonce of its own after its proof, and the listener's acknowledgement carries
// ProveListener of its challenge and that nonce, which the dialer checks with VerifyListener. A dialer whose
// Authenticator is Mutual takes no listener that does not prove itself; a listener answers a dialer that sends no
// nonce, as one of an earlier version does, with the plain acknowledgement.
//
// Mutual 是监听方也要证明密钥的 Authenticator，使拨号方不会接受不知道密钥的监听方：拨号方在证明之后附上自己的随机数，监听方
// 的确认携带对其挑战与该随机数的 ProveListener，拨号方用 VerifyListener 校验。Authenticator 为 Mutual 的拨号方不接受无法
// 证明自身的监听方；对于不发送随机数的拨号方（如早先版本），监听方照旧只回普通确认。
type Mutual interface {
	Authenticator
	// Mutual reports whether the authenticator proves both ways (a Password does when it is not empty).
	// Mutual 表示该认证是否双向证明（非空 Password 是）。
	Mutual() bool
	// ProveListener is the listener's proof for its challenge and the dialer's nonce.
	// ProveListener 是监听方针对其挑战与拨号方随机数的证明。
	ProveListener(challenge, nonce []byte) []byte
	// VerifyListener checks the listener's proof (dialer side).
	// VerifyListener 校验监听方的证明（拨号方）。
	VerifyListener(challenge, nonce, proof []byte) error
}

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

func (p Password) Mutual() bool { return p != "" }

// listenerLabel sets the listener's proof apart from a dialer's, which is of the challenge alone: a dialer's answer
// to a challenge never proves a listener.
// listenerLabel 把监听方的证明与拨号方的证明（仅针对挑战）区分开：拨号方对挑战的应答永远不能充当监听方的证明。
const listenerLabel = "fit llm wire listener\x00"

func (p Password) ProveListener(challenge, nonce []byte) []byte {
	m := hmac.New(sha256.New, []byte(p))
	m.Write([]byte(listenerLabel))
	m.Write(challenge)
	m.Write(nonce)
	return m.Sum(nil)
}

func (p Password) VerifyListener(challenge, nonce, proof []byte) error {
	if hmac.Equal(proof, p.ProveListener(challenge, nonce)) {
		return nil
	}
	return ErrAuth
}

// Credential is one password a listener takes, and the roles a dialer that proves it may take: Roles is opaque to
// wire, which hands it to the layer above as the connection's Roles.
// Credential 是监听方接受的一个密码，以及证明它的拨号方可担任的角色：Roles 对 wire 不透明，wire 把它作为连接的 Roles
// 交给上层。
type Credential struct {
	Password Password
	Roles    []string
}

// match checks proof against every credential, without stopping at the first that matches, and returns the first
// one it proves, or nil. Their passwords are distinct and not empty (the caller merges the roles of a password).
// match 用每个凭据校验 proof（不在第一个匹配处停下），返回第一个被证明的凭据，没有则为 nil。各凭据的密码互不相同且非空
// （调用方合并同一密码的角色）。
func match(creds []Credential, challenge, proof []byte) *Credential {
	var found *Credential
	for i := range creds {
		if creds[i].Password != "" && hmac.Equal(proof, creds[i].Password.mac(challenge)) && found == nil {
			found = &creds[i]
		}
	}
	return found
}
