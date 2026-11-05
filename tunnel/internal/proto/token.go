package proto

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/lesismal/fit/internal/linktoken"
)

// tokenLabel names the tunnel's registration in a token's proofs, so that
// no proof of the LLM mesh's checks here.
const tokenLabel = "fit tunnel register v1"

// TokenTranscript is what a registration with a token proves: the token, both
// nonces of the link's challenge, and the peer's type and name.
func TokenTranscript(req *RegisterReq, peerNonce, hubNonce []byte) linktoken.Transcript {
	return linktoken.Transcript{Label: tokenLabel, Token: req.Token, Dialer: peerNonce, Listener: hubNonce,
		Extra: []string{req.Info.Type, req.Info.Name}}
}

// ProveToken fills in req's Token and Proof with sec, over the challenge's
// nonces.
func ProveToken(req *RegisterReq, sec linktoken.Secret, peerNonce, hubNonce []byte) {
	req.Token = sec.ID
	req.Time, req.Nonce, req.MAC = 0, "", ""
	req.Proof = hex.EncodeToString(sec.Prove(TokenTranscript(req, peerNonce, hubNonce)))
}

// rspFields are the fields of an answer a hub's proof covers.
func rspFields(rsp *RegisterRsp) []string {
	return []string{strconv.FormatUint(uint64(rsp.Peer), 10), strconv.FormatUint(uint64(rsp.Conn), 10), strconv.Itoa(rsp.Index), rsp.Hub}
}

// errTokenAuth is the error of a registration whose token's proof does not
// check.
var errTokenAuth = errors.New("tunnel: authentication failed")

// VerifyToken checks the proof of req, a registration with the token whose
// stored key is stored.
func VerifyToken(req *RegisterReq, stored, peerNonce, hubNonce []byte) error {
	proof, err := hex.DecodeString(req.Proof)
	if err != nil || linktoken.Verify(stored, TokenTranscript(req, peerNonce, hubNonce), proof) != nil {
		return errTokenAuth
	}
	return nil
}

// ProveTokenHub fills in rsp's Proof, the hub's answer to req, a
// registration with the token whose stored key is stored.
func ProveTokenHub(req *RegisterReq, rsp *RegisterRsp, stored, peerNonce, hubNonce []byte) {
	rsp.Proof = hex.EncodeToString(linktoken.ListenerProof(stored, TokenTranscript(req, peerNonce, hubNonce), rspFields(rsp)...))
}

// ErrHubTokenProof is the error of a registration with a token answered
// without the hub's proof of the token, or with a wrong one.
var ErrHubTokenProof = errors.New("tunnel: the hub did not prove it issued the token: it is not the hub, or the token is not its")

// CheckTokenProof checks that rsp, the answer to req, proves its hub holds
// the token sec.
func CheckTokenProof(req *RegisterReq, rsp *RegisterRsp, sec linktoken.Secret, peerNonce, hubNonce []byte) error {
	proof, err := hex.DecodeString(rsp.Proof)
	if err != nil || sec.VerifyListener(TokenTranscript(req, peerNonce, hubNonce), proof, rspFields(rsp)...) != nil {
		return ErrHubTokenProof
	}
	return nil
}

// ErrOldHubTokens is the error of a hub that does not take tokens.
var ErrOldHubTokens = fmt.Errorf("tunnel: the hub runs a fit without link tokens, older than this side's %s; upgrade it, or register with its password until then", Version)

// DecodeNonce reads a challenge's nonce, or fails.
func DecodeNonce(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != linktoken.NonceSize {
		return nil, fmt.Errorf("tunnel: a challenge's nonce is %d bytes in hex", linktoken.NonceSize)
	}
	return b, nil
}
