package admin

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/nettcp"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/login"
	"github.com/lesismal/fit/medialib"
)

// The Admin API's timeouts: for a request's header, its whole body, writing
// its response, and an idle connection; variables for tests to shorten.
var (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = time.Minute
	writeTimeout      = time.Minute
	idleTimeout       = time.Minute
	// unauthTimeout is how long after it is accepted a connection that has
	// made no authenticated request yet may still read: its handshake, its
	// requests, and its idle time between them all fall within it, so that
	// clients without the token cannot hold the connections long, however
	// often they ask.
	unauthTimeout = 5 * time.Second
)

const maxHeaderBytes = 64 << 10

// VersionHeader carries the version of the config a response is about.
const VersionHeader = "Fit-Config-Version"

// DryRunHeader is set, to 1, on the answer of a dry run that passed: the
// config was checked, not applied, and VersionHeader and ETag are still the
// running config's.
const DryRunHeader = "Fit-Dry-Run"

// Server serves the Admin API:
//
//	GET    /config[/<pointer>]  the running config, as it was written, or
//	                            the value a JSON pointer names in it
//	PUT    /config[/<pointer>]  set that value to the body; "-" appends
//	PATCH  /config[/<pointer>]  merge the body into it (RFC 7396)
//	DELETE /config/<pointer>    remove it
//	POST   /load                replace the running config with the body
//	POST   /rollback/{n}        apply version n again, as a new version
//	POST   /reload              apply the running config again, as a new
//	                            version, loading again what it loads from
//	                            files: certificates, keys, token files
//	GET    /versions            the configs kept, oldest first
//	GET    /versions/{n}        the config applied as version n
//	GET    /config/effective    the config as it runs, with what providers
//	                            provided merged in, and where each part
//	                            came from
//	GET    /config/check        the differences between the running service
//	                            and the config it runs from, with how to fix
//	                            each drift
//	POST   /config/reconcile    apply the fixes a client selected from
//	                            /config/check: {"fixes":[{"id","action"}]}
//	POST   /config/render       a config fragment, a JSON object, written in
//	                            the text format ?format= names: .conf, .yaml
//	                            or .json
//	POST   /config/parse        the JSON of such a text
//	PUT    /config/text         apply the text as the whole config, or, with
//	                            ?members=a,b, as those top-level members
//	                            only, leaving the rest as it is
//	GET    /node                this fit: its node ID, host name, versions,
//	                            start, running config, roles, the schema's
//	                            digest and the API's capabilities (node.go)
//	GET    /node/managed        the parts of the config a manager of many
//	                            fits owns, each with whether it changed here
//	                            since; PUT keeps a manager's record of them,
//	                            DELETE forgets it (managed.go)
//	GET    /status              what fit is doing now: listeners,
//	                            connections, upstreams and their backends'
//	                            health, certificates
//	GET    /cache               what each cache zone keeps
//	POST   /cache/purge         remove responses a cache zone keeps, by
//	                            URL, by URL prefix, by tag (Cache-Tag,
//	                            Surrogate-Key) or all: {"zone", "urls",
//	                            "prefixes", "tags", "all"}
//	GET    /logs                the file outputs of fit's own log and the
//	                            access log, with their loggers, formats,
//	                            time zones, rotation and files on disk
//	POST   /logs/rotate         rotate one output now: {"path"}
//	POST   /logs/cleanup        remove what retention says, or report it
//	                            with dry_run: {"path", "dry_run"}
//	GET    /tokens              the link tokens of every role that issues
//	                            them (tunnel.hub, llm.*); /tokens/{role}
//	                            one role's; POST issues one, PATCH
//	                            /tokens/{role}/{id} changes its expiry,
//	                            DELETE revokes it (see tokens.go)
//	GET    /media/library       the media libraries and their scans;
//	                            /media/library/{name} one library, with a
//	                            page of its items; .../items/{id} one item;
//	                            POST .../rescan scans it again (medialib.go);
//	                            .../uploads resumable uploads, and
//	                            .../rename, move, delete and sidecar
//	                            changes of its files (mediafiles.go)
//	POST   /media/preview       a short-lived URL of an item or a channel,
//	                            signed for the console's user; GET
//	                            /media/auth the media_auth policies, with
//	                            .../{policy}/sessions a page of their
//	                            playback sessions, POST .../sessions/end
//	                            ending one, .../rotate rotating keys and
//	                            .../urls a test URL; GET /media/stats what
//	                            the libraries, vod handlers, policies and
//	                            channels counted (media.go)
//	GET    /streaming/channels  the streaming role's channels: what each
//	                            plays and what comes next; POST
//	                            /streaming/channels/{name} starts, stops
//	                            or skips one; /streaming/push the push
//	                            targets, POST .../{name} starts, stops or
//	                            restarts one (channels.go)
//	GET    /sync/pipelines      the sync role's pipelines: each one's state,
//	                            checkpoint, queue, counts, throughput, lag
//	                            and errors; /sync/pipelines/{name} one, and
//	                            POST there pauses, resumes or snapshots it;
//	                            .../checkpoint shown, set (PUT) and reset
//	                            (DELETE), with confirm when that skips or
//	                            replays records; POST .../test tests its
//	                            connections and .../dry-run reads its first
//	                            records through its transforms, writing
//	                            nothing; POST /sync/test and /sync/dry-run
//	                            the same for a pipeline's config;
//	                            .../dead-letters lists the dead letters,
//	                            POST .../dead-letters/replay writes them to
//	                            the sinks again and DELETE drops them
//	                            (sync.go)
//	GET    /bittorrent/torrents the BitTorrent client's torrents, filtered
//	                            and paged; POST adds one by magnet link,
//	                            URL or .torrent file; /bittorrent/torrents/
//	                            {id} one, with its files, POST pauses,
//	                            resumes or rechecks it, PATCH changes its
//	                            priorities, category, limits and place,
//	                            DELETE removes it; .../files, .../peers,
//	                            .../trackers; /bittorrent/alt_speed the
//	                            alternative rates (PUT auto, on or off);
//	                            /bittorrent/publish the publish entries,
//	                            .../<name>/torrents/<id> a .torrent file;
//	                            /bittorrent/events its events as
//	                            server-sent events; /bittorrent/tracker/
//	                            stats, .../torrents and .../passkey the
//	                            tracker's (bittorrent.go, btevents.go);
//	                            /bittorrent/rss the RSS feeds and rules,
//	                            .../items the items seen, POST .../feeds/
//	                            <name>/poll polls one, PUT .../feeds/<name>
//	                            and .../rules/<name> turn them on or off
//	                            (btrss.go)
//	GET    /dashboard/          read-only web pages of the GET endpoints,
//	                            unless admin.disable_dashboard
//	GET    /debug/pprof/        Go's runtime profiles, as net/http/pprof
//	                            serves them, while the config has pprof;
//	                            on this server only, not the console's
//	                            (pprof.go)
//
// Every config carries an ETag, a hash of it as written; a change sent with
// If-Match is applied only if the running config is still that one, and is
// otherwise answered 412. A change applies a whole new config, so an edit
// that leaves the config invalid is refused, with its errors.
//
// /config is the base config, as the file or the API wrote it; what
// providers provide is merged into it, and only GET /config/effective
// shows that.
//
// PUT, PATCH and DELETE /config, POST /load and PUT /config/text take
// ?dry_run=1: the config the request would apply is checked through the
// whole path an apply takes, If-Match, loaded, compiled and its listeners'
// new addresses tried, and answered as an apply is, with its errors or with
// "dry_run": true, the running config's version and ETag, the ETag the
// config would have and the listeners it would add and remove; nothing is
// applied, kept or counted (Configs.DryRun).
//
// GET /config/check compares what fit runs with the config it runs from and
// lists each difference, a drift with the fixes that end it or an info that
// is a runtime fact. POST /config/reconcile applies the fixes a client
// selected, by ID and action: it checks again inside the edit that applies
// the change, so that a selection is refused with 400 when the difference is
// gone or the action is not offered, applies each fix's patch to the base
// config, and applies the result as any change, with If-Match required. A
// reapply fix applies the running config again, as a reload does.
type Server struct {
	configs  *Configs
	report   func(Result)
	token    []byte // nil when no token is asked for
	loopback bool   // a TCP address without a token: the Host field must name a loopback host
	maxBody  int64
	source   string // what the configs this server applies are from
	// audit, when not nil, writes the changes of media files to the
	// assistant's audit log: the console's server sets it.
	audit auditFunc
	// cluster serves /cluster/ on the Admin API of a manager of other
	// fits (SetCluster).
	cluster clusterSlot

	ln   net.Listener
	srv  *http.Server
	done chan struct{} // closed when Serve returns
	// closing is closed when the server shuts down, ending the event
	// streams, which never go idle; nil for the console's.
	closing chan struct{}
}

// Start listens where a says and serves the Admin API for configs, over
// TLS when a sets cert_file. report, when not nil, is called after each
// config the API applies; warnf, when not nil, hears what goes wrong
// without stopping the API, as certificate files that changed and do not
// load.
func Start(a *config.Admin, configs *Configs, report func(Result), warnf func(format string, args ...any)) (*Server, error) {
	s := &Server{configs: configs, report: report, maxBody: a.ConfigBytes(), source: SourceAdmin, done: make(chan struct{}), closing: make(chan struct{})}
	if warnf == nil {
		warnf = func(string, ...any) {}
	}
	if a.TokenFile != "" {
		data, err := os.ReadFile(a.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("admin: token_file: %w", err)
		}
		if s.token = []byte(strings.TrimSpace(string(data))); len(s.token) == 0 {
			return nil, fmt.Errorf("admin: token_file %s is empty", a.TokenFile)
		}
	}
	var certs *certFiles
	if a.TLS() {
		var err error
		if certs, err = loadCertFiles(a, warnf); err != nil {
			return nil, err
		}
	}
	network, address := a.Network()
	if network == "unix" {
		if err := removeStaleSocket(address); err != nil {
			return nil, err
		}
	}
	ln, err := nettcp.Listen(network, address, a.MPTCP)
	if err != nil {
		return nil, fmt.Errorf("admin: %w", err)
	}
	if network == "unix" {
		// Only fit's own user may connect.
		if err := os.Chmod(address, 0o600); err != nil {
			_ = ln.Close()
			return nil, fmt.Errorf("admin: %w", err)
		}
	}
	s.loopback = network == "tcp" && s.token == nil
	if network == "tcp" {
		// Under the limitListener, so that a peer refused takes no slot.
		ln = &accessListener{Listener: ln, gate: &configs.adminAccess}
	}
	s.ln = newLimitListener(ln, login.NewSlots(a.Conns()), unauthTimeout)
	if certs != nil {
		// Over the limitListener, so that a connection is counted, and its
		// life bounded, before its handshake.
		s.ln = tls.NewListener(s.ln, certs.serverConfig())
	}

	mux := s.routes()
	mux.HandleFunc("/cluster", s.clusterAPI)
	mux.HandleFunc("/cluster/", s.clusterAPI)
	mux.Handle("/debug/pprof/", s.pprofHandler())
	if !a.DisableDashboard {
		mux.Handle("GET /dashboard/", dashboardHandler())
		mux.Handle("GET /dashboard", http.RedirectHandler("/dashboard/", http.StatusMovedPermanently))
	}
	s.srv = &http.Server{
		Handler:           s.guard(mux),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*tls.Conn); ok {
				c = tc.NetConn()
			}
			return context.WithValue(ctx, connKey{}, c)
		},
		// HTTP/1.1 alone, over TLS too: one request at a time on each of
		// the connections max_conns counts.
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	s.srv.RegisterOnShutdown(func() { close(s.closing) })
	go func() {
		defer close(s.done)
		_ = s.srv.Serve(s.ln)
	}()
	return s, nil
}

// routes returns the API's endpoints, with nothing in front of them.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	for _, p := range []string{"/config", "/config/{path...}"} {
		mux.HandleFunc("GET "+p, s.getConfig)
		mux.HandleFunc("PUT "+p, s.putConfig)
		mux.HandleFunc("PATCH "+p, s.patchConfig)
		mux.HandleFunc("DELETE "+p, s.deleteConfig)
	}
	mux.HandleFunc("GET /config/effective", s.effective)
	mux.HandleFunc("GET /config/check", s.check)
	mux.HandleFunc("POST /config/reconcile", s.reconcile)
	mux.HandleFunc("POST /config/render", s.renderText)
	mux.HandleFunc("POST /config/parse", s.parseText)
	mux.HandleFunc("PUT /config/text", s.putText)
	mux.HandleFunc("POST /config/export", s.exportConfig)
	mux.HandleFunc("POST /config/import", s.importConfig)
	mux.HandleFunc("POST /load", s.load)
	mux.HandleFunc("POST /rollback/{n}", s.rollback)
	mux.HandleFunc("POST /reload", s.reload)
	mux.HandleFunc("GET /versions", s.versions)
	mux.HandleFunc("GET /versions/{n}", s.version)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /status", s.status)
	mux.HandleFunc("GET /node", s.node)
	mux.HandleFunc("GET /node/managed", s.getManaged)
	mux.HandleFunc("PUT /node/managed", s.putManaged)
	mux.HandleFunc("DELETE /node/managed", s.deleteManaged)
	mux.HandleFunc("GET /schema", s.schemaHandler)
	mux.HandleFunc("GET /cache", s.cacheZones)
	mux.HandleFunc("POST /cache/purge", s.purge)
	mux.HandleFunc("GET /logs", s.logs)
	mux.HandleFunc("GET /logs/recent", s.recentLogs)
	mux.HandleFunc("POST /logs/rotate", s.logsRotate)
	mux.HandleFunc("POST /logs/cleanup", s.logsCleanup)
	mux.HandleFunc("GET /tokens", s.listAllTokens)
	mux.HandleFunc("GET /tokens/{role}", s.listTokens)
	mux.HandleFunc("POST /tokens/{role}", s.issueToken)
	mux.HandleFunc("PATCH /tokens/{role}/{id}", s.tokenExpiry)
	mux.HandleFunc("DELETE /tokens/{role}/{id}", s.revokeToken)
	mux.HandleFunc("GET /media/library", s.libraries)
	mux.HandleFunc("GET /media/library/{name}", s.libraryPage)
	mux.HandleFunc("GET /media/library/{name}/items/{id...}", s.libraryItem)
	mux.HandleFunc("POST /media/library/{name}/rescan", s.rescan)
	mux.HandleFunc("GET /media/library/{name}/uploads", s.listUploads)
	mux.HandleFunc("POST /media/library/{name}/uploads", s.createUpload)
	mux.HandleFunc("GET /media/library/{name}/uploads/{id}", s.getUpload)
	mux.HandleFunc("PATCH /media/library/{name}/uploads/{id}", s.patchUpload)
	mux.HandleFunc("DELETE /media/library/{name}/uploads/{id}", s.cancelUpload)
	mux.HandleFunc("GET /media/library/{name}/dirs", s.libraryDirs)
	mux.HandleFunc("GET /media/library/{name}/usage", s.itemUsage)
	mux.HandleFunc("POST /media/library/{name}/rename", s.changeItem(medialib.OpRename))
	mux.HandleFunc("POST /media/library/{name}/move", s.changeItem(medialib.OpMove))
	mux.HandleFunc("POST /media/library/{name}/delete", s.changeItem(medialib.OpDelete))
	mux.HandleFunc("GET /media/library/{name}/sidecar", s.getSidecar)
	mux.HandleFunc("PUT /media/library/{name}/sidecar", s.putSidecar)
	mux.HandleFunc("POST /media/preview", s.preview)
	mux.HandleFunc("GET /media/auth", s.mediaAuth)
	mux.HandleFunc("GET /media/auth/{policy}/sessions", s.authSessions)
	mux.HandleFunc("POST /media/auth/{policy}/sessions/end", s.endSession)
	mux.HandleFunc("POST /media/auth/{policy}/rotate", s.rotateKeys)
	mux.HandleFunc("POST /media/auth/{policy}/urls", s.testURL)
	mux.HandleFunc("GET /media/stats", s.mediaStats)
	mux.HandleFunc("GET /streaming/channels", s.listChannels)
	mux.HandleFunc("GET /streaming/channels/{name...}", s.getChannel)
	mux.HandleFunc("POST /streaming/channels/{name...}", s.channelAction)
	mux.HandleFunc("GET /streaming/push", s.listPush)
	mux.HandleFunc("POST /streaming/push/{name...}", s.pushAction)
	mux.HandleFunc("GET /sync/pipelines", s.syncPipelines)
	mux.HandleFunc("GET /sync/pipelines/{name}", s.syncPipeline)
	mux.HandleFunc("POST /sync/pipelines/{name}", s.syncAction)
	mux.HandleFunc("GET /sync/pipelines/{name}/checkpoint", s.syncCheckpoint)
	mux.HandleFunc("PUT /sync/pipelines/{name}/checkpoint", s.syncSetCheckpoint)
	mux.HandleFunc("DELETE /sync/pipelines/{name}/checkpoint", s.syncResetCheckpoint)
	mux.HandleFunc("POST /sync/pipelines/{name}/test", s.syncTestRunning)
	mux.HandleFunc("POST /sync/pipelines/{name}/dry-run", s.syncDryRunRunning)
	mux.HandleFunc("POST /sync/test", s.syncTestConfig)
	mux.HandleFunc("POST /sync/dry-run", s.syncDryRunConfig)
	mux.HandleFunc("GET /sync/pipelines/{name}/dead-letters", s.syncDeadLetters)
	mux.HandleFunc("POST /sync/pipelines/{name}/dead-letters/replay", s.syncReplay)
	mux.HandleFunc("DELETE /sync/pipelines/{name}/dead-letters", s.syncDropDeadLetters)
	mux.HandleFunc("GET /bittorrent/torrents", s.btTorrents)
	mux.HandleFunc("POST /bittorrent/torrents", s.btAdd)
	mux.HandleFunc("GET /bittorrent/torrents/{id}", s.btTorrent)
	mux.HandleFunc("POST /bittorrent/torrents/{id}", s.btAction)
	mux.HandleFunc("PATCH /bittorrent/torrents/{id}", s.btPatch)
	mux.HandleFunc("DELETE /bittorrent/torrents/{id}", s.btRemove)
	mux.HandleFunc("GET /bittorrent/torrents/{id}/files", s.btFiles)
	mux.HandleFunc("GET /bittorrent/torrents/{id}/peers", s.btPeers)
	mux.HandleFunc("GET /bittorrent/torrents/{id}/trackers", s.btTrackers)
	mux.HandleFunc("GET /bittorrent/alt_speed", s.btAltSpeed)
	mux.HandleFunc("PUT /bittorrent/alt_speed", s.btSetAltSpeed)
	mux.HandleFunc("GET /bittorrent/publish", s.btPublish)
	mux.HandleFunc("GET /bittorrent/publish/{name}", s.btPublish)
	mux.HandleFunc("GET /bittorrent/publish/{name}/torrents/{id}", s.btPublishedTorrent)
	mux.HandleFunc("GET /bittorrent/events", s.btEvents)
	mux.HandleFunc("GET /bittorrent/rss", s.btRSS)
	mux.HandleFunc("GET /bittorrent/rss/items", s.btRSSItems)
	mux.HandleFunc("POST /bittorrent/rss/feeds/{name}/poll", s.btRSSPoll)
	mux.HandleFunc("PUT /bittorrent/rss/feeds/{name}", s.btRSSToggle(false))
	mux.HandleFunc("PUT /bittorrent/rss/rules/{name}", s.btRSSToggle(true))
	mux.HandleFunc("GET /bittorrent/tracker/stats", s.btTrackerStats)
	mux.HandleFunc("GET /bittorrent/tracker/torrents", s.btTrackerTorrents)
	mux.HandleFunc("GET /bittorrent/tracker/torrents/{hash}", s.btTrackerTorrent)
	mux.HandleFunc("GET /bittorrent/tracker/passkey", s.btPasskey)
	return mux
}

// removeStaleSocket removes a socket left at path by a process that is gone,
// and fails if a process still listens there.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode().Type() != os.ModeSocket {
		return nil // nothing there, or something listening would fail on it anyway
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return fmt.Errorf("admin: %s is in use", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("admin: %w", err)
	}
	return nil
}

// Addr returns the address the Admin API listens on.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Shutdown stops taking requests and waits for those under way, until ctx
// ends; then it closes their connections.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	if err != nil {
		_ = s.srv.Close()
	}
	<-s.done
	return err
}

// guard refuses what must not reach the API: a request without the token,
// when one is asked for, but for the dashboard's files, which hold no data
// and ask for the token themselves; one from a web page, which a browser
// marks with Origin, but a GET from the API's own origin, as the
// dashboard's are; and, on a TCP address without a token, one whose Host is
// not a loopback name, as a web page's is after DNS rebinding.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, _ := s.configs.TemporaryPassword(); user != "" {
			writeError(w, http.StatusForbidden, fmt.Sprintf("the console user %q still has its temporary password: change it on the console first", user))
			return
		}
		valid := false
		if s.token != nil {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			valid = ok && subtle.ConstantTimeCompare([]byte(got), s.token) == 1
			if !valid && !isDashboard(r) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="fit admin"`)
				// Nothing more is taken on a connection that asks without
				// the token: it is not kept for another try.
				w.Header().Set("Connection", "close")
				writeError(w, http.StatusUnauthorized, "a valid bearer token is needed")
				return
			}
		}
		if o := r.Header.Get("Origin"); o != "" && !sameOriginRead(r, o) {
			writeError(w, http.StatusForbidden, "requests from web pages are refused")
			return
		}
		if s.loopback && !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "the Host field must name a loopback host")
			return
		}
		if c, ok := r.Context().Value(connKey{}).(*limitConn); ok {
			switch {
			case s.token == nil || valid:
				c.authenticate()
			case !c.authenticated():
				// A dashboard file asked for without the token: nothing
				// more is taken on the connection, so that a client cannot
				// queue requests on it to hold it.
				w.Header().Set("Connection", "close")
			}
		}
		next.ServeHTTP(w, r)
	})
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// pointer returns the JSON pointer a /config/{path...} request names; none
// for /config itself, or /config/.
func pointer(r *http.Request) ([]string, error) {
	path := r.PathValue("path")
	if path == "" {
		return nil, nil
	}
	return parsePointer("/" + path)
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	tokens, err := pointer(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	v := s.configs.Current()
	w.Header().Set("ETag", v.ETag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, v.ETag) {
		w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if tokens == nil {
		writeConfig(w, v)
		return
	}
	root, err := decodeJSON(v.data)
	if err == nil {
		root, err = lookup(root, tokens)
	}
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	data := encodeJSON(root)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func writeConfig(w http.ResponseWriter, v *Version) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", v.ETag)
	w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
	w.Header().Set("Content-Length", strconv.Itoa(len(v.data)))
	_, _ = w.Write(v.data)
}

// readBody reads a request's body, which must be one of types and no larger
// than max_config_bytes. It answers the request itself when it cannot.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request, types ...string) ([]byte, bool) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !slices.Contains(types, mt) {
		writeError(w, http.StatusUnsupportedMediaType, "the body must be sent as "+strings.Join(types, " or "))
		return nil, false
	}
	if r.ContentLength > s.maxBody {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than max_config_bytes, %d", s.maxBody))
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("the body is larger than max_config_bytes, %d", s.maxBody))
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "reading the body: "+err.Error())
		return nil, false
	}
	return data, true
}

// isDryRun reports whether r asks for a dry run: ?dry_run=1 or true. Any
// other value but 0, false or none is an error.
func isDryRun(r *http.Request) (bool, error) {
	switch v := r.URL.Query().Get("dry_run"); v {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, badRequest("dry_run is 1 or true, 0 or false, not %q", v)
	}
}

// update applies the config edit makes, with the request's If-Match, or
// only checks it when dry.
func (s *Server) update(r *http.Request, dry bool, edit func(cur *Version) ([]byte, error)) (Result, error) {
	if dry {
		return s.configs.DryRun(s.source, r.Header.Get("If-Match"), edit)
	}
	return s.configs.Update(s.source, r.Header.Get("If-Match"), edit)
}

func (s *Server) load(w http.ResponseWriter, r *http.Request) {
	dry, err := isDryRun(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	res, err := s.update(r, dry, func(*Version) ([]byte, error) { return data, nil })
	s.applied(w, res, err)
}

// putConfig sets the value at the pointer to the body: it replaces a member
// or an element, adds a member, or, at "-", appends to an array.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	s.edit(w, r, []string{"application/json"}, func(root any, tokens []string, body any) (any, error) {
		return put(root, tokens, body)
	})
}

// patchConfig merges the body into the value at the pointer, as a JSON merge
// patch (RFC 7396).
func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request) {
	s.edit(w, r, []string{"application/merge-patch+json", "application/json"}, func(root any, tokens []string, body any) (any, error) {
		target, err := lookup(root, tokens)
		if err != nil {
			return nil, err
		}
		return put(root, tokens, mergePatch(target, body))
	})
}

// deleteConfig removes the member or element at the pointer.
func (s *Server) deleteConfig(w http.ResponseWriter, r *http.Request) {
	tokens, err := pointer(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	dry, err := isDryRun(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	res, err := s.update(r, dry, func(cur *Version) ([]byte, error) {
		root, err := decodeJSON(cur.data)
		if err == nil {
			root, err = remove(root, tokens)
		}
		if err != nil {
			return nil, err
		}
		return encodeJSON(root), nil
	})
	s.applied(w, res, err)
}

// edit applies change, given the running config's tree, the request's
// pointer and its body, and applies the config it gives.
func (s *Server) edit(w http.ResponseWriter, r *http.Request, types []string, change func(root any, tokens []string, body any) (any, error)) {
	tokens, err := pointer(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	dry, err := isDryRun(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	data, ok := s.readBody(w, r, types...)
	if !ok {
		return
	}
	body, err := decodeJSON(data)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	res, err := s.update(r, dry, func(cur *Version) ([]byte, error) {
		if tokens == nil && r.Method == http.MethodPut {
			return data, nil // a whole config, kept as written
		}
		root, err := decodeJSON(cur.data)
		if err == nil {
			root, err = change(root, tokens, body)
		}
		if err != nil {
			return nil, err
		}
		return encodeJSON(root), nil
	})
	s.applied(w, res, err)
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseUint(r.PathValue("n"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "versions are numbers")
		return
	}
	res, err := s.configs.Rollback(n, r.Header.Get("If-Match"))
	s.applied(w, res, err)
}

// reload answers POST /reload: the running config applied again, as a new
// version, with If-Match when sent and ?dry_run=1 as the other writes take
// it; no body.
func (s *Server) reload(w http.ResponseWriter, r *http.Request) {
	dry, err := isDryRun(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	res, err := s.configs.Reload(s.source, r.Header.Get("If-Match"), dry)
	s.applied(w, res, err)
}

// applied answers a request that applied a config, or tried to, or only
// checked it (Result.DryRun).
func (s *Server) applied(w http.ResponseWriter, res Result, err error) {
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	if s.report != nil && !res.DryRun {
		s.report(res)
	}
	w.Header().Set("ETag", res.ETag)
	w.Header().Set(VersionHeader, strconv.FormatUint(res.Version, 10))
	if res.DryRun {
		w.Header().Set(DryRunHeader, "1")
	}
	warnings := slices.Clone(res.Warnings)
	for _, e := range res.Deprecated {
		warnings = append(warnings, e.Error())
	}
	writeJSON(w, http.StatusOK, loaded{DryRun: res.DryRun, Version: res.Version, ETag: res.ETag, NewETag: res.NewETag, Unchanged: res.Unchanged,
		Added: listeners(res.Added), Removed: listeners(res.Removed), Warnings: warnings})
}

func writeUpdateError(w http.ResponseWriter, err error) {
	var errs config.Errors
	var re *RequestError
	switch {
	case errors.Is(err, ErrChanged):
		writeError(w, http.StatusPreconditionFailed, strings.TrimPrefix(err.Error(), "admin: "))
	case errors.As(err, &re):
		writeError(w, re.Status, re.Msg)
	case errors.As(err, &errs):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "the config was not applied; the running config stays", Errors: problems(errs)})
	default:
		writeError(w, http.StatusInternalServerError, "the config was not applied; the running config stays: "+err.Error())
	}
}

func (s *Server) effective(w http.ResponseWriter, r *http.Request) {
	e := s.configs.Effective()
	w.Header().Set(VersionHeader, strconv.FormatUint(e.Version, 10))
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) versions(w http.ResponseWriter, r *http.Request) {
	vs := s.configs.Versions()
	writeJSON(w, http.StatusOK, struct {
		Current  uint64     `json:"current"`
		Versions []*Version `json:"versions"`
	}{vs[len(vs)-1].Number, vs})
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseUint(r.PathValue("n"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "versions are numbers")
		return
	}
	v, ok := s.configs.Version(n)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("version %d is not kept", n))
		return
	}
	writeConfig(w, v)
}

// loaded is POST /load's answer, and every change's. A dry run's has
// dry_run, the running config's version and ETag, new_etag, the ETag the
// config checked would have, and unchanged when that is the running one.
type loaded struct {
	DryRun    bool           `json:"dry_run,omitempty"`
	Version   uint64         `json:"version"`
	ETag      string         `json:"etag"`
	NewETag   string         `json:"new_etag,omitempty"`
	Unchanged bool           `json:"unchanged,omitempty"`
	Added     []listenerJSON `json:"listeners_added,omitempty"`
	Removed   []listenerJSON `json:"listeners_removed,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
}

type listenerJSON struct {
	Name      string   `json:"name"`
	Address   string   `json:"address"`
	Protocols []string `json:"protocols"`
}

func listeners(as []listener.Addr) []listenerJSON {
	ls := make([]listenerJSON, len(as))
	for i, a := range as {
		ls[i] = listenerJSON{Name: a.Name, Address: a.Addr.String(), Protocols: a.Protocols}
	}
	return ls
}

type errorBody struct {
	Error  string    `json:"error"`
	Errors []problem `json:"errors,omitempty"`
}

// problem is a config error, as the API reports it.
type problem struct {
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

func problems(errs config.Errors) []problem {
	ps := make([]problem, len(errs))
	for i, e := range errs {
		ps[i] = problem{Path: e.Path, Line: e.Line, Column: e.Col, Message: e.Msg}
	}
	return ps
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"error":"encoding the answer failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)+1))
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

// limitListener counts the connections it accepts (login.Slots): past its
// limit, or past the share of it unauthenticated connections may hold, a
// new one closes the oldest unauthenticated one and takes its place; it is
// itself closed at once, rather than left to queue, only when every
// connection has authenticated, or when its client already holds its share
// (login.Peers).
type limitListener struct {
	net.Listener
	slots  *login.Slots
	unauth time.Duration // how long after its accept a connection is kept before it is authenticated
}

func newLimitListener(ln net.Listener, slots *login.Slots, unauth time.Duration) *limitListener {
	return &limitListener{Listener: ln, slots: slots, unauth: unauth}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		var ip netip.Addr
		if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
			ip = a.AddrPort().Addr()
		}
		lc := &limitConn{Conn: c, until: time.Now().Add(l.unauth)}
		lc.mu.Lock()
		lc.slot = l.slots.Open(ip, func() { _ = lc.Close() })
		if lc.slot == nil {
			lc.mu.Unlock()
			_ = c.Close()
			continue
		}
		lc.timer = time.AfterFunc(l.unauth, lc.expire)
		lc.mu.Unlock()
		return lc, nil
	}
}

// accessListener closes, as it accepts them, the connections of the peers
// its gate refuses (admin.access, then the global access), and keeps
// accepting. A Unix socket's peers have no address, and are not put
// through one.
type accessListener struct {
	net.Listener
	gate *access.Gate
}

func (l *accessListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if a, ok := c.RemoteAddr().(*net.TCPAddr); ok && !l.gate.Allows(a.AddrPort()) {
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// connKey keys a request's connection in its context.
type connKey struct{}

// limitConn is a connection the limitListener counts. Until it has made an
// authenticated request, it is closed at until, unauth after it was
// accepted, whatever it is doing then, and no read or write deadline
// net/http sets on it lies past until: neither requests without the token,
// however often they come, nor answers read slowly keep it open past then.
type limitConn struct {
	net.Conn
	once  sync.Once
	until time.Time

	mu     sync.Mutex
	slot   *login.Slot
	authed bool
	closed bool
	timer  *time.Timer // closes the connection at until unless it is authed
	// wantRead and wantWrite are the deadlines net/http last set.
	wantRead, wantWrite time.Time
}

func (c *limitConn) Close() error {
	c.mu.Lock()
	c.closed = true
	if c.timer != nil {
		c.timer.Stop()
	}
	slot := c.slot
	c.mu.Unlock()
	err := c.Conn.Close()
	c.once.Do(slot.Release)
	return err
}

// expire closes the connection unless it has authenticated: its time is up.
func (c *limitConn) expire() {
	if !c.authenticated() {
		_ = c.Close()
	}
}

// bound returns t, or until when the connection has not authenticated and
// t lies past it or is none; c.mu is held.
func (c *limitConn) bound(t time.Time) time.Time {
	if !c.authed && (t.IsZero() || t.After(c.until)) {
		return c.until
	}
	return t
}

func (c *limitConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wantRead = t
	return c.Conn.SetReadDeadline(c.bound(t))
}

func (c *limitConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wantWrite = t
	return c.Conn.SetWriteDeadline(c.bound(t))
}

func (c *limitConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}

// authenticated reports whether the connection has made an authenticated
// request.
func (c *limitConn) authenticated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authed
}

// authenticate marks the connection as having made an authenticated
// request: it is no longer closed at until, nor for a newer connection, and
// the deadlines net/http sets apply as they are from then on, beginning
// with those it set last.
func (c *limitConn) authenticate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authed || c.closed {
		return
	}
	c.authed = true
	c.timer.Stop()
	c.slot.Authenticate()
	_ = c.Conn.SetReadDeadline(c.wantRead)
	_ = c.Conn.SetWriteDeadline(c.wantWrite)
}
