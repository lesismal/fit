package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/buildinfo"
	"github.com/lesismal/fit/schema"
)

// Where a node ID is kept (Node.IDKept).
const (
	// IDInConfig is an ID the config sets, as admin.node_id.
	IDInConfig = "config"
	// IDInStore is an ID kept in admin.store, in its section NodeSection.
	IDInStore = "store"
	// IDInFile is an ID kept in a file beside the config, named as the
	// config's files without their extension and NodeIDSuffix, as
	// /etc/fit/fit.node-id beside /etc/fit/fit.json.
	IDInFile = "file"
	// IDInMemory is an ID kept nowhere: fit had nowhere to keep it, as with
	// a config read from standard input, and makes another when it
	// restarts.
	IDInMemory = "memory"
)

// NodeSection is the store's section that keeps the node ID, and
// NodeIDSuffix what the file beside the config that keeps it without a
// store ends with.
const (
	NodeSection  = "node"
	NodeIDSuffix = ".node-id"
)

// The capabilities GET /node lists: what a manager may use on this fit,
// which it checks before it calls the endpoints they name. A name stays
// with what it means; a change to what an endpoint answers takes a new
// name.
const (
	// CapNode is GET /node itself.
	CapNode = "node"
	// CapIfMatch: every config change takes If-Match with the ETag GET
	// /config answers, and is refused with 412 when the config changed.
	CapIfMatch = "config.if_match"
	// CapDryRun: PUT, PATCH and DELETE /config, POST /load and PUT
	// /config/text take ?dry_run=1.
	CapDryRun = "config.dry_run"
	// CapText: POST /config/render, POST /config/parse and PUT /config/text.
	CapText = "config.text"
	// CapEffective: GET /config/effective.
	CapEffective = "config.effective"
	// CapDrift: GET /config/check and POST /config/reconcile.
	CapDrift = "config.check"
	// CapVersions: GET /versions, /versions/{n} and POST /rollback/{n}.
	CapVersions = "config.versions"
	// CapTransfer: POST /config/export and /config/import.
	CapTransfer = "config.transfer"
	// CapStatus: GET /status, and CapMetrics GET /metrics.
	CapStatus  = "status"
	CapMetrics = "metrics"
	// CapStatusRequests: GET /status answers requests, the requests answered
	// since fit started by the class of their status, counters that never
	// go back, when the running config counts requests (metrics set).
	CapStatusRequests = "status.requests"
	// CapReload: POST /reload.
	CapReload = "config.reload"
	// CapCache: GET /cache and POST /cache/purge.
	CapCache = "cache"
	// CapCachePrefix: POST /cache/purge takes "prefixes".
	CapCachePrefix = "cache.prefix"
	// CapLogs: GET /logs, POST /logs/rotate and /logs/cleanup.
	CapLogs = "logs"
	// CapTokens: the link tokens, /tokens.
	CapTokens = "tokens"
	// CapMedia: /media/... and /streaming/....
	CapMedia = "media"
	// CapSchema: GET /schema, the JSON Schema of the config this fit
	// takes, whose digest is schema_digest.
	CapSchema = "schema"
	// CapStore: admin.store keeps the config, the console users and the
	// sections beside them.
	CapStore = "store"
	// CapConsole: admin.console runs. Its POST /api/login takes
	// {"username", "password"} and answers {"user", "csrf"} with the
	// session's cookie; every request but a GET or a HEAD sends csrf in
	// X-Fit-Csrf; /api/<path> is the Admin API's /<path>. A login refused
	// for failing too often is answered 429 with Retry-After and
	// {"error", "retry_after"}, the seconds to wait, per address and per
	// user.
	CapConsole = "console"
)

// Node is who this fit is, as GET /node says.
type Node struct {
	// ID is the node's ID, made once and kept where IDKept says.
	ID     string
	IDKept string
	// Hostname is the host's name, as the kernel gives it.
	Hostname string
	// FitVersion and FibVersion are the versions of fit and of the fib
	// linked into it.
	FitVersion, FibVersion string
	// Started is when fit started.
	Started time.Time
}

// newNode is the Node of a fit that UseNode was not given one: an ID kept
// nowhere, made now.
func newNode() *Node {
	host, _ := os.Hostname()
	fit, fib := buildinfo.Versions("")
	return &Node{ID: newNodeID(), IDKept: IDInMemory, Hostname: host, FitVersion: fit, FibVersion: fib, Started: time.Now()}
}

// UseNode says who this fit is, for GET /node. Fields left empty keep what
// Configs found itself: an ID kept nowhere, the host's name, the versions
// the build recorded, and when NewConfigs was called.
func (cs *Configs) UseNode(n Node) {
	old := cs.node.Load()
	if n.ID == "" {
		n.ID, n.IDKept = old.ID, old.IDKept
	}
	if n.Hostname == "" {
		n.Hostname = old.Hostname
	}
	if n.FitVersion == "" {
		n.FitVersion = old.FitVersion
	}
	if n.FibVersion == "" {
		n.FibVersion = old.FibVersion
	}
	if n.Started.IsZero() {
		n.Started = old.Started
	}
	cs.node.Store(&n)
}

// Node returns who this fit is.
func (cs *Configs) Node() Node { return *cs.node.Load() }

// nodeID is what a node ID may be: what NodeID makes, or one a person
// wrote into the file.
var nodeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// newNodeID makes a node ID: a random UUID (version 4).
func newNodeID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// nodeSection is the store's section NodeSection.
type nodeSection struct {
	ID string `json:"id"`
}

// NodeID returns this fit's node ID and where it is kept: in st, when there
// is a store, or else in the file beside the config whose files begin with
// prefix (app.BesidePrefix); it is made, and kept, the first time. An ID in
// the file is taken into a store that has none yet, so that a fit given a
// store keeps its ID. With no prefix and no store, or when keeping it
// fails, the ID is kept in memory only, and err says why when something
// failed. It reads and writes files: call it before serving.
func NodeID(st *store.Store, prefix string) (id, kept string, err error) {
	if st != nil {
		if sec := st.Section(NodeSection); sec != nil {
			var n nodeSection
			if json.Unmarshal(sec, &n) == nil && nodeID.MatchString(n.ID) {
				return n.ID, IDInStore, nil
			}
			return newNodeID(), IDInMemory, fmt.Errorf("the store's section %q holds no node ID", NodeSection)
		}
		id, _ := readNodeID(prefix)
		if id == "" {
			id = newNodeID()
		}
		data, _ := json.Marshal(nodeSection{ID: id})
		if err := st.SetSection(NodeSection, data); err != nil {
			return id, IDInMemory, err
		}
		return id, IDInStore, nil
	}
	if prefix == "" {
		return newNodeID(), IDInMemory, nil
	}
	id, err = readNodeID(prefix)
	if err != nil {
		return newNodeID(), IDInMemory, err
	}
	if id != "" {
		return id, IDInFile, nil
	}
	id = newNodeID()
	if err := writeNodeID(prefix+NodeIDSuffix, id); err != nil {
		return id, IDInMemory, err
	}
	return id, IDInFile, nil
}

// readNodeID reads the ID in the file beside the config, or "" when there
// is none.
func readNodeID(prefix string) (string, error) {
	if prefix == "" {
		return "", nil
	}
	path := prefix + NodeIDSuffix
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if !nodeID.MatchString(id) {
		return "", fmt.Errorf("%s holds no node ID: 1 to 64 letters, digits, '.', '_' and '-'", path)
	}
	return id, nil
}

// writeNodeID writes id to path, through a file beside it renamed over it.
func writeNodeID(path, id string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.WriteString(id + "\n")
	if err2 := tmp.Close(); err == nil {
		err = err2
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}

// schemaJSON is the schema GET /schema answers, made once.
var schemaJSON = sync.OnceValue(func() []byte {
	data, _ := schema.Generate()
	return data
})

// schemaHandler answers GET /schema: the config's JSON Schema, as the
// console's /api/m/admin/schema, for a manager whose node switcher opens
// this fit's pages through its Admin API.
func (s *Server) schemaHandler(w http.ResponseWriter, _ *http.Request) {
	data := schemaJSON()
	w.Header().Set("Content-Type", "application/schema+json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

// schemaDigest is the SHA-256 of the schema the console serves, made once:
// two fits with the same digest take the same configs.
var schemaDigest = sync.OnceValue(func() string {
	data := schemaJSON()
	if data == nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
})

// nodeBody is GET /node's answer.
type nodeBody struct {
	ID            string    `json:"id"`
	IDKept        string    `json:"id_kept"`
	Hostname      string    `json:"hostname"`
	FitVersion    string    `json:"fit_version"`
	FibVersion    string    `json:"fib_version"`
	GoVersion     string    `json:"go_version"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	Started       time.Time `json:"started"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	ConfigVersion uint64    `json:"config_version"`
	ConfigETag    string    `json:"config_etag"`
	ConfigApplied time.Time `json:"config_applied"`
	ConfigSource  string    `json:"config_source"`
	Roles         []string  `json:"roles"`
	SchemaVersion int       `json:"schema_version"`
	SchemaDigest  string    `json:"schema_digest"`
	Capabilities  []string  `json:"capabilities"`
}

// capabilities lists what this fit's Admin API offers, sorted.
func (cs *Configs) capabilities() []string {
	caps := []string{CapNode, CapIfMatch, CapDryRun, CapText, CapEffective, CapDrift, CapVersions, CapTransfer,
		CapReload, CapStatus, CapMetrics, CapCache, CapCachePrefix, CapLogs, CapLogsRecent, CapTokens, CapMedia, CapSchema, CapManaged}
	if cs.Sealer() != nil {
		caps = append(caps, CapStore)
	}
	if cs.env.Metrics != nil {
		// Requests are counted once a config sets metrics.
		caps = append(caps, CapStatusRequests)
	}
	if cs.console.Load() {
		caps = append(caps, CapConsole)
	}
	slices.Sort(caps)
	return caps
}

// roles returns the IDs of the roles running, sorted; none without them.
func (cs *Configs) roles() []string {
	cs.mu.Lock()
	apps := cs.apps
	cs.mu.Unlock()
	if apps == nil {
		return []string{}
	}
	return apps.Running()
}

// node answers GET /node.
func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	n := s.configs.Node()
	v := s.configs.Current()
	w.Header().Set("ETag", v.ETag)
	w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
	writeJSON(w, http.StatusOK, nodeBody{
		ID: n.ID, IDKept: n.IDKept, Hostname: n.Hostname, FitVersion: n.FitVersion, FibVersion: n.FibVersion,
		GoVersion: goruntime.Version(), OS: goruntime.GOOS, Arch: goruntime.GOARCH,
		Started: n.Started.UTC(), UptimeSeconds: int64(time.Since(n.Started) / time.Second),
		ConfigVersion: v.Number, ConfigETag: v.ETag, ConfigApplied: v.Applied.UTC(), ConfigSource: v.Source,
		Roles: s.configs.roles(), SchemaVersion: config.SchemaVersion, SchemaDigest: schemaDigest(),
		Capabilities: s.configs.capabilities(),
	})
}
