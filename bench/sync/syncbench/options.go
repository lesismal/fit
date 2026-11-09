package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// A scenario is a source and a sink: what the mover reads and where it
// writes.
type scenario struct {
	source, sink string
	// stream says whether the mover follows its source for good, so that
	// records are written after it starts; otherwise they are there before
	// it starts (files, a batch job).
	stream bool
}

var scenarios = map[string]scenario{
	"files":       {source: "files", sink: "files"},
	"mysql-es":    {source: "mysql", sink: "es", stream: true},
	"mysql-kafka": {source: "mysql", sink: "kafka", stream: true},
	"pg-kafka":    {source: "pg", sink: "kafka", stream: true},
	"mysql-batch": {source: "mysql", sink: "mysql"},
}

// options are prepare's and measure's flags.
type options struct {
	scenario string
	sc       scenario
	mover    string
	variant  string

	// The source.
	mysql, mysqlUser, mysqlPassword string
	pg, pgUser, pgPassword, pgDB    string
	inDir                           string
	files, lineBytes                int
	records, txRows                 int
	writers                         int

	// The sink.
	outDir, es, esIndex, kafka, kafkaTopic, kafkaFormat, target string

	// The measurement.
	containers    []string
	docker        string
	pause, start  bool
	steadyRate    int
	steadyFor     time.Duration
	interval      time.Duration
	timeout       time.Duration
	pprof         string
	profileFor    time.Duration
	profileDir    string
	out           string
	startupFor    time.Duration
	settle        time.Duration
	dockerTimeout time.Duration
}

func parseOptions(cmd string, args []string, stderr io.Writer) (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet("syncbench "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.scenario, "scenario", "", "files, mysql-es, mysql-kafka, pg-kafka or mysql-batch")
	fs.StringVar(&o.mover, "mover", "", "the mover's name in the results")
	fs.StringVar(&o.variant, "variant", "", "the scenario's variant, as its results name it")

	fs.StringVar(&o.mysql, "mysql", "mysql:3306", "the source MySQL's address")
	fs.StringVar(&o.mysqlUser, "mysql-user", "root", "")
	fs.StringVar(&o.mysqlPassword, "mysql-password", "", "")
	fs.StringVar(&o.pg, "pg", "postgres:5432", "the source PostgreSQL's address")
	fs.StringVar(&o.pgUser, "pg-user", "postgres", "")
	fs.StringVar(&o.pgPassword, "pg-password", "", "")
	fs.StringVar(&o.pgDB, "pg-db", "shop", "")
	fs.StringVar(&o.inDir, "in", "/data/in", "the directory the files scenario's files are written to")
	fs.IntVar(&o.files, "files", 1, "the files the lines are spread over")
	fs.IntVar(&o.lineBytes, "line-bytes", 128, "each line's size, its newline included")
	fs.IntVar(&o.records, "records", 100000, "the records (lines, rows) the mover moves, measured")
	fs.IntVar(&o.txRows, "tx-rows", 10, "rows a source transaction, each its own INSERT")
	fs.IntVar(&o.writers, "writers", 8, "connections writing the measured rows at once")

	fs.StringVar(&o.outDir, "out-dir", "/data/out", "the directory the files sink writes to")
	fs.StringVar(&o.es, "es", "http://es:9200", "Elasticsearch's URL")
	fs.StringVar(&o.esIndex, "es-index", "bench", "the index the movers write to")
	fs.StringVar(&o.kafka, "kafka", "kafka:9092", "the Kafka broker")
	fs.StringVar(&o.kafkaTopic, "kafka-topic", "bench", "the topic the mover writes to")
	fs.StringVar(&o.kafkaFormat, "kafka-format", "", "canal for a topic of Canal's flat messages, its rows counted; a message a record otherwise")
	fs.StringVar(&o.target, "target", "mysql2:3306", "the batch job's target MySQL")

	containers := fs.String("containers", "", "the mover's containers, comma-separated, measured and paused together")
	fs.StringVar(&o.docker, "docker", "unix:///var/run/docker.sock", "the Docker API: unix://path or http://host:port")
	fs.BoolVar(&o.pause, "pause", true, "pause the mover while a stream's records are written, so that it reads them as a backlog")
	fs.BoolVar(&o.start, "start", false, "start the mover's containers, made but not started, once the sink is counted: for the files and batch scenarios, whose records are there before")
	fs.IntVar(&o.steadyRate, "steady-rate", 1000, "records a second of the steady phase, which measures the lag; 0 for none")
	fs.DurationVar(&o.steadyFor, "steady-for", 10*time.Second, "how long the steady phase writes")
	fs.DurationVar(&o.interval, "interval", 100*time.Millisecond, "how often the sink is counted and the containers sampled")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Minute, "how long a phase may take before the run fails")
	fs.DurationVar(&o.startupFor, "startup-timeout", 10*time.Minute, "how long the mover may take to deliver the warm-up rows")
	fs.DurationVar(&o.settle, "settle", 3*time.Second, "how long the count is watched after the end, for records counted twice")
	fs.DurationVar(&o.dockerTimeout, "docker-timeout", 10*time.Second, "how long a Docker API call may take")
	fs.StringVar(&o.pprof, "pprof", "", "fit's Admin API, to save its CPU and heap profiles")
	fs.DurationVar(&o.profileFor, "profile-for", 10*time.Second, "how long the CPU profile samples, from the start of the drain")
	fs.StringVar(&o.profileDir, "profile-dir", "/results", "where the profiles are saved")
	fs.StringVar(&o.out, "out", "", "the result's file (JSON); standard output when left out")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	sc, ok := scenarios[o.scenario]
	if !ok {
		fmt.Fprintf(stderr, "syncbench %s: -scenario %q is not one of files, mysql-es, mysql-kafka, pg-kafka, mysql-batch\n", cmd, o.scenario)
		return nil, errUsage
	}
	o.sc = sc
	if *containers != "" {
		o.containers = strings.Split(*containers, ",")
	}
	switch {
	case o.records <= 0, o.files <= 0, o.txRows <= 0, o.writers <= 0, o.steadyRate < 0:
		fmt.Fprintf(stderr, "syncbench %s: -records, -files, -tx-rows and -writers must be positive, -steady-rate not negative\n", cmd)
		return nil, errUsage
	case o.lineBytes < 16:
		fmt.Fprintf(stderr, "syncbench %s: -line-bytes must be 16 or more\n", cmd)
		return nil, errUsage
	case cmd == "measure" && o.mover == "":
		fmt.Fprintf(stderr, "syncbench %s: -mover is required\n", cmd)
		return nil, errUsage
	case o.interval <= 0:
		fmt.Fprintf(stderr, "syncbench %s: -interval must be positive\n", cmd)
		return nil, errUsage
	}
	return o, nil
}

var errUsage = errors.New("usage")
