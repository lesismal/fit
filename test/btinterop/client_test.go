package btinterop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	ametainfo "github.com/anacrolix/torrent/metainfo"
	amse "github.com/anacrolix/torrent/mse"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"
)

// The client cases run fit's bittorrent.client role against anacrolix and
// libtorrent both ways: fit downloading from the other's seed, and the
// other downloading from fit's, from a .torrent file and from a magnet
// link, over TCP and over uTP, with Message Stream Encryption off, on as
// fit's default prefers it, and required, and for v1, hybrid and (against
// libtorrent, which has them) v2 torrents.

// enc is how a case uses MSE.
type enc int

const (
	encPlain   enc = iota // fit allow, the other plaintext only: no MSE
	encPrefer             // fit's default (prefer), the other's default
	encRequire            // both require RC4
)

type clientCase struct {
	name    string
	version metainfo.Version
	magnet  bool
	utp     bool
	enc     enc
}

func clientCases() []clientCase {
	return []clientCase{
		{name: "v1-torrent-tcp-plain", version: metainfo.V1},
		{name: "v1-torrent-utp", version: metainfo.V1, utp: true, enc: encPrefer},
		{name: "v1-magnet-tcp", version: metainfo.V1, magnet: true, enc: encPrefer},
		{name: "v1-magnet-utp-plain", version: metainfo.V1, magnet: true, utp: true},
		{name: "v1-magnet-tcp-rc4", version: metainfo.V1, magnet: true, enc: encRequire},
		{name: "v1-torrent-utp-rc4", version: metainfo.V1, utp: true, enc: encRequire},
		{name: "hybrid-magnet-tcp", version: metainfo.Hybrid, magnet: true, enc: encPrefer},
		{name: "hybrid-torrent-utp-rc4", version: metainfo.Hybrid, utp: true, enc: encRequire},
	}
}

// v2Cases are libtorrent's more: anacrolix has no v2-only torrents.
func v2Cases() []clientCase {
	return []clientCase{
		{name: "v2-magnet-tcp", version: metainfo.V2, magnet: true, enc: encPrefer},
		{name: "v2-torrent-utp-plain", version: metainfo.V2, utp: true},
		{name: "v2-magnet-utp-rc4", version: metainfo.V2, magnet: true, utp: true, enc: encRequire},
	}
}

// clientFiles writes a multi-file torrent's content below dir/name: files
// of sizes that do not fall on piece boundaries, one of them larger.
func clientFiles(t *testing.T, dir, name string) map[string][]byte {
	t.Helper()
	r := rand.New(rand.NewPCG(7, 11))
	out := map[string][]byte{}
	for i, n := range []int{3<<20 + 333, 200_000, 77, 600_001} {
		b := make([]byte, n)
		for j := range b {
			b[j] = byte(r.Uint32())
		}
		rel := filepath.Join(name, fmt.Sprintf("sub%d", i%2), fmt.Sprintf("f%d.bin", i))
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		out[rel] = b
	}
	return out
}

func buildTorrent(t *testing.T, path string, v metainfo.Version, private bool, trackers ...string) *metainfo.MetaInfo {
	t.Helper()
	b := metainfo.Builder{Version: v, PieceLength: 64 << 10, Private: private}
	for _, tr := range trackers {
		b.Trackers = append(b.Trackers, []string{tr})
	}
	mi, err := b.BuildPath(path)
	if err != nil {
		t.Fatal(err)
	}
	return mi
}

func marshal(t *testing.T, mi *metainfo.MetaInfo) []byte {
	t.Helper()
	b, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeTorrent(t *testing.T, mi *metainfo.MetaInfo) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.torrent")
	if err := os.WriteFile(p, marshal(t, mi), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// magnetOf is a magnet link of mi with only its hash (btih, or btmh for a
// v2 torrent) and the peers given.
func magnetOf(mi *metainfo.MetaInfo, peers ...string) string {
	m := &metainfo.Magnet{Peers: peers}
	if mi.Info.HasV1() {
		m.InfoHash = mi.Info.InfoHashV1()
	} else {
		m.InfoHashV2 = mi.Info.InfoHashV2()
	}
	return m.String()
}

// torrentID is fit's ID of mi: its v1 info hash, or its v2 hash cut to 20
// bytes, in hex.
func torrentID(mi *metainfo.MetaInfo) string {
	if mi.Info.HasV1() {
		h := mi.Info.InfoHashV1()
		return hex.EncodeToString(h[:])
	}
	h := mi.Info.InfoHashV2()
	return hex.EncodeToString(h[:20])
}

// sameFiles checks that dir has the files of want.
func sameFiles(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	for rel, b := range want {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, b) {
			t.Fatalf("%s differs", rel)
		}
	}
}

// fitClient is the bittorrent.client member of a case: no DHT, PEX or
// LSD, uTP on only for a uTP case (fit dials uTP first, then TCP), the
// case's encryption, and no disk reserve, since CI's disks are small. Its
// rates are 2 MiB/s, so that a transfer lasts the seconds it takes to see
// its peers through the Admin API, which lists only those connected.
func fitClient(t *testing.T, c clientCase, downloadDir string) string {
	utp := "false"
	if c.utp {
		utp = "true"
	}
	encryption := "prefer"
	switch c.enc {
	case encPlain:
		encryption = "allow"
	case encRequire:
		encryption = "require"
	}
	return fmt.Sprintf(`{"data_dir": %q, "download_dir": %q, "dht": false, "pex": false, "utp": %s, "encryption": %q, "save_interval": "200ms", "limits": {"min_free_bytes": 1, "download_rate": 2097152, "upload_rate": 2097152}}`,
		t.TempDir(), downloadDir, utp, encryption)
}

// checkPeers checks the connections fit had in a case: uTP or TCP as the
// case says, encrypted when it requires it, plaintext when it is off.
func checkPeers(t *testing.T, peers []peerStatus, c clientCase) {
	t.Helper()
	if len(peers) == 0 {
		t.Fatal("fit never listed a peer")
	}
	for _, p := range peers {
		if p.UTP != c.utp {
			t.Errorf("%v (%s): uTP %v", p.Addr, p.Client, p.UTP)
		}
		if c.enc == encRequire && !p.Encrypted {
			t.Errorf("%v (%s): not encrypted", p.Addr, p.Client)
		}
		if c.enc == encPlain && p.Encrypted {
			t.Errorf("%v (%s): encrypted", p.Addr, p.Client)
		}
	}
}

// anacrolixTorrent turns fib's metainfo into anacrolix's.
func anacrolixTorrent(t *testing.T, mi *metainfo.MetaInfo) *ametainfo.MetaInfo {
	t.Helper()
	ami, err := ametainfo.Load(bytes.NewReader(marshal(t, mi)))
	if err != nil {
		t.Fatal(err)
	}
	return ami
}

// anacrolixOpts are the knobs of an anacrolix client beside a case's.
type anacrolixOpts struct {
	seed     bool
	trackers bool
	webSeeds bool
	upload   int // bytes per second, 0 for no limit
}

// newAnacrolix starts an anacrolix client on 127.0.0.1, over TCP or uTP
// alone, without DHT, PEX or port mapping.
func newAnacrolix(t *testing.T, dir string, c clientCase, o anacrolixOpts) *torrent.Client {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   dir,
		PieceCompletion: storage.NewMapPieceCompletion(),
		UsePartFiles:    g.Some(false),
	})
	cfg.Seed = o.seed
	cfg.NoDHT = true
	cfg.DisablePEX = true
	cfg.DisableUTP = !c.utp
	cfg.DisableTCP = c.utp
	cfg.DisableTrackers = !o.trackers
	cfg.DisableWebtorrent = true
	cfg.DisableWebseeds = !o.webSeeds
	cfg.DisableIPv6 = true
	cfg.NoDefaultPortForwarding = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	// anacrolix's writer can lose a wake-up and then sends nothing until
	// its keepalive timer fires (fib's interop notes); a short keepalive
	// bounds that stall.
	cfg.KeepAliveTimeout = 2 * time.Second
	switch c.enc {
	case encPlain:
		cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: false, RequirePreferred: true}
	case encRequire:
		cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
		cfg.CryptoProvides = amse.CryptoMethodRC4
	}
	if o.upload > 0 {
		cfg.UploadRateLimiter = rate.NewLimiter(rate.Limit(o.upload), 256<<10)
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func anacrolixAddr(cl *torrent.Client) string {
	for _, a := range cl.ListenAddrs() {
		switch a := a.(type) {
		case *net.TCPAddr:
			return fmt.Sprintf("127.0.0.1:%d", a.Port)
		case *net.UDPAddr:
			return fmt.Sprintf("127.0.0.1:%d", a.Port)
		}
	}
	return ""
}

// anacrolixSeed has cl seed mi from the data it finds in its directory.
func anacrolixSeed(t *testing.T, cl *torrent.Client, mi *metainfo.MetaInfo) *torrent.Torrent {
	t.Helper()
	at, err := cl.AddTorrent(anacrolixTorrent(t, mi))
	if err != nil {
		t.Fatal(err)
	}
	if err := at.VerifyDataContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-at.Complete().On():
	case <-time.After(hang):
		t.Fatal("anacrolix does not have the data it seeds")
	}
	return at
}

// anacrolixCheck reads every file of a complete anacrolix torrent and
// compares it with want.
func anacrolixCheck(t *testing.T, at *torrent.Torrent, want map[string][]byte, name string) {
	t.Helper()
	for _, f := range at.Files() {
		rel := filepath.FromSlash(f.Path())
		w, ok := want[filepath.Join(name, rel)]
		if !ok {
			continue // a padding file
		}
		r := f.NewReader()
		got, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, w) {
			t.Fatalf("%s differs in anacrolix", rel)
		}
	}
}

// fitSeed starts fit seeding mi from dir, where its data is, and waits
// until it has every piece.
func fitSeed(t *testing.T, c clientCase, dir string, mi *metainfo.MetaInfo) (*fitProc, status) {
	t.Helper()
	f := startFit(t, fitConfig{client: fitClient(t, c, dir)})
	st := f.add(t, map[string]any{"torrent": marshal(t, mi)})
	return f, f.waitComplete(t, st.ID)
}

// fitLeech has f download mi, from a .torrent file or a magnet link as the
// case says, from the peer at addr, and checks the files and the peers.
func fitLeech(t *testing.T, c clientCase, mi *metainfo.MetaInfo, addr string, files map[string][]byte) {
	t.Helper()
	ldir := t.TempDir()
	f := startFit(t, fitConfig{client: fitClient(t, c, ldir)})
	body := map[string]any{"peers": []string{addr}}
	if c.magnet {
		body["magnet"] = magnetOf(mi)
	} else {
		body["torrent"] = marshal(t, mi)
	}
	st := f.add(t, body)
	w := f.watchPeers(t, st.ID)
	f.waitComplete(t, st.ID)
	sameFiles(t, ldir, files)
	checkPeers(t, w.peers(), c)
}

func TestClientFromAnacrolix(t *testing.T) {
	for _, c := range clientCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.utp && !haveLibutp {
				t.Skip("anacrolix's uTP is go-libutp only with cgo")
			}
			sdir := t.TempDir()
			files := clientFiles(t, sdir, "content")
			mi := buildTorrent(t, filepath.Join(sdir, "content"), c.version, false)
			seeder := newAnacrolix(t, sdir, c, anacrolixOpts{seed: true})
			anacrolixSeed(t, seeder, mi)
			fitLeech(t, c, mi, anacrolixAddr(seeder), files)
		})
	}
}

func TestAnacrolixFromClient(t *testing.T) {
	for _, c := range clientCases() {
		t.Run(c.name, func(t *testing.T) {
			if c.utp && !haveLibutp {
				t.Skip("anacrolix's uTP is go-libutp only with cgo")
			}
			sdir := t.TempDir()
			files := clientFiles(t, sdir, "content")
			mi := buildTorrent(t, filepath.Join(sdir, "content"), c.version, false)
			f, st := fitSeed(t, c, sdir, mi)
			w := f.watchPeers(t, st.ID)

			cl := newAnacrolix(t, t.TempDir(), c, anacrolixOpts{})
			var at *torrent.Torrent
			if c.magnet {
				at, _ = cl.AddTorrentInfoHash(ametainfo.Hash(mi.Info.InfoHashV1()))
			} else {
				var err error
				if at, err = cl.AddTorrent(anacrolixTorrent(t, mi)); err != nil {
					t.Fatal(err)
				}
			}
			addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.bt}
			at.AddPeers([]torrent.PeerInfo{{Addr: addr, Trusted: true}})
			ctx, cancel := context.WithTimeout(context.Background(), hang)
			defer cancel()
			select {
			case <-at.GotInfo():
			case <-ctx.Done():
				t.Fatal("anacrolix got no metadata from fit")
			}
			at.DownloadAll()
			select {
			case <-at.Complete().On():
			case <-ctx.Done():
				t.Fatalf("anacrolix incomplete: %d of %d bytes; fit %+v", at.BytesCompleted(), mi.Info.TotalLength(), f.get(t, st.ID))
			}
			anacrolixCheck(t, at, files, "content")
			checkPeers(t, w.peers(), c)
			if up := f.get(t, st.ID).Uploaded; up < mi.Info.TotalLength()/2 {
				t.Fatalf("fit uploaded %d bytes", up)
			}
		})
	}
}

// libtorrentPeer runs libtorrent_peer.py and hands its output lines to a
// channel.
type libtorrentPeer struct {
	port  int
	lines chan string
}

func needLibtorrent(t *testing.T) {
	t.Helper()
	if os.Getenv("FIT_LIBTORRENT") == "" {
		t.Skip("set FIT_LIBTORRENT=1 with python3-libtorrent installed; run.sh does")
	}
}

func startLibtorrent(t *testing.T, args ...string) *libtorrentPeer {
	t.Helper()
	cmd := exec.Command("python3", append([]string{"libtorrent_peer.py"}, args...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &libtorrentPeer{lines: make(chan string, 4096)}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
	}()
	for line := range p.lines {
		if _, err := fmt.Sscanf(line, "PORT %d", &p.port); err == nil {
			return p
		}
	}
	t.Fatal("libtorrent_peer.py ended without a port")
	return nil
}

// wait waits for the line "DONE", logging the rest.
func (p *libtorrentPeer) wait(t *testing.T, what string) {
	t.Helper()
	deadline := time.After(hang)
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				t.Fatalf("libtorrent ended before %s", what)
			}
			if line == "DONE" {
				return
			}
			t.Log("libtorrent:", line)
		case <-deadline:
			t.Fatalf("libtorrent: no %s", what)
		}
	}
}

func libtorrentFlags(c clientCase) []string {
	var f []string
	if c.utp {
		f = append(f, "--utp")
	}
	switch c.enc {
	case encPlain:
		f = append(f, "--plain")
	case encRequire:
		f = append(f, "--require")
	}
	return f
}

func TestClientLibtorrent(t *testing.T) {
	needLibtorrent(t)
	for _, c := range append(clientCases(), v2Cases()...) {
		t.Run("fit-from-libtorrent/"+c.name, func(t *testing.T) {
			sdir := t.TempDir()
			files := clientFiles(t, sdir, "content")
			mi := buildTorrent(t, filepath.Join(sdir, "content"), c.version, false)
			lt := startLibtorrent(t, append([]string{"seed", writeTorrent(t, mi), sdir}, libtorrentFlags(c)...)...)
			fitLeech(t, c, mi, fmt.Sprintf("127.0.0.1:%d", lt.port), files)
		})
		t.Run("libtorrent-from-fit/"+c.name, func(t *testing.T) {
			sdir := t.TempDir()
			files := clientFiles(t, sdir, "content")
			mi := buildTorrent(t, filepath.Join(sdir, "content"), c.version, false)
			f, st := fitSeed(t, c, sdir, mi)
			w := f.watchPeers(t, st.ID)
			src := writeTorrent(t, mi)
			if c.magnet {
				src = magnetOf(mi)
			}
			ldir := t.TempDir()
			lt := startLibtorrent(t, append([]string{"leech", src, ldir, "--peer", fmt.Sprintf("127.0.0.1:%d", f.bt)}, libtorrentFlags(c)...)...)
			lt.wait(t, "the download")
			sameFiles(t, ldir, files)
			checkPeers(t, w.peers(), c)
		})
	}
}
