// Command btbench measures fit's bittorrent.client on the benchmark host:
// the throughput and CPU of fit downloading one torrent from several local
// seeders, and of fit seeding it to several local leechers, each beside
// anacrolix/torrent doing the same as a baseline, and fib's client alone in
// a process (fit's client is fib's: the difference is fit's). The other side (seeders
// or leechers) is fib's client, in btbench's own process; the client
// measured is a process of its own, pinned to its CPUs, and its CPU time
// and peak resident memory are read from /proc. Linux only; it runs on the
// benchmark host, never on a workstation (docs/rules.md, rules 16-18):
// bench/bt.sh cross-builds it and runs bench/bt/host.sh there.
//
//	btbench gen -dir D -size BYTES          the data and its .torrent file
//	btbench run -scenario download|seed -impl fit|anacrolix|fib -dir D \
//	    -fit BIN -peers N -mse -server-cpus 0-3 -work W   one measurement, JSON
//	btbench anacrolix|fib ...               a child measured (run starts it)
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent"
	ametainfo "github.com/anacrolix/torrent/metainfo"
	amse "github.com/anacrolix/torrent/mse"
	"github.com/anacrolix/torrent/storage"
)

const dataName = "payload.bin"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: btbench gen|run|anacrolix|fib ...")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = gen(os.Args[2:])
	case "run":
		err = run(os.Args[2:])
	case "anacrolix":
		err = anacrolixChild(os.Args[2:])
	case "fib":
		err = fibChild(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "btbench:", err)
		os.Exit(1)
	}
}

// gen writes dir/data/payload.bin, random bytes, and dir/payload.torrent, a
// v1 torrent of it with 1 MiB pieces.
func gen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	dir := fs.String("dir", "", "the directory")
	size := fs.Int64("size", 1<<30, "the payload's size in bytes")
	_ = fs.Parse(args)
	data := filepath.Join(*dir, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(data, dataName))
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, rand.Reader, *size); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	b := metainfo.Builder{Version: metainfo.V1, PieceLength: 1 << 20}
	mi, err := b.BuildPath(filepath.Join(data, dataName))
	if err != nil {
		return err
	}
	enc, err := bencode.Marshal(mi)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*dir, "payload.torrent"), enc, 0o644)
}

func loadTorrent(dir string) (*metainfo.MetaInfo, []byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "payload.torrent"))
	if err != nil {
		return nil, nil, err
	}
	mi, err := metainfo.Parse(raw)
	return mi, raw, err
}

// result is one measurement.
type result struct {
	Scenario  string  `json:"scenario"`
	Impl      string  `json:"impl"`
	MSE       bool    `json:"mse"`
	Peers     int     `json:"peers"`
	Bytes     int64   `json:"bytes"`
	Seconds   float64 `json:"seconds"`
	MiBps     float64 `json:"mib_per_sec"`
	CPUSecs   float64 `json:"cpu_seconds"`
	CPUPct    float64 `json:"cpu_percent"`
	CPUPerGiB float64 `json:"cpu_seconds_per_gib"`
	RSSMiB    float64 `json:"peak_rss_mib"`
}

// run is one measurement: the client measured (fit or anacrolix) started
// pinned to -server-cpus, the other side in this process.
func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	scenario := fs.String("scenario", "download", "download or seed")
	impl := fs.String("impl", "fit", "fit, anacrolix or fib")
	dir := fs.String("dir", "", "gen's directory")
	work := fs.String("work", "", "a scratch directory, emptied first")
	fitBin := fs.String("fit", "", "fit's binary")
	self := fs.String("self", "", "this binary, for the anacrolix and fib children (os.Executable when empty)")
	peers := fs.Int("peers", 4, "seeders (download) or leechers (seed)")
	mse := fs.Bool("mse", false, "encrypt the connections (MSE, RC4); plaintext otherwise")
	cpus := fs.String("server-cpus", "0-3", "the CPUs of the client measured (taskset -c)")
	timeout := fs.Duration("timeout", 10*time.Minute, "the most a measurement may take")
	_ = fs.Parse(args)
	mi, raw, err := loadTorrent(*dir)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(*work)
	if err := os.MkdirAll(*work, 0o755); err != nil {
		return err
	}
	if *self == "" {
		if *self, err = os.Executable(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res := result{Scenario: *scenario, Impl: *impl, MSE: *mse, Peers: *peers, Bytes: mi.Info.TotalLength()}

	var srv server
	switch *impl {
	case "fit":
		// Seeding, fit's download_dir is where the data is.
		down := filepath.Join(*work, "down")
		if *scenario == "seed" {
			down = filepath.Join(*dir, "data")
		}
		srv, err = startFit(ctx, *fitBin, *cpus, *work, down, *mse)
	case "anacrolix", "fib":
		srv = &childServer{kind: *impl, work: *work, mse: *mse, self: *self, cpus: *cpus}
	default:
		err = fmt.Errorf("unknown -impl %q", *impl)
	}
	if err != nil {
		return err
	}
	defer srv.stop()

	var cpu0 float64
	var start time.Time
	switch *scenario {
	case "download":
		seeders, err := startSeeders(mi, filepath.Join(*dir, "data"), *peers, *mse)
		if err != nil {
			return err
		}
		defer closeAll(seeders)
		var addrs []string
		for _, s := range seeders {
			addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", s.Port()))
		}
		if err := srv.prepare(ctx, raw, filepath.Join(*work, "down")); err != nil {
			return err
		}
		cpu0 = cpuSeconds(srv.pid())
		start = time.Now()
		if err := srv.download(ctx, raw, addrs); err != nil {
			return err
		}
	case "seed":
		if err := srv.seed(ctx, raw, filepath.Join(*dir, "data")); err != nil {
			return err
		}
		cpu0 = cpuSeconds(srv.pid())
		start = time.Now()
		if err := leech(ctx, mi, filepath.Join(*work, "leech"), *peers, srv.addr(), *mse); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown -scenario %q", *scenario)
	}
	res.Seconds = time.Since(start).Seconds()
	res.CPUSecs = cpuSeconds(srv.pid()) - cpu0
	res.RSSMiB = peakRSS(srv.pid())
	moved := float64(res.Bytes)
	if *scenario == "seed" {
		moved *= float64(*peers)
	}
	res.MiBps = moved / (1 << 20) / res.Seconds
	res.CPUPct = 100 * res.CPUSecs / res.Seconds
	res.CPUPerGiB = res.CPUSecs / (moved / (1 << 30))
	return json.NewEncoder(os.Stdout).Encode(res)
}

func closeAll(cls []*fibbt.Client) {
	for _, c := range cls {
		_ = c.Close()
	}
}

// fibClient is a client of the other side: on loopback, TCP alone, no
// discovery.
func fibClient(dir string, mse bool) (*fibbt.Client, error) {
	enc := fibbt.EncryptionAllow
	if mse {
		enc = fibbt.EncryptionRequire
	}
	return fibbt.NewClient(fibbt.Config{
		DataDir: dir, ListenAddr: "127.0.0.1:0", Encryption: enc,
		DisableDHT: true, DisableLSD: true, DisablePEX: true, DisableTrackers: true, DisableUTP: true,
	})
}

// startSeeders starts n fib clients seeding mi from data: the first checks
// the data, the others take its resume data and check nothing.
func startSeeders(mi *metainfo.MetaInfo, data string, n int, mse bool) ([]*fibbt.Client, error) {
	var out []*fibbt.Client
	var resume []byte
	for i := range n {
		c, err := fibClient(data, mse)
		if err != nil {
			closeAll(out)
			return nil, err
		}
		out = append(out, c)
		t, err := c.AddTorrent(mi, &fibbt.AddOptions{Resume: resume})
		if err != nil {
			closeAll(out)
			return nil, err
		}
		select {
		case <-t.Done():
		case <-time.After(5 * time.Minute):
			closeAll(out)
			return nil, fmt.Errorf("seeder %d never had the data", i)
		}
		if resume == nil {
			if resume, err = t.ResumeData(); err != nil {
				closeAll(out)
				return nil, err
			}
		}
	}
	return out, nil
}

// leech has n fib clients download mi from addr at once, each into a
// directory of its own.
func leech(ctx context.Context, mi *metainfo.MetaInfo, dir string, n int, addr string, mse bool) error {
	var wg sync.WaitGroup
	errs := make(chan error, n)
	var cls []*fibbt.Client
	defer func() { closeAll(cls) }()
	var tors []*fibbt.Torrent
	for i := range n {
		c, err := fibClient(filepath.Join(dir, strconv.Itoa(i)), mse)
		if err != nil {
			return err
		}
		cls = append(cls, c)
		t, err := c.AddTorrent(mi, &fibbt.AddOptions{Peers: []string{addr}})
		if err != nil {
			return err
		}
		tors = append(tors, t)
	}
	for i, t := range tors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-t.Done():
			case <-ctx.Done():
				errs <- fmt.Errorf("leecher %d: %v at %+v", i, ctx.Err(), t.Stats())
			}
		}()
	}
	wg.Wait()
	close(errs)
	return <-errs
}

// server is the client measured.
type server interface {
	pid() int
	addr() string
	// prepare readies a download into dir, download makes it.
	prepare(ctx context.Context, torrent []byte, dir string) error
	download(ctx context.Context, torrent []byte, peers []string) error
	seed(ctx context.Context, torrent []byte, dataDir string) error
	stop()
}

// cpuSeconds is the user and system time of process pid, from
// /proc/<pid>/stat.
func cpuSeconds(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	f := strings.Fields(s[i+2:])
	// Fields from the state (3rd): utime is the 14th, stime the 15th.
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / 100 // USER_HZ
}

// peakRSS is process pid's VmHWM in MiB.
func peakRSS(pid int) float64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			kb, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 64)
			return kb / 1024
		}
	}
	return 0
}

func freePort() (int, error) {
	for range 50 {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(port))
		_ = l.Close()
		if err == nil {
			_ = u.Close()
			return port, nil
		}
	}
	return 0, errors.New("no free port")
}

// fitServer is fit, steered through its Admin API.
type fitServer struct {
	cmd   *exec.Cmd
	admin string
	port  int
	work  string
}

func startFit(ctx context.Context, bin, cpus, work, down string, mse bool) (*fitServer, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	aport, err := freePort()
	if err != nil {
		return nil, err
	}
	wport, err := freePort()
	if err != nil {
		return nil, err
	}
	enc := "allow"
	if mse {
		enc = "require"
	}
	cfg := fmt.Sprintf(`{
  "admin": {"address": "127.0.0.1:%d"},
  "listeners": [{"name": "web", "address": "127.0.0.1:%d"}],
  "routes": [],
  "bittorrent": {"client": {"listen": "127.0.0.1:%d", "data_dir": %q, "download_dir": %q,
    "dht": false, "pex": false, "utp": false, "encryption": %q, "limits": {"min_free_bytes": 1}}}
}`, aport, wport, port, filepath.Join(work, "fit-state"), down, enc)
	p := filepath.Join(work, "fit.json")
	if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
		return nil, err
	}
	f := &fitServer{admin: fmt.Sprintf("http://127.0.0.1:%d", aport), port: port, work: work}
	f.cmd = exec.Command("taskset", "-c", cpus, bin, "run", "-c", p)
	log, err := os.Create(filepath.Join(work, "fit.log"))
	if err != nil {
		return nil, err
	}
	f.cmd.Stdout, f.cmd.Stderr = log, log
	if err := f.cmd.Start(); err != nil {
		return nil, err
	}
	for {
		if resp, err := http.Get(f.admin + "/bittorrent/torrents"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return f, nil
			}
		}
		select {
		case <-ctx.Done():
			f.stop()
			return nil, fmt.Errorf("fit did not start: %v", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (f *fitServer) pid() int     { return f.cmd.Process.Pid }
func (f *fitServer) addr() string { return fmt.Sprintf("127.0.0.1:%d", f.port) }

func (f *fitServer) stop() {
	_ = f.cmd.Process.Kill()
	_ = f.cmd.Wait()
}

// wait polls the torrent until it has every piece, every 20ms.
func (f *fitServer) wait(ctx context.Context, id string) error {
	for {
		resp, err := http.Get(f.admin + "/bittorrent/torrents/" + id)
		if err == nil {
			// Pieces, not "complete": the role marks completion at its
			// manager's look, every second, which would round the time
			// up to it.
			var st struct {
				Pieces     int    `json:"pieces"`
				PiecesHave int    `json:"pieces_have"`
				State      string `json:"state"`
			}
			err = json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
			if err == nil && st.Pieces > 0 && st.PiecesHave == st.Pieces && st.State != "checking" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fit's torrent incomplete: %v", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (f *fitServer) prepare(context.Context, []byte, string) error { return nil }

func (f *fitServer) download(ctx context.Context, torrent []byte, peers []string) error {
	b, _ := json.Marshal(map[string]any{"torrent": torrent, "peers": peers})
	id, err := f.addRaw(b)
	if err != nil {
		return err
	}
	return f.wait(ctx, id)
}

func (f *fitServer) addRaw(b []byte) (string, error) {
	resp, err := http.Post(f.admin+"/bittorrent/torrents", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("adding: %d %s", resp.StatusCode, data)
	}
	var st struct {
		ID string `json:"id"`
	}
	return st.ID, json.Unmarshal(data, &st)
}

// seed adds the torrent, whose data is in fit's download_dir, and waits
// for the check.
func (f *fitServer) seed(ctx context.Context, torrent []byte, _ string) error {
	b, _ := json.Marshal(map[string]any{"torrent": torrent})
	id, err := f.addRaw(b)
	if err != nil {
		return err
	}
	return f.wait(ctx, id)
}

// childServer is btbench's anacrolix or fib side, a process of its own: it
// prints READY once it listens (and, seeding, has checked the data), takes
// its peers from the line on its stdin, and prints DONE.
type childServer struct {
	kind  string
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan string
	port  int
	work  string
	mse   bool
	self  string
	cpus  string
}

// start runs the child for mode with the args, waiting for READY.
func (a *childServer) start(ctx context.Context, args ...string) error {
	full := append([]string{"-c", a.cpus, a.self, a.kind}, args...)
	if a.mse {
		full = append(full, "-mse")
	}
	a.cmd = exec.Command("taskset", full...)
	a.cmd.Stderr = os.Stderr
	var err error
	if a.in, err = a.cmd.StdinPipe(); err != nil {
		return err
	}
	out, err := a.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := a.cmd.Start(); err != nil {
		return err
	}
	a.lines = make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			a.lines <- sc.Text()
		}
		close(a.lines)
	}()
	return a.expect(ctx, "READY")
}

func (a *childServer) expect(ctx context.Context, word string) error {
	for {
		select {
		case line, ok := <-a.lines:
			if !ok {
				return fmt.Errorf("%s ended before %s", a.kind, word)
			}
			if rest, ok := strings.CutPrefix(line, word); ok {
				if p, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil {
					a.port = p
				}
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("%s: no %s: %v", a.kind, word, ctx.Err())
		}
	}
}

func (a *childServer) pid() int     { return a.cmd.Process.Pid }
func (a *childServer) addr() string { return fmt.Sprintf("127.0.0.1:%d", a.port) }

func (a *childServer) stop() {
	if a.cmd != nil && a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
		_ = a.cmd.Wait()
	}
}

func (a *childServer) prepare(ctx context.Context, torrent []byte, dir string) error {
	p := filepath.Join(a.work, "payload.torrent")
	if err := os.WriteFile(p, torrent, 0o644); err != nil {
		return err
	}
	return a.start(ctx, "-mode", "leech", "-torrent", p, "-dir", dir)
}

func (a *childServer) download(ctx context.Context, _ []byte, peers []string) error {
	if _, err := fmt.Fprintln(a.in, strings.Join(peers, ",")); err != nil {
		return err
	}
	return a.expect(ctx, "DONE")
}

func (a *childServer) seed(ctx context.Context, torrent []byte, dataDir string) error {
	p := filepath.Join(a.work, "payload.torrent")
	if err := os.WriteFile(p, torrent, 0o644); err != nil {
		return err
	}
	return a.start(ctx, "-mode", "seed", "-torrent", p, "-dir", dataDir)
}

// anacrolixChild is the anacrolix side's process.
func anacrolixChild(args []string) error {
	fs := flag.NewFlagSet("anacrolix", flag.ExitOnError)
	mode := fs.String("mode", "leech", "leech or seed")
	tp := fs.String("torrent", "", "the .torrent file")
	dir := fs.String("dir", "", "the data directory")
	mse := fs.Bool("mse", false, "require MSE (RC4)")
	_ = fs.Parse(args)
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = *dir
	cfg.DefaultStorage = storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   *dir,
		PieceCompletion: storage.NewMapPieceCompletion(),
		UsePartFiles:    g.Some(false),
	})
	cfg.Seed = *mode == "seed"
	cfg.NoDHT = true
	cfg.DisablePEX = true
	cfg.DisableUTP = true
	cfg.DisableTrackers = true
	cfg.DisableWebtorrent = true
	cfg.DisableWebseeds = true
	cfg.DisableIPv6 = true
	cfg.NoDefaultPortForwarding = true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
	if *mse {
		cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
		cfg.CryptoProvides = amse.CryptoMethodRC4
	} else {
		cfg.HeaderObfuscationPolicy = torrent.HeaderObfuscationPolicy{Preferred: false, RequirePreferred: true}
	}
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		return err
	}
	defer cl.Close()
	mi, err := ametainfo.LoadFromFile(*tp)
	if err != nil {
		return err
	}
	port := 0
	for _, a := range cl.ListenAddrs() {
		if ta, ok := a.(*net.TCPAddr); ok {
			port = ta.Port
		}
	}
	if *mode == "seed" {
		t, err := cl.AddTorrent(mi)
		if err != nil {
			return err
		}
		if err := t.VerifyDataContext(context.Background()); err != nil {
			return err
		}
		<-t.Complete().On()
		fmt.Println("READY", port)
		select {} // until killed
	}
	fmt.Println("READY", port)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	t, err := cl.AddTorrent(mi)
	if err != nil {
		return err
	}
	var peers []torrent.PeerInfo
	for p := range strings.SplitSeq(strings.TrimSpace(line), ",") {
		ap, err := net.ResolveTCPAddr("tcp4", p)
		if err != nil {
			return err
		}
		peers = append(peers, torrent.PeerInfo{Addr: ap, Trusted: true})
	}
	t.AddPeers(peers)
	t.DownloadAll()
	<-t.Complete().On()
	fmt.Println("DONE")
	select {} // until killed, so that its peak memory can be read
}

// fibChild is the fib side's process: fib's client alone, as btbench's
// peers are, on its own engine.
func fibChild(args []string) error {
	fs := flag.NewFlagSet("fib", flag.ExitOnError)
	mode := fs.String("mode", "leech", "leech or seed")
	tp := fs.String("torrent", "", "the .torrent file")
	dir := fs.String("dir", "", "the data directory")
	mse := fs.Bool("mse", false, "require MSE (RC4)")
	_ = fs.Parse(args)
	// BTBENCH_FIB_PROFILE names a CPU profile of the download, written
	// when it completes: a diagnosis aid.
	if p := os.Getenv("BTBENCH_FIB_PROFILE"); p != "" && *mode == "leech" {
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	raw, err := os.ReadFile(*tp)
	if err != nil {
		return err
	}
	mi, err := metainfo.Parse(raw)
	if err != nil {
		return err
	}
	cl, err := fibClient(*dir, *mse)
	if err != nil {
		return err
	}
	defer cl.Close()
	if *mode == "seed" {
		t, err := cl.AddTorrent(mi, nil)
		if err != nil {
			return err
		}
		<-t.Done()
		fmt.Println("READY", cl.Port())
		select {} // until killed
	}
	fmt.Println("READY", cl.Port())
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return err
	}
	t, err := cl.AddTorrent(mi, &fibbt.AddOptions{Peers: strings.Split(strings.TrimSpace(line), ",")})
	if err != nil {
		return err
	}
	<-t.Done()
	if os.Getenv("BTBENCH_FIB_PROFILE") != "" {
		pprof.StopCPUProfile()
	}
	fmt.Println("DONE")
	select {} // until killed, so that its peak memory can be read
}
