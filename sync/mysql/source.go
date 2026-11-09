package syncmysql

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".mysql", New: func() any { return new(Source) },
		Doc: "Follows MySQL (5.7 and later) or MariaDB (10.11 and later) as a replica: a snapshot of the tables read by key without locks, then the binlog in row format, read with COM_BINLOG_DUMP_GTID (or MariaDB's GTID position, or a file and offset) from the checkpoint, else from now. Each row inserted, updated or deleted is a record of namespace db.table, its key the primary key's columns, a DDL a ddl record; the records of a transaction carry its GTID and boundaries, and the checkpoint, the GTID set and the file and offset at a transaction's end, never falls inside one. A position the server no longer has (binlogs purged) is an error saying a new snapshot is needed. In a job (mode once) the snapshot is split into ranges of each table's key, read job.max_parallel at a time."})
}

// The defaults of the source's params.
const (
	DefaultHeartbeat = 10 * time.Second
)

// The source's retries of a stream cut by a lost connection, from the last
// transaction it handed on, before the error is the pipeline's, which
// restarts it from its checkpoint; and how often it asks the server how far
// its binlog runs ahead.
var (
	maxRetries   = 5
	retryBackoff = 100 * time.Millisecond
	lagInterval  = 10 * time.Second
)

// systemDBs are the databases the source never reads.
var systemDBs = []string{"mysql", "sys", "information_schema", "performance_schema"}

// Source is the source mysql.
type Source struct {
	Address             string              `json:"address,omitempty" default:"\"127.0.0.1:3306\"" doc:"The server's host and port: MySQL 5.7 or later, or MariaDB 10.11 or later, with the binlog on (log_bin), binlog_format ROW and binlog_row_image FULL. 127.0.0.1:3306 when left out."`
	Username            string              `json:"username" fit:"required" doc:"The user to log in as, which needs REPLICATION SLAVE, REPLICATION CLIENT and SELECT on the tables read (information_schema tells the source the columns' names and keys)."`
	PasswordEnv         string              `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	TLS                 bool                `json:"tls,omitempty" doc:"Connect over TLS, required: a server that does not offer it is refused, never spoken to in plain text. The server's certificate is checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile              string              `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own; needs tls."`
	CertFile            string              `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs tls and key_file."`
	KeyFile             string              `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName          string              `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; address's host when left out."`
	TLSRSA              bool                `json:"tls_rsa,omitempty" doc:"Offer TLS's RSA key exchange besides the default suites, for MySQL 5.7's image, which offers no other that Go has; needs tls. false when left out."`
	ServerPublicKeyFile string              `json:"server_public_key_file,omitempty" doc:"A PEM file of the server's RSA public key, with which caching_sha2_password and sha256_password send the password without TLS. Without it and without TLS, a user of those plugins logs in only through the server's cache."`
	Timeout             config.Duration     `json:"timeout,omitempty" default:"\"60s\"" doc:"How long connecting, and each wait for the server, may take; above heartbeat. 60s when left out or 0."`
	ServerID            uint32              `json:"server_id" fit:"required" doc:"The replica's server ID, unique among the server's replicas and the servers of its topology: a second replica connecting with the same ID cuts the first one off. Not 0."`
	Tables              []string            `json:"tables,omitempty" doc:"The tables read, as db.table globs (* and ? within a name, [ranges]): shop.orders, shop.*. Every table of every database but mysql, sys, information_schema and performance_schema when left out."`
	Exclude             []string            `json:"exclude,omitempty" doc:"Tables left out of those tables names, as db.table globs."`
	Keys                map[string][]string `json:"keys,omitempty" doc:"The key's columns of tables, by db.table, in place of the primary key: for a table without one, or to key by a unique key. A table with neither key nor primary key has records without a key."`
	Heartbeat           config.Duration     `json:"heartbeat,omitempty" default:"\"10s\"" doc:"How long the server waits, when it has nothing to send, before it sends a heartbeat, which tells the source that it is caught up and the connection alive; below timeout. 10s when left out or 0."`
	Snapshot            string              `json:"snapshot,omitempty" enum:"initial,always,never" default:"\"initial\"" doc:"Whether the tables' rows are read before the binlog: initial, when the pipeline has no checkpoint; always, at every start of the source, unless its checkpoint is inside a snapshot, which it goes on with; never, the binlog alone, from the checkpoint or from now. The binlog's position is read first and the stream starts there, so what changes during the snapshot comes again. initial when left out."`
	BatchSize           int                 `json:"batch_size,omitempty" min:"0" max:"100000" default:"1000" doc:"The rows a snapshot's query reads, by key, each in a transaction of its own with a consistent snapshot. 1000 when left out or 0."`
	ChunkRows           int64               `json:"chunk_rows,omitempty" min:"0" default:"100000" doc:"In a job (mode once), the rows of a chunk of the snapshot: each table is split into ranges of its key this many rows each, read job.max_parallel at a time. 100000 when left out or 0."`

	cfg  mysqlwire.Config
	dial func(context.Context, mysqlwire.Config) (srcConn, error)
	log  *logs.Logger
}

// srcConn is what the source asks of a connection: *mysqlwire.Conn
// through wireSrc, or a fake in the tests.
type srcConn interface {
	Exec(ctx context.Context, query string, args ...any) (mysqlwire.Result, error)
	// QueryAll returns query's columns and rows, the values copied.
	QueryAll(ctx context.Context, query string, args ...any) ([]mysqlwire.Column, [][]any, error)
	Command(ctx context.Context, cmd byte, body []byte) error
	StartBinlog(ctx context.Context, cmd byte, body []byte) error
	ReadEvent(ctx context.Context) ([]byte, error)
	ServerVersion() string
	IsMariaDB() bool
	Close() error
}

type wireSrc struct{ *mysqlwire.Conn }

func (c wireSrc) QueryAll(ctx context.Context, query string, args ...any) ([]mysqlwire.Column, [][]any, error) {
	rows, err := c.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(rows.Values()))
		for i, v := range rows.Values() {
			if b, ok := v.([]byte); ok {
				v = bytes.Clone(b)
			}
			vals[i] = v
		}
		out = append(out, vals)
	}
	cols := rows.Columns()
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	return cols, out, nil
}

func dialSrc(ctx context.Context, cfg mysqlwire.Config) (srcConn, error) {
	c, err := mysqlwire.Dial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return wireSrc{c}, nil
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := wireConfig(ctx, connParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv,
		TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, TLSRSA: s.TLSRSA,
		ServerPublicKeyFile: s.ServerPublicKeyFile, Timeout: s.Timeout})
	s.cfg = cfg
	if s.dial == nil {
		s.dial = dialSrc
	}
	s.log = logs.Named(config.LoggerSync)
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if s.ServerID == 0 {
		add("server_id", "server_id may not be 0")
	}
	for name, globs := range map[string][]string{"tables": s.Tables, "exclude": s.Exclude} {
		for i, g := range globs {
			if _, err := path.Match(g, ""); err != nil || strings.Count(g, ".") != 1 {
				errs = append(errs, ctx.Params.Field(name).Index(i).Errorf("%q is not a db.table glob", g))
			}
		}
	}
	for ns, cols := range s.Keys {
		if strings.Count(ns, ".") != 1 || len(cols) == 0 {
			add("keys", "%q: a key is a db.table and its columns", ns)
		}
	}
	if s.heartbeat() >= cfg.Timeout {
		add("heartbeat", "heartbeat %v is not below timeout %v", s.heartbeat(), cfg.Timeout)
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func (s *Source) heartbeat() time.Duration {
	if s.Heartbeat <= 0 {
		return DefaultHeartbeat
	}
	return s.Heartbeat.Std()
}

// included reports whether the source reads table ns, db.table.
func (s *Source) included(ns string) bool {
	db, _, _ := strings.Cut(ns, ".")
	if len(s.Tables) == 0 && isSystemDB(db) {
		return false
	}
	ok := len(s.Tables) == 0
	for _, g := range s.Tables {
		if m, _ := path.Match(g, ns); m {
			ok = true
			break
		}
	}
	for _, g := range s.Exclude {
		if m, _ := path.Match(g, ns); m {
			return false
		}
	}
	return ok
}

// includesDB reports whether the source may read a table of db, for a DDL
// whose table is not known.
func (s *Source) includesDB(db string) bool {
	if db == "" {
		return false
	}
	if len(s.Tables) == 0 {
		return !isSystemDB(db)
	}
	for _, g := range s.Tables {
		gdb, _, _ := strings.Cut(g, ".")
		if m, _ := path.Match(gdb, db); m {
			return true
		}
	}
	return false
}

func isSystemDB(db string) bool {
	for _, d := range systemDBs {
		if strings.EqualFold(d, db) {
			return true
		}
	}
	return false
}

// Test connects and checks that the server writes a binlog the source can
// read.
func (s *Source) Test(ctx context.Context) error {
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = checkServer(ctx, c)
	return err
}

// server is what the source learned of the server it reads.
type server struct {
	mariadb bool
	// gtid says the server has GTIDs: MariaDB always, MySQL with
	// gtid_mode ON.
	gtid     bool
	checksum bool
	// major and minor are the version's.
	major, minor int
}

// str is a value of a result as text, "" for NULL.
func str(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// queryRow returns the first row of query's result.
func queryRow(ctx context.Context, c srcConn, query string, args ...any) ([]any, error) {
	_, rows, err := c.QueryAll(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("mysql: %s: %w", query, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("mysql: %s: no row", query)
	}
	return rows[0], nil
}

// checkServer checks the binlog's settings: on, in row format, with full
// row images.
func checkServer(ctx context.Context, c srcConn) (server, error) {
	srv := server{mariadb: c.IsMariaDB()}
	v := c.ServerVersion()
	maj, rest, _ := strings.Cut(v, ".")
	mi, _, _ := strings.Cut(rest, ".")
	srv.major, _ = strconv.Atoi(maj)
	srv.minor, _ = strconv.Atoi(mi)
	row, err := queryRow(ctx, c, "SELECT @@GLOBAL.log_bin, @@GLOBAL.binlog_format, @@GLOBAL.binlog_row_image, @@GLOBAL.binlog_checksum")
	if err != nil {
		return srv, err
	}
	logBin, format, image, checksum := str(row[0]), str(row[1]), str(row[2]), str(row[3])
	switch {
	case logBin != "1" && !strings.EqualFold(logBin, "ON"):
		return srv, errors.New("mysql: the server's binlog is off (log_bin): there is nothing to follow")
	case !strings.EqualFold(format, "ROW"):
		return srv, fmt.Errorf("mysql: the server's binlog_format is %s, not ROW, which the source needs to read the rows changed", format)
	case !strings.EqualFold(image, "FULL"):
		return srv, fmt.Errorf("mysql: the server's binlog_row_image is %s, not FULL, which the source needs for whole rows", image)
	}
	srv.checksum = strings.EqualFold(checksum, "CRC32")
	srv.gtid = srv.mariadb
	if !srv.mariadb {
		row, err := queryRow(ctx, c, "SELECT @@GLOBAL.gtid_mode")
		if err != nil {
			return srv, err
		}
		srv.gtid = strings.EqualFold(str(row[0]), "ON")
	}
	return srv, nil
}

// position is the source's checkpoint: the GTID set (MySQL's, or MariaDB's
// position) and the binlog file and offset at a transaction's end, from
// which the stream resumes, by GTIDs where the server has them.
type position struct {
	GTIDs string `json:"gtids,omitempty"`
	File  string `json:"file,omitempty"`
	Pos   uint64 `json:"pos,omitempty"`
	// Snapshot is where a snapshot is, while it is read; the position is
	// then the stream's start.
	Snapshot *snapPos `json:"snapshot,omitempty"`
}

func (p position) marshal() fitsync.Position {
	b, _ := json.Marshal(p)
	return b
}

// binlogStatus returns the server's binlog position now: its file and
// offset, and its GTIDs.
func binlogStatus(ctx context.Context, c srcConn, srv server) (position, error) {
	q := "SHOW MASTER STATUS"
	if !srv.mariadb && (srv.major > 8 || srv.major == 8 && srv.minor >= 2) {
		q = "SHOW BINARY LOG STATUS"
	}
	row, err := queryRow(ctx, c, q)
	if err != nil {
		return position{}, err
	}
	p := position{File: str(row[0])}
	p.Pos, _ = strconv.ParseUint(str(row[1]), 10, 64)
	if srv.mariadb {
		row, err := queryRow(ctx, c, "SELECT @@GLOBAL.gtid_binlog_pos")
		if err != nil {
			return position{}, err
		}
		p.GTIDs = str(row[0])
	} else if srv.gtid && len(row) > 4 {
		p.GTIDs = str(row[4])
	}
	if p.GTIDs != "" {
		// Checked, and written as the source writes it.
		if srv.mariadb {
			m, err := parseMariaPos(p.GTIDs)
			if err != nil {
				return position{}, err
			}
			p.GTIDs = m.String()
		} else {
			g, err := parseGTIDSet(p.GTIDs)
			if err != nil {
				return position{}, err
			}
			p.GTIDs = g.String()
		}
	}
	return p, nil
}

// Run reads the snapshot, when the checkpoint is inside one or snapshot
// says so, then the binlog from the checkpoint, from the snapshot's start,
// or from now.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var from *position
	if r.From != nil {
		from = new(position)
		if err := json.Unmarshal(r.From, from); err != nil {
			return fmt.Errorf("mysql: the checkpoint %s: %w", r.From, err)
		}
		if from.File == "" && from.GTIDs == "" {
			return fmt.Errorf("mysql: the checkpoint %s has neither GTIDs nor a file", r.From)
		}
	}
	switch {
	case from != nil && from.Snapshot != nil:
		at := from.Snapshot
		from.Snapshot = nil
		c, _, err := s.snapshotConn(ctx)
		if err != nil {
			return err
		}
		err = s.snapshot(ctx, r, c, *from, at)
		c.Close()
		if err != nil {
			return err
		}
	case from == nil && s.Snapshot != "never" || s.Snapshot == "always":
		c, srv, err := s.snapshotConn(ctx)
		if err != nil {
			return err
		}
		start, err := binlogStatus(ctx, c, srv)
		if err == nil && from == nil {
			// The stream's start with the snapshot not begun is the
			// pipeline's checkpoint before its first record: a run killed
			// before the next reads the snapshot again and streams from
			// this start, replaying the changes since over it, rather than
			// from a later one, which would lose them.
			at := start
			at.Snapshot = &snapPos{}
			err = r.SaveStart(at.marshal())
		}
		if err == nil {
			err = s.snapshot(ctx, r, c, start, nil)
		}
		c.Close()
		if err != nil {
			return err
		}
		from = &start
	}
	st := &streamer{s: s, r: r}
	defer st.close()
	return st.run(ctx, from)
}

// errNeedSnapshot is the error of a position the server no longer has.
var errNeedSnapshot = errors.New("a new snapshot is needed (the pipeline's snapshot, or a reset of its checkpoint)")

// streamer reads the binlog as a replica.
type streamer struct {
	s    *Source
	r    *fitsync.Run
	srv  server
	conn srcConn
	// meta is a second connection, for information_schema and the
	// binlog's size, while conn streams.
	meta srcConn
	dec  *binlog.Decoder

	// at is where the stream is as of the last transaction ended: what a
	// reconnect resumes from and the position of the records.
	at    position
	gtids gtidSet
	maria mariaPos
	// file is the binlog file being read, from the rotates.
	file string

	// The transaction being read.
	inTx, begun bool
	txID        string
	sid         string
	gno         int64
	mariaGTID   *binlog.MariaGTID
	commit      time.Time
	// held is the transaction's last record so far, held until the next
	// tells whether it is the last; n counts the records the transaction
	// made.
	held *fitsync.Record
	n    int
	// After a reconnect inside a transaction, its first skipN records,
	// handed on already, are passed over.
	skipTx string
	skipN  int

	// tables are the columns read from information_schema, by db.table;
	// keys the key's columns of each table map's table, by table ID.
	tables map[string]*tableInfo
	keys   map[uint64][]int
	err    error

	lastLag time.Time
	runCtx  context.Context
}

func (st *streamer) close() {
	if st.conn != nil {
		st.conn.Close()
		st.conn = nil
	}
	if st.meta != nil {
		st.meta.Close()
		st.meta = nil
	}
}

// metaConn returns the second connection, made when first needed.
func (st *streamer) metaConn(ctx context.Context) (srcConn, error) {
	if st.meta == nil {
		c, err := st.s.dial(ctx, st.s.cfg)
		if err != nil {
			return nil, err
		}
		st.meta = c
	}
	return st.meta, nil
}

// dropMeta closes the second connection after an error, so that the next
// use makes a new one.
func (st *streamer) dropMeta() {
	if st.meta != nil {
		st.meta.Close()
		st.meta = nil
	}
}

// setPosition makes p the stream's position.
func (st *streamer) setPosition(p position) error {
	st.at, st.file = p, p.File
	st.gtids, st.maria = nil, nil
	if p.GTIDs == "" {
		if st.srv.mariadb {
			st.maria = mariaPos{}
		} else {
			st.gtids = gtidSet{}
		}
		return nil
	}
	var err error
	if st.srv.mariadb {
		st.maria, err = parseMariaPos(p.GTIDs)
	} else {
		st.gtids, err = parseGTIDSet(p.GTIDs)
	}
	return err
}

func (st *streamer) run(ctx context.Context, from *position) error {
	st.runCtx = ctx
	failures := 0
	for {
		err := st.connect(ctx, from)
		if err == nil {
			from = nil
			err = st.follow(ctx, &failures)
		}
		if err == nil || ctx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		var se *mysqlwire.Error
		if errors.As(err, &se) || errors.Is(err, errNeedSnapshot) || !isConnErr(err) || failures >= maxRetries {
			return err
		}
		failures++
		st.s.log.Warnf("sync mysql: the binlog's stream is cut (%v): reading it again from %s", err, st.at.marshal())
		st.resetTx()
		if st.conn != nil {
			st.conn.Close()
			st.conn = nil
		}
		t := time.NewTimer(retryBackoff << (failures - 1))
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// connErr marks an error of the connection, after which the stream is read
// again from where it was.
type connErr struct{ err error }

func (e *connErr) Error() string { return e.err.Error() }
func (e *connErr) Unwrap() error { return e.err }

func isConnErr(err error) bool {
	var ce *connErr
	return errors.As(err, &ce)
}

// resetTx forgets the transaction being read, after a stream cut inside
// it: the records it handed on already are passed over when it is read
// again.
func (st *streamer) resetTx() {
	if st.inTx {
		emitted := st.n
		if st.held != nil {
			emitted--
		}
		if st.skipTx == st.txID {
			emitted = max(emitted, st.skipN)
		}
		st.skipTx, st.skipN = st.txID, emitted
	}
	st.inTx, st.begun, st.txID, st.held, st.n, st.mariaGTID, st.sid = false, false, "", nil, 0, nil, ""
}

// connect connects, checks the server, and starts the stream from from (the
// checkpoint, the first time), from where the stream was, or from now.
func (st *streamer) connect(ctx context.Context, from *position) error {
	c, err := st.s.dial(ctx, st.s.cfg)
	if err != nil {
		return &connErr{err}
	}
	st.conn = c
	if st.srv, err = checkServer(ctx, c); err != nil {
		return err
	}
	switch {
	case from != nil:
		if err := st.setPosition(*from); err != nil {
			return fmt.Errorf("mysql: the checkpoint: %w", err)
		}
	case st.gtids == nil && st.maria == nil:
		p, err := binlogStatus(ctx, c, st.srv)
		if err != nil {
			return err
		}
		if err := st.setPosition(p); err != nil {
			return err
		}
	}
	st.dec = binlog.NewDecoder()
	st.dec.SetChecksum(st.srv.checksum)
	st.dec.OnTableMap = st.onTableMap
	st.tables, st.keys = map[string]*tableInfo{}, map[uint64][]int{}
	return st.startDump(ctx)
}

// The flags of the dump commands.
const (
	dumpNonBlock    = 0x01
	dumpThroughGTID = 0x04
)

// startDump registers as a replica and asks for the binlog from st.at.
func (st *streamer) startDump(ctx context.Context) error {
	c := st.conn
	hb := st.s.heartbeat().Nanoseconds()
	set := fmt.Sprintf("SET @master_binlog_checksum = @@GLOBAL.binlog_checksum, @source_binlog_checksum = @@GLOBAL.binlog_checksum, "+
		"@master_heartbeat_period = %d, @source_heartbeat_period = %d", hb, hb)
	if st.srv.mariadb {
		set += ", @mariadb_slave_capability = 4"
	}
	if _, err := c.Exec(ctx, set); err != nil {
		return err
	}
	var flags uint16
	if st.r.Once {
		flags |= dumpNonBlock
	}
	useGTID := st.srv.gtid && st.at.GTIDs != "" && !(st.gtids != nil && st.gtids.tagged())
	if useGTID && st.srv.mariadb {
		if _, err := c.Exec(ctx, "SET @slave_connect_state = '"+st.maria.String()+"', @slave_gtid_strict_mode = 0, @slave_gtid_ignore_duplicates = 0"); err != nil {
			return err
		}
	}
	reg := binary.LittleEndian.AppendUint32(nil, st.s.ServerID)
	reg = append(reg, 0, 0, 0)    // host, user and password, empty
	reg = append(reg, 0, 0)       // port
	reg = append(reg, 0, 0, 0, 0) // rank
	reg = append(reg, 0, 0, 0, 0) // the source's ID
	if err := c.Command(ctx, mysqlwire.ComRegisterSlave, reg); err != nil {
		return wrapConn(err)
	}
	var err error
	switch {
	case useGTID && !st.srv.mariadb:
		b := binary.LittleEndian.AppendUint16(nil, flags|dumpThroughGTID)
		b = binary.LittleEndian.AppendUint32(b, st.s.ServerID)
		b = binary.LittleEndian.AppendUint32(b, 0) // no file
		b = binary.LittleEndian.AppendUint64(b, 4)
		data := st.gtids.encode()
		b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
		err = c.StartBinlog(ctx, mysqlwire.ComBinlogDumpGTID, append(b, data...))
	default:
		file, pos := st.at.File, st.at.Pos
		if useGTID { // MariaDB: the server starts from @slave_connect_state.
			file, pos = "", 4
		} else if file == "" {
			return fmt.Errorf("mysql: the position %s names no binlog file, and the server cannot start from its GTIDs", st.at.marshal())
		}
		b := binary.LittleEndian.AppendUint32(nil, uint32(max(pos, 4)))
		b = binary.LittleEndian.AppendUint16(b, flags)
		b = binary.LittleEndian.AppendUint32(b, st.s.ServerID)
		err = c.StartBinlog(ctx, mysqlwire.ComBinlogDump, append(b, file...))
	}
	return wrapConn(err)
}

// wrapConn marks err as the connection's, unless the server answered it.
func wrapConn(err error) error {
	var se *mysqlwire.Error
	if err == nil || errors.As(err, &se) {
		return err
	}
	return &connErr{err}
}

// The server's error of a position it does not have.
const errCodeFatalReading = 1236

// follow reads the stream's events until an error, or, in a job, its end.
func (st *streamer) follow(ctx context.Context, failures *int) error {
	for {
		b, err := st.conn.ReadEvent(ctx)
		if err == io.EOF {
			if st.inTx {
				return errors.New("mysql: the binlog's stream ended inside a transaction")
			}
			return nil
		}
		if err != nil {
			var se *mysqlwire.Error
			if errors.As(err, &se) && se.Code == errCodeFatalReading {
				return fmt.Errorf("mysql: the server cannot send the binlog from %s, which it may have purged: %w: %w", st.at.marshal(), errNeedSnapshot, err)
			}
			return wrapConn(err)
		}
		ev, err := st.dec.Decode(b)
		if err == nil {
			err = st.err
		}
		if err != nil {
			return fmt.Errorf("mysql: the binlog %s: %w", st.file, err)
		}
		if err := st.event(ctx, ev, ev.Header); err != nil {
			return err
		}
		*failures = 0
	}
}

// event handles ev, an event of the stream or of a transaction payload,
// whose outer event's header is outer.
func (st *streamer) event(ctx context.Context, ev *binlog.Event, outer binlog.Header) error {
	switch b := ev.Body.(type) {
	case *binlog.Rotate:
		st.file = b.NextFile
		if outer.LogPos == 0 && !st.inTx {
			// The artificial rotate that starts a stream names the file
			// and the offset it starts at, which a stream started by
			// GTIDs did not know.
			st.at.File, st.at.Pos = b.NextFile, b.Position
		}
	case *binlog.Heartbeat:
		if !st.inTx {
			st.r.ReportLag(0, fitsync.LagBytes)
			st.lastLag = time.Now()
		}
	case *binlog.GTID:
		st.begin(ev.Header, outer)
		if !b.Anonymous {
			st.txID = b.String()
			st.sid, st.gno = strings.ToLower(binlog.FormatUUID(b.SID)), b.GNO
			if b.Tag != "" {
				st.sid += ":" + strings.ToLower(b.Tag)
			}
		}
		if b.OriginalCommitTimestamp != 0 {
			st.commit = time.UnixMicro(int64(b.OriginalCommitTimestamp)).UTC()
		}
	case *binlog.MariaGTID:
		st.begin(ev.Header, outer)
		st.txID, st.mariaGTID = b.String(), b
		st.begun = !b.Standalone()
	case *binlog.Query:
		return st.query(ctx, b, ev.Header, outer)
	case *binlog.Rows:
		return st.rows(b)
	case *binlog.XID:
		return st.endTx(ctx, outer)
	case *binlog.Payload:
		for _, e := range b.Events {
			if err := st.event(ctx, e, outer); err != nil {
				return err
			}
		}
	}
	return nil
}

// begin starts a transaction at an event of header h.
func (st *streamer) begin(h, outer binlog.Header) {
	st.inTx, st.begun, st.mariaGTID, st.sid = true, false, nil, ""
	st.txID = st.file + ":" + strconv.FormatUint(uint64(outer.LogPos)-uint64(outer.Size), 10)
	st.commit = time.Unix(int64(h.Timestamp), 0).UTC()
	st.held, st.n = nil, 0
}

// query handles a statement: BEGIN, COMMIT, or a DDL.
func (st *streamer) query(ctx context.Context, q *binlog.Query, h, outer binlog.Header) error {
	sql := strings.TrimSpace(q.SQL)
	word := strings.ToUpper(firstWord(sql))
	switch word {
	case "BEGIN":
		if !st.inTx {
			st.begin(h, outer)
		}
		st.begun = true
		return nil
	case "COMMIT":
		return st.endTx(ctx, outer)
	case "ROLLBACK":
		if strings.Contains(strings.ToUpper(sql), " TO ") {
			return nil
		}
		return st.endTx(ctx, outer)
	case "SAVEPOINT", "XA", "RELEASE", "#", "":
		return nil
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "LOAD":
		if st.s.includesDB(q.Schema) {
			return fmt.Errorf("mysql: a statement-based change in the binlog (%.80s): every change must be logged in row format", sql)
		}
		return nil
	}
	if !isDDL(word) {
		return nil
	}
	// A DDL changes what information_schema says.
	clear(st.tables)
	alone := !st.inTx || !st.begun
	if !st.inTx {
		st.begin(h, outer)
	}
	ns, ok := ddlTable(sql, q.Schema)
	if ok && st.s.included(ns) || !ok && st.s.includesDB(q.Schema) {
		if !ok {
			ns = q.Schema
		}
		rec := &fitsync.Record{Source: "mysql", Namespace: ns, Op: fitsync.OpDDL,
			After: fitsync.Map(fitsync.Field{Name: "statement", Value: fitsync.String(sql)}, fitsync.Field{Name: "database", Value: fitsync.String(q.Schema)})}
		if err := st.push(rec); err != nil {
			return err
		}
	}
	if alone {
		return st.endTx(ctx, outer)
	}
	return nil
}

func firstWord(s string) string {
	if strings.HasPrefix(s, "#") || strings.HasPrefix(s, "/*") {
		return "#"
	}
	i := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '(' || r == ';' })
	if i < 0 {
		return s
	}
	return s[:i]
}

func isDDL(word string) bool {
	switch word {
	case "CREATE", "ALTER", "DROP", "RENAME", "TRUNCATE":
		return true
	}
	return false
}

// push hands on the record held, and holds rec, of the transaction being
// read.
func (st *streamer) push(rec *fitsync.Record) error {
	st.n++
	if st.skipTx == st.txID && st.n <= st.skipN {
		return nil
	}
	rec.CommitTime = st.commit
	rec.Tx = fitsync.Tx{ID: st.txID, First: st.n == 1}
	// The transaction's GTID, or its binlog file and offset, and the
	// record's place in it.
	rec.Origin = fitsync.Origin{Stream: st.txID, N: [4]uint64{uint64(st.n)}}
	if h := st.held; h != nil {
		st.held = nil
		if err := st.r.Emit(h); err != nil {
			return err
		}
	}
	st.held = rec
	return nil
}

// endTx ends the transaction: its GTID added to the position, and its last
// record handed on with the position.
func (st *streamer) endTx(ctx context.Context, outer binlog.Header) error {
	if st.sid != "" && st.gtids != nil {
		st.gtids.add(st.sid, st.gno)
		st.at.GTIDs = st.gtids.String()
	}
	if g := st.mariaGTID; g != nil && st.maria != nil {
		st.maria[g.Domain] = g.String()
		st.at.GTIDs = st.maria.String()
	}
	if outer.LogPos != 0 {
		st.at.File, st.at.Pos = st.file, uint64(outer.LogPos)
	}
	h := st.held
	if st.skipTx == st.txID {
		st.skipTx, st.skipN = "", 0
	}
	st.inTx, st.begun, st.txID, st.held, st.n, st.mariaGTID, st.sid = false, false, "", nil, 0, nil, ""
	if h != nil {
		h.Tx.Last = true
		h.Position = st.at.marshal()
		if err := st.r.Emit(h); err != nil {
			return err
		}
	}
	st.lag(ctx)
	return nil
}

// lag tells the pipeline, every lagInterval, how many bytes of binlog the
// server has past the stream's position, from SHOW BINARY LOGS on the
// second connection.
func (st *streamer) lag(ctx context.Context) {
	if st.r.Lag == nil || time.Since(st.lastLag) < lagInterval {
		return
	}
	st.lastLag = time.Now()
	c, err := st.metaConn(ctx)
	if err != nil {
		return
	}
	_, rows, err := c.QueryAll(ctx, "SHOW BINARY LOGS")
	if err != nil {
		st.s.log.Debugf("sync mysql: the binlog's size: %v", err)
		st.dropMeta()
		return
	}
	var behind int64
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		name := str(row[0])
		size, _ := strconv.ParseInt(str(row[1]), 10, 64)
		switch {
		case name == st.at.File:
			behind += size - int64(st.at.Pos)
		case name > st.at.File:
			behind += size
		}
	}
	st.r.ReportLag(behind, fitsync.LagBytes)
}

// rows makes the records of a rows event of a table read.
func (st *streamer) rows(b *binlog.Rows) error {
	if b.Table == nil {
		return nil
	}
	key, ok := st.keys[b.TableID]
	if !ok {
		return nil // not a table read
	}
	tm := b.Table
	ns := tm.Schema + "." + tm.Table
	for i := range b.Present {
		if !b.Present[i] || b.Kind == binlog.Update && (i >= len(b.PresentAfter) || !b.PresentAfter[i]) {
			return fmt.Errorf("mysql: a rows event of %s without every column: binlog_row_image must be FULL, also in the sessions that write", ns)
		}
	}
	if !st.inTx {
		return fmt.Errorf("mysql: a rows event of %s outside a transaction", ns)
	}
	for _, row := range b.Rows {
		before, after := rowValue(tm, row.Before), rowValue(tm, row.After)
		var recs []*fitsync.Record
		switch b.Kind {
		case binlog.Insert:
			recs = append(recs, &fitsync.Record{Op: fitsync.OpInsert, Key: keyValue(tm, key, row.After), After: after})
		case binlog.Delete:
			recs = append(recs, &fitsync.Record{Op: fitsync.OpDelete, Key: keyValue(tm, key, row.Before), Before: before})
		default:
			oldKey, newKey := keyValue(tm, key, row.Before), keyValue(tm, key, row.After)
			if oldKey.Equal(newKey) {
				recs = append(recs, &fitsync.Record{Op: fitsync.OpUpdate, Key: newKey, Before: before, After: after})
			} else {
				// A key changed: the old row deleted, the new one
				// inserted, so that a sink by key does not keep both.
				recs = append(recs, &fitsync.Record{Op: fitsync.OpDelete, Key: oldKey, Before: before},
					&fitsync.Record{Op: fitsync.OpInsert, Key: newKey, After: after})
			}
		}
		for _, rec := range recs {
			rec.Source, rec.Namespace = "mysql", ns
			if err := st.push(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

// rowValue is a row as a map of its columns.
func rowValue(tm *binlog.TableMap, vals []fitsync.Value) fitsync.Value {
	if vals == nil {
		return fitsync.Null()
	}
	f := make([]fitsync.Field, len(vals))
	for i, v := range vals {
		f[i] = fitsync.Field{Name: tm.Columns[i].Name, Value: v}
	}
	return fitsync.Map(f...)
}

// keyValue is a row's key, the map of the key's columns; null for a table
// without one.
func keyValue(tm *binlog.TableMap, key []int, vals []fitsync.Value) fitsync.Value {
	if len(key) == 0 {
		return fitsync.Null()
	}
	f := make([]fitsync.Field, len(key))
	for i, k := range key {
		f[i] = fitsync.Field{Name: tm.Columns[k].Name, Value: vals[k]}
	}
	return fitsync.Map(f...)
}
