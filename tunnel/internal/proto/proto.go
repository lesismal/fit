// Package proto is what the tunnel's server, nodes and clients say to each
// other over their links (package link, which documents the wire format):
// the methods, the messages they carry, the password check of a
// registration and the frames of a tunnel.
//
// A tunnel is a TCP connection a client accepted, carried to a node through
// the server, and the TCP connection the node opened to a service for it.
// Its data, acknowledgements and close travel as notifies whose body is a
// frame: the tunnel's ID, 8 bytes big-endian, and then the payload. The
// server passes a frame on as it is, to the other end of the tunnel.
package proto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/lesismal/fit/tunnel/internal/latency"
)

// Version is the version every peer reports: fit's, as the build recorded
// it.
var Version = version()

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path == "github.com/lesismal/fit" && info.Main.Version != "" {
			return "fit " + info.Main.Version
		}
		for _, d := range info.Deps {
			if d.Path == "github.com/lesismal/fit" {
				return "fit " + d.Version
			}
		}
	}
	return "fit"
}

// The types of peer a server serves.
const (
	TypeNode   = "node"
	TypeClient = "client"
)

// Method is what a call or notify asks for: one byte on the wire.
type Method uint8

// The methods: those a peer calls on the server, those the server calls on
// a node, and the tunnel methods, which go both ways.
const (
	// MethodRegister authenticates a node or client and registers it:
	// RegisterReq answered with RegisterRsp. It must be the first call on a
	// connection; the server refuses every other one until it succeeds.
	// Every connection of a peer's pool registers, and the server takes the
	// connections of one type and name, from however many processes, for
	// one peer.
	MethodRegister Method = 1
	// MethodUpdate replaces what a registered peer told about itself: Info
	// answered with "ok".
	MethodUpdate Method = 2
	// MethodNodes lists the nodes a server has: an empty request answered
	// with NodeList. The server also notifies it, with a NodeList, to every
	// client whenever the list changes.
	MethodNodes Method = 3
	// MethodSetNodeServices has the server set a node's services:
	// SetServicesReq answered with the node's new Info.
	MethodSetNodeServices Method = 4
	// MethodPing measures a peer's link to the server: a peer calls it
	// every few seconds with a PingReq, which tells the server what the
	// peer measured, and the server answers with a PingRsp. The time the
	// call takes is the link's round trip.
	MethodPing Method = 5
	// MethodKick tells a peer the server is closing its connection, with the
	// reason as the body.
	MethodKick Method = 6

	// MethodNodeInfo asks a node for its Info: an empty request answered
	// with Info.
	MethodNodeInfo Method = 16
	// MethodNodeSetServices sets a node's services: []Service answered with
	// the node's new Info.
	MethodNodeSetServices Method = 17

	// MethodTunnelOpen opens a tunnel: a client calls it on the server with
	// a TunnelOpenReq, and the server calls it on the node with the same
	// request, Client filled in. Both answer with a TunnelOpenRsp.
	MethodTunnelOpen Method = 32
	// MethodTunnelData carries a frame of the tunnel's bytes.
	MethodTunnelData Method = 33
	// MethodTunnelAck acknowledges bytes of a tunnel its end has written to
	// its TCP connection: a frame whose payload is the count, 8 bytes
	// big-endian. An end sends no more than Window bytes it has not had
	// acknowledged.
	MethodTunnelAck Method = 34
	// MethodTunnelClose closes a tunnel: a frame whose payload is the
	// reason. An end sends it after the last of its data, and the other end
	// closes its TCP connection once that data is written.
	MethodTunnelClose Method = 35
)

var methodNames = map[Method]string{
	MethodRegister: "register", MethodUpdate: "update", MethodNodes: "nodes", MethodSetNodeServices: "node/services",
	MethodPing: "ping", MethodKick: "kick", MethodNodeInfo: "node/info", MethodNodeSetServices: "node/set-services",
	MethodTunnelOpen: "tunnel/open", MethodTunnelData: "tunnel/data", MethodTunnelAck: "tunnel/ack", MethodTunnelClose: "tunnel/close",
}

func (m Method) String() string {
	if n, ok := methodNames[m]; ok {
		return n
	}
	return "method " + strconv.Itoa(int(m))
}

// Flow control of a tunnel.
const (
	// Window is how many bytes one end of a tunnel may send that the other
	// has not acknowledged.
	Window = 4 << 20
	// AckEvery is how many bytes an end lets arrive before it acknowledges
	// them.
	AckEvery = 256 << 10
	// MaxChunk is the most bytes one data frame carries.
	MaxChunk = 64 << 10
	// MaxTunnelsPerLink is the most tunnels one link carries at once: what
	// waits on a link is bounded by the windows of its tunnels, and so by
	// MaxTunnelsPerLink windows. A peer that needs more tunnels opens more
	// links, with a larger pool.
	MaxTunnelsPerLink = 64
	// LinkQueue is what a link may have waiting to be sent: the windows of
	// its tunnels, and room for the control messages beside them.
	LinkQueue = MaxTunnelsPerLink*Window + 4<<20
)

// Service is a TCP service a node reaches and opens tunnels to.
type Service struct {
	// Name identifies the service among its node's.
	Name string `json:"name"`
	// Addr is the service's TCP address, host:port.
	Addr string `json:"addr"`
	Desc string `json:"desc,omitempty"`
}

// Info is what a peer tells the server about itself.
type Info struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Version  string `json:"version,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	// Services are the services a node reaches.
	Services []Service `json:"services,omitempty"`
	// Meta is anything else the peer was configured to tell.
	Meta map[string]string `json:"meta,omitempty"`
}

// NewInfo is an Info of this process.
func NewInfo(name, typ string) Info {
	host, _ := os.Hostname()
	return Info{Name: name, Type: typ, Version: Version, Hostname: host, OS: runtime.GOOS + "/" + runtime.GOARCH}
}

// FindService returns the service of info named name.
func (info *Info) FindService(name string) (Service, bool) {
	for _, s := range info.Services {
		if s.Name == name {
			return s, true
		}
	}
	return Service{}, false
}

// RegisterReq authenticates and registers a connection of a peer's pool.
// MAC is the HMAC-SHA256, keyed with the server's password, of the peer's
// type and name, Time and Nonce; see Sign.
type RegisterReq struct {
	Info  Info   `json:"info"`
	Time  int64  `json:"time"`
	Nonce string `json:"nonce"`
	MAC   string `json:"mac"`
}

// RegisterRsp answers a registration.
type RegisterRsp struct {
	// Peer is the peer's number on the server, for as long as any of its
	// connections lasts.
	Peer uint32 `json:"peer"`
	// Conn is the connection's number on the server, which no other
	// connection has while it lasts. The tunnels a client opens over it
	// have it in their IDs' high 32 bits.
	Conn uint32 `json:"conn"`
	// Index is the connection's place among the peer's connections: the
	// lowest that none of them holds, as a Unix file descriptor is.
	Index int `json:"index"`
	// Server is the server's name.
	Server string `json:"server"`
}

// NodeView is a node as the server lists it to clients.
type NodeView struct {
	Info  Info      `json:"info"`
	Addr  string    `json:"addr"`
	Since time.Time `json:"since"`
	NodeLatency
}

// NodeLatency is how fast a node's links are: to the server, and to each of
// its services.
type NodeLatency struct {
	// RTT is the round trip between the server and the node.
	RTT latency.Stats `json:"rtt"`
	// ServiceRTT is, by service name, how long the node's connects to the
	// service took.
	ServiceRTT map[string]latency.Stats `json:"serviceRtt,omitempty"`
}

// PingReq is what a peer measured: its round trips to the server and, for
// a node, its connects to its services.
type PingReq struct {
	RTT        latency.Stats            `json:"rtt"`
	ServiceRTT map[string]latency.Stats `json:"serviceRtt,omitempty"`
}

// PingRsp answers a ping. A client is told how fast each node's links are,
// by node name, which it weighs, with its own round trip to the server, to
// pick the server a tunnel goes through.
type PingRsp struct {
	Nodes map[string]NodeLatency `json:"nodes,omitempty"`
}

// NodeList is the nodes of a server.
type NodeList struct {
	Server string     `json:"server"`
	Nodes  []NodeView `json:"nodes"`
	// Epoch is the server's, picked when it starts, and Version counts the
	// changes of its nodes, so that a client keeps the newest of the lists
	// it is sent: the answer to a call may arrive after a notify of a later
	// change. Zero when they are not counted.
	Epoch   uint64 `json:"epoch,omitempty"`
	Version uint64 `json:"version,omitempty"`
}

// Older reports whether l is older than cur, a list from the same server:
// both counted, in the same epoch, l with the lower version.
func (l *NodeList) Older(cur *NodeList) bool {
	return cur != nil && l.Version != 0 && cur.Version != 0 && l.Epoch == cur.Epoch && l.Version < cur.Version
}

// SetServicesReq sets the services of the node named Node.
type SetServicesReq struct {
	Node     string    `json:"node"`
	Services []Service `json:"services"`
}

// TunnelOpenReq opens tunnel ID to service Service of node Node.
type TunnelOpenReq struct {
	ID      uint64 `json:"id"`
	Node    string `json:"node"`
	Service string `json:"service"`
	// Client is the name of the client that opens it, which the server
	// fills in for the node.
	Client string `json:"client,omitempty"`
}

// TunnelOpenRsp answers a TunnelOpenReq once the node is connected to the
// service.
type TunnelOpenRsp struct {
	ID uint64 `json:"id"`
	// Addr is the service's address.
	Addr string `json:"addr"`
}

// TunnelID is the ID of the seq-th tunnel opened over the connection the
// server numbered conn.
func TunnelID(conn uint32, seq uint32) uint64 { return uint64(conn)<<32 | uint64(seq) }

// TunnelConn is the connection number in a tunnel's ID.
func TunnelConn(id uint64) uint32 { return uint32(id >> 32) }

// AckPayload is the payload of a frame acknowledging n bytes.
func AckPayload(n int64) []byte { return binary.BigEndian.AppendUint64(nil, uint64(n)) }

// ErrShortFrame is the error of a frame too short to carry a tunnel ID.
var ErrShortFrame = errors.New("tunnel: frame shorter than a tunnel ID")

// ParseFrame splits a frame into its tunnel ID and payload.
func ParseFrame(b []byte) (uint64, []byte, error) {
	if len(b) < 8 {
		return 0, nil, ErrShortFrame
	}
	return binary.BigEndian.Uint64(b), b[8:], nil
}

// ParseAck is the count of an acknowledgement's payload.
func ParseAck(payload []byte) (int64, error) {
	if len(payload) != 8 {
		return 0, fmt.Errorf("tunnel: ack payload of %d bytes", len(payload))
	}
	return int64(binary.BigEndian.Uint64(payload)), nil
}

// MaxClockSkew is how far a registration's Time may be from the server's
// clock.
const MaxClockSkew = 5 * time.Minute

// MaxNonces is how many registration nonces a Verifier remembers; past it,
// a registration is refused until older ones expire, so that a flood of
// registrations cannot grow it without bound.
const MaxNonces = 1 << 16

// Sign fills in req's Time, Nonce and MAC for password.
func Sign(req *RegisterReq, password string) {
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	req.Time = time.Now().Unix()
	req.Nonce = hex.EncodeToString(nonce[:])
	req.MAC = mac(req, password)
}

func mac(req *RegisterReq, password string) string {
	h := hmac.New(sha256.New, []byte(password))
	h.Write([]byte(req.Info.Type + "\n" + req.Info.Name + "\n" + strconv.FormatInt(req.Time, 10) + "\n" + req.Nonce))
	return hex.EncodeToString(h.Sum(nil))
}

// Verifier checks registrations against a password, and refuses a nonce it
// has seen within MaxClockSkew, so a registration overheard cannot be
// replayed.
type Verifier struct {
	Password string

	mu    sync.Mutex
	seen  map[string]time.Time
	swept time.Time
}

// Verify checks req.
func (v *Verifier) Verify(req *RegisterReq) error {
	if req.Info.Name == "" {
		return errors.New("tunnel: a peer needs a name")
	}
	if req.Info.Type != TypeNode && req.Info.Type != TypeClient {
		return fmt.Errorf("tunnel: unknown peer type %q", req.Info.Type)
	}
	now := time.Now()
	if d := now.Sub(time.Unix(req.Time, 0)); d > MaxClockSkew || d < -MaxClockSkew {
		return errors.New("tunnel: registration time too far from the server's clock")
	}
	want := mac(req, v.Password)
	if len(req.Nonce) < 16 || len(req.Nonce) > 128 || !hmac.Equal([]byte(want), []byte(req.MAC)) {
		return errors.New("tunnel: authentication failed")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seen == nil {
		v.seen = map[string]time.Time{}
	}
	// A nonce is kept as long as its registration's time could pass.
	if len(v.seen) >= MaxNonces || now.Sub(v.swept) > time.Minute {
		v.swept = now
		for nonce, at := range v.seen {
			if now.Sub(at) > 2*MaxClockSkew {
				delete(v.seen, nonce)
			}
		}
	}
	if _, ok := v.seen[req.Nonce]; ok {
		return errors.New("tunnel: registration replayed")
	}
	if len(v.seen) >= MaxNonces {
		return errors.New("tunnel: too many registrations; try again later")
	}
	v.seen[req.Nonce] = now
	return nil
}
