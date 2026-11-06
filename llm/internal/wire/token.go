package wire

import (
	"bytes"
	"errors"

	"github.com/lesismal/fit/internal/linktoken"
)

// tokenMagic starts a dialer's answer to the challenge when it proves a
// token rather than a password: the answer is
//
//	magic:4 bytes  idlen:uint8  id  nonce:linktoken.NonceSize  proof:linktoken.ProofSize
//
// It is never the 32 or 64 bytes of a password's answer. The listener
// acknowledges with ackOK and its own proof of the token.
//
// tokenMagic 开始拨号方以令牌（而非密码）应答挑战时的内容（格式见上）；它永远不是密码应答的 32 或 64 字节。监听方以 ackOK
// 及其自身对令牌的证明确认。
const tokenMagic = "FTK1"

// tokenLabel names the mesh's handshake in a token's proofs.
// tokenLabel 在令牌证明中标明 mesh 的握手。
const tokenLabel = "fit llm mesh v1"

// Tokens are the tokens a listener issued: Lookup returns the stored key of
// one by its ID, or why it is refused.
// Tokens 是监听方签发的令牌：Lookup 按 ID 返回令牌存储的密钥，或拒绝的原因。
type Tokens interface {
	Lookup(id string) ([]byte, error)
}

// ErrTokenListener is the error of a dial whose listener did not prove the
// token: it is not the listener that issued it.
// ErrTokenListener 表示监听方未能证明令牌：它不是签发该令牌的监听方。
var ErrTokenListener = errors.New("wire: the listener did not prove it issued the token")

func tokenTranscript(id string, nonce, challenge []byte) linktoken.Transcript {
	return linktoken.Transcript{Label: tokenLabel, Token: id, Dialer: nonce, Listener: challenge}
}

// tokenAnswer is a dialer's answer to challenge with sec, and its nonce.
func tokenAnswer(sec *linktoken.Secret, challenge []byte) ([]byte, []byte) {
	nonce := linktoken.Nonce()
	b := append([]byte(tokenMagic), byte(len(sec.ID)))
	b = append(b, sec.ID...)
	b = append(b, nonce...)
	return append(b, sec.Prove(tokenTranscript(sec.ID, nonce, challenge))...), nonce
}

// parseTokenAnswer splits an answer made by tokenAnswer, or reports false.
func parseTokenAnswer(data []byte) (id string, nonce, proof []byte, ok bool) {
	if !bytes.HasPrefix(data, []byte(tokenMagic)) || len(data) < len(tokenMagic)+1 {
		return "", nil, nil, false
	}
	data = data[len(tokenMagic):]
	n := int(data[0])
	if len(data) != 1+n+linktoken.NonceSize+linktoken.ProofSize || n == 0 {
		return "", nil, nil, false
	}
	return string(data[1 : 1+n]), data[1+n : 1+n+linktoken.NonceSize], data[1+n+linktoken.NonceSize:], true
}

// verifyToken checks a token's answer to challenge, and returns the token's
// ID and the listener's proof to acknowledge with.
func (s *server) verifyToken(challenge, data []byte) (string, []byte, error) {
	id, nonce, proof, ok := parseTokenAnswer(data)
	if !ok || s.cfg.Tokens == nil {
		return "", nil, ErrAuth
	}
	stored, err := s.cfg.Tokens.Lookup(id)
	if err != nil {
		return "", nil, err
	}
	t := tokenTranscript(id, nonce, challenge)
	if linktoken.Verify(stored, t, proof) != nil {
		return "", nil, ErrAuth
	}
	return id, linktoken.ListenerProof(stored, t), nil
}
