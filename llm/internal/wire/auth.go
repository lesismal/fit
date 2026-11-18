package wire

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/argon2"
)

// The handshake that follows the WebSocket upgrade proves, both ways, that each end knows the password of the
// role the dialer links as, and binds both ends' roles to the link:
//
//  1. listener → dialer: version, salt (16 bytes), the listener's nonce Ns (32), the listener's role.
//  2. dialer → listener: version, the dialer's nonce Nd (32), the dialer's proof (32), the dialer's role.
//  3. listener → dialer: ackOK and the listener's proof (32).
//
// Both proofs are HMAC-SHA256, keyed with K = Argon2id(password, salt), over the transcript
// "fit-llm-mesh/2" | salt | Ns | Nd | listener role | dialer role, prefixed "dialer" or "listener". The listener
// keeps one password per role it takes links from, and checks the dialer's proof with the password of the role
// the dialer says it is, so a dialer cannot link as a role whose password it does not know. The dialer checks
// the listener's proof, so a listener that does not know the password cannot complete a dial. The password never
// crosses the network; the key is slow to derive, so a proof seen on the network or answered to a dialer's own
// nonce is costly to guess passwords from. A listener derives its keys once, when it starts; a dialer once per
// listener salt, on its own goroutine.
//
// WebSocket 升级之后的握手双向证明两端都知道拨号方所声明角色的密码，并把两端的角色绑定到连接上：
//
//  1. 监听方 → 拨号方：版本、盐（16 字节）、监听方随机数 Ns（32）、监听方角色。
//  2. 拨号方 → 监听方：版本、拨号方随机数 Nd（32）、拨号方证明（32）、拨号方角色。
//  3. 监听方 → 拨号方：ackOK 与监听方证明（32）。
//
// 两个证明都是以 K = Argon2id(密码, 盐) 为密钥的 HMAC-SHA256，覆盖 "fit-llm-mesh/2" | 盐 | Ns | Nd | 监听方角色 |
// 拨号方角色，并分别以 "dialer"、"listener" 为前缀。监听方为每个接受的角色保存一个密码，用拨号方所声明角色的密码校验其证明，
// 因此拨号方无法以不知道密码的角色连接。拨号方校验监听方的证明，因此不知道密码的监听方无法完成拨号。密码从不在网络上传输；
// 密钥派生很慢，因此从网络上看到的证明、或对拨号方自选随机数的应答去猜测密码代价很高。监听方在启动时派生一次密钥；
// 拨号方对每个监听方盐派生一次，在其自己的协程上进行。

const (
	handshakeVersion = 2
	saltSize         = 16
	nonceSize        = 32
	proofSize        = sha256.Size
	maxRoleLen       = 32
	// maxHandshakeMsg bounds a handshake message.
	maxHandshakeMsg = 1 + saltSize + nonceSize + 1 + maxRoleLen + proofSize
)

// The Argon2id parameters of the key: RFC 9106's second recommended option (64 MiB, 3 passes, 4 lanes).
const (
	kdfTime    = 3
	kdfMemory  = 64 << 10 // KiB
	kdfThreads = 4
	keySize    = 32
)

const transcriptLabel = "fit-llm-mesh/2"

// ErrAuth is returned by a dial that failed the handshake: the listener rejected the dialer's proof, or the
// listener did not prove the password, or is not of the role the dialer expects.
// ErrAuth 表示拨号在握手中失败：监听方拒绝了拨号方的证明，或监听方没有证明其知道密码、或不是拨号方期望的角色。
var ErrAuth = errors.New("wire: authentication rejected")

// kdfSem runs one key derivation at a time in the process: each takes kdfMemory.
var kdfSem = make(chan struct{}, 1)

// DeriveKey derives the handshake key of password for salt. It is slow on purpose (tens of milliseconds and
// 64 MiB), one at a time in the process; never call it on a fib worker.
// DeriveKey 为 salt 派生 password 的握手密钥。它故意很慢（数十毫秒、64 MiB），进程内一次一个；不要在 fib worker 上调用。
func DeriveKey(password string, salt []byte) []byte {
	kdfSem <- struct{}{}
	defer func() { <-kdfSem }()
	return argon2.IDKey([]byte(password), salt, kdfTime, kdfMemory, kdfThreads, keySize)
}

// NewSalt returns a random salt for a listener.
// NewSalt 返回监听方使用的随机盐。
func NewSalt() []byte {
	s := make([]byte, saltSize)
	_, _ = rand.Read(s)
	return s
}

// ListenAuth is the listener's side of the handshake.
// ListenAuth 握手中监听方一侧。
type ListenAuth struct {
	role string
	salt []byte
	keys map[string][]byte // the dialer's role → key / 拨号方角色 → 密钥
}

// NewListenAuth derives the keys of a listener of role, which takes links from the roles of passwords (role →
// password; a role without a password, or with an empty one, is refused). It is slow: see DeriveKey.
// NewListenAuth 为角色 role 的监听方派生密钥，它接受 passwords 中各角色的连接（角色 → 密码；没有密码或密码为空的角色被拒绝）。
// 它很慢：见 DeriveKey。
func NewListenAuth(role string, salt []byte, passwords map[string]string) (*ListenAuth, error) {
	if err := checkRole(role); err != nil {
		return nil, err
	}
	if len(salt) != saltSize {
		return nil, fmt.Errorf("wire: the salt must be %d bytes", saltSize)
	}
	a := &ListenAuth{role: role, salt: bytes.Clone(salt), keys: map[string][]byte{}}
	for r, pw := range passwords {
		if pw == "" {
			continue
		}
		if err := checkRole(r); err != nil {
			return nil, err
		}
		a.keys[r] = DeriveKey(pw, salt)
	}
	return a, nil
}

// DialAuth is the dialer's side of the handshake.
// DialAuth 握手中拨号方一侧。
type DialAuth struct {
	// Role is the dialer's own role, which it proves it may link as.
	// Role 拨号方自己的角色，它证明自己可以以该角色连接。
	Role string
	// PeerRole is the role the listener must be; "" takes any.
	// PeerRole 监听方必须是的角色；为 "" 时不限。
	PeerRole string
	// Password is the listener's password for Role.
	// Password 监听方为 Role 设置的密码。
	Password string
	// Keys, when set, keeps the key derived for the listener's salt across dials.
	// Keys 如已设置，在多次拨号之间保留为监听方盐派生的密钥。
	Keys *KeyCache
}

// KeyCache keeps the key of one password for the last salt it was derived for.
// KeyCache 保存一个密码针对最近一次派生所用盐的密钥。
type KeyCache struct {
	mu       sync.Mutex
	password string
	salt     []byte
	key      []byte
}

func (c *KeyCache) get(password string, salt []byte) []byte {
	if c == nil {
		return DeriveKey(password, salt)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil && c.password == password && bytes.Equal(c.salt, salt) {
		return c.key
	}
	c.key = DeriveKey(password, salt)
	c.password, c.salt = password, bytes.Clone(salt)
	return c.key
}

func checkRole(role string) error {
	if len(role) > maxRoleLen {
		return fmt.Errorf("wire: a role is at most %d bytes", maxRoleLen)
	}
	return nil
}

// transcript is what both proofs are made over.
func transcript(salt, ns, nd []byte, listenerRole, dialerRole string) []byte {
	b := make([]byte, 0, len(transcriptLabel)+saltSize+2*nonceSize+2+len(listenerRole)+len(dialerRole))
	b = append(b, transcriptLabel...)
	b = append(b, salt...)
	b = append(b, ns...)
	b = append(b, nd...)
	b = append(b, byte(len(listenerRole)))
	b = append(b, listenerRole...)
	b = append(b, byte(len(dialerRole)))
	b = append(b, dialerRole...)
	return b
}

func proof(key []byte, side string, t []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(side))
	m.Write(t)
	return m.Sum(nil)
}

// challenge is the listener's first message.
type challenge struct {
	salt, nonce []byte
	role        string
}

func (c challenge) marshal() []byte {
	b := make([]byte, 0, 2+saltSize+nonceSize+len(c.role))
	b = append(b, handshakeVersion)
	b = append(b, c.salt...)
	b = append(b, c.nonce...)
	b = append(b, byte(len(c.role)))
	return append(b, c.role...)
}

func parseChallenge(b []byte) (challenge, error) {
	const fixed = 1 + saltSize + nonceSize + 1
	if len(b) < fixed || b[0] != handshakeVersion || len(b) != fixed+int(b[fixed-1]) {
		return challenge{}, fmt.Errorf("%w: bad handshake challenge (a peer of another version?)", ErrProtocol)
	}
	return challenge{salt: b[1 : 1+saltSize], nonce: b[1+saltSize : 1+saltSize+nonceSize], role: string(b[fixed:])}, nil
}

// answer is the dialer's message.
type answer struct {
	nonce, proof []byte
	role         string
}

func (a answer) marshal() []byte {
	b := make([]byte, 0, 2+nonceSize+proofSize+len(a.role))
	b = append(b, handshakeVersion)
	b = append(b, a.nonce...)
	p := a.proof
	if len(p) != proofSize {
		p = make([]byte, proofSize)
	}
	b = append(b, p...)
	b = append(b, byte(len(a.role)))
	return append(b, a.role...)
}

func parseAnswer(b []byte) (answer, error) {
	const fixed = 1 + nonceSize + proofSize + 1
	if len(b) < fixed || b[0] != handshakeVersion || len(b) != fixed+int(b[fixed-1]) {
		return answer{}, fmt.Errorf("%w: bad handshake answer", ErrProtocol)
	}
	return answer{nonce: b[1 : 1+nonceSize], proof: b[1+nonceSize : 1+nonceSize+proofSize], role: string(b[fixed:])}, nil
}

func newNonce() []byte {
	n := make([]byte, nonceSize)
	_, _ = rand.Read(n)
	return n
}

// listenerState is a handshake in progress on the listener.
type listenerState struct {
	a    *ListenAuth
	chal challenge
}

func newListenerState(a *ListenAuth) *listenerState {
	st := &listenerState{a: a, chal: challenge{nonce: newNonce(), salt: make([]byte, saltSize)}}
	if a != nil {
		st.chal.salt, st.chal.role = a.salt, a.role
	}
	return st
}

// verify checks the dialer's answer; it returns the dialer's role and the ack to send. Without a ListenAuth any
// answer is taken, as from a dialer of no role.
func (st *listenerState) verify(msg []byte) (role string, ack []byte, err error) {
	if st.a == nil {
		if ans, err := parseAnswer(msg); err == nil {
			role = ans.role
		}
		return role, []byte{ackOK}, nil
	}
	ans, err := parseAnswer(msg)
	if err != nil {
		return "", nil, ErrAuth
	}
	key := st.a.keys[ans.role]
	if key == nil || ans.role == st.a.role {
		return "", nil, ErrAuth
	}
	t := transcript(st.chal.salt, st.chal.nonce, ans.nonce, st.a.role, ans.role)
	if !hmac.Equal(ans.proof, proof(key, "dialer", t)) {
		return "", nil, ErrAuth
	}
	return ans.role, append([]byte{ackOK}, proof(key, "listener", t)...), nil
}

// dialerState is a handshake in progress on the dialer.
type dialerState struct {
	a   *DialAuth
	key []byte
	t   []byte
	// peerRole is the role the listener announced.
	peerRole string
}

// answer makes the dialer's answer to the listener's challenge. It derives the key: run it off fib's workers.
func (st *dialerState) answer(msg []byte) ([]byte, error) {
	nd := newNonce()
	if st.a == nil {
		if c, err := parseChallenge(msg); err == nil {
			st.peerRole = c.role
		}
		return answer{nonce: nd}.marshal(), nil
	}
	c, err := parseChallenge(msg)
	if err != nil {
		return nil, err
	}
	if st.a.PeerRole != "" && c.role != st.a.PeerRole {
		return nil, fmt.Errorf("%w: the listener is a %q, not a %s", ErrAuth, c.role, st.a.PeerRole)
	}
	st.peerRole = c.role
	st.key = st.a.Keys.get(st.a.Password, c.salt)
	st.t = transcript(c.salt, c.nonce, nd, c.role, st.a.Role)
	return answer{nonce: nd, proof: proof(st.key, "dialer", st.t), role: st.a.Role}.marshal(), nil
}

// accept checks the listener's ack and its proof.
func (st *dialerState) accept(msg []byte) error {
	if len(msg) == 0 || msg[0] != ackOK {
		return fmt.Errorf("%w: bad handshake ack", ErrProtocol)
	}
	if st.a == nil {
		return nil
	}
	if len(msg) != 1+proofSize || !hmac.Equal(msg[1:], proof(st.key, "listener", st.t)) {
		return fmt.Errorf("%w: the listener did not prove the password", ErrAuth)
	}
	return nil
}
