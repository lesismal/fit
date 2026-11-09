package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// esIndex is the index every mover writes to in the mysql-es scenario,
// made beforehand so that each finds the same settings and mapping: one
// shard, no replica (one node), refreshed every second.
const esIndex = `{"settings": {"number_of_shards": 1, "number_of_replicas": 0, "refresh_interval": "1s"},
 "mappings": {"properties": {"id": {"type": "long"}, "k": {"type": "integer"},
  "c": {"type": "keyword"}, "pad": {"type": "keyword"}}}}`

// prepare readies the scenario before its mover starts: the files
// scenario's directories and its lines; the source's table (and a batch
// job's rows and target table); the index.
func prepare(ctx context.Context, o *options) error {
	switch o.sc.source {
	case "files":
		for _, d := range []string{o.inDir, o.outDir} {
			if err := emptyDir(d); err != nil {
				return err
			}
		}
		// The movers run as users of their own (Filebeat's is 1000).
		if err := os.Chmod(o.outDir, 0o777); err != nil {
			return err
		}
		src := &fileSource{dir: o.inDir, files: o.files, size: o.lineBytes}
		return src.write(ctx, 0, o.records)
	case "pg":
		c, err := pgDial(ctx, o.pg, o.pgUser, o.pgPassword, o.pgDB)
		if err != nil {
			return err
		}
		defer c.close()
		if err := c.exec(ctx, "DROP TABLE IF EXISTS items; "+pgSchema); err != nil {
			return err
		}
	default:
		if err := mysqlTable(ctx, o, o.mysql); err != nil {
			return err
		}
	}
	switch o.sc.sink {
	case "es":
		return esCreate(ctx, strings.TrimRight(o.es, "/")+"/"+o.esIndex)
	case "mysql":
		if err := mysqlTable(ctx, o, o.target); err != nil {
			return err
		}
		conns, err := dialPool(ctx, o.writers, func(ctx context.Context) (execer, error) {
			return mysqlDial(ctx, o.mysql, o.mysqlUser, o.mysqlPassword)
		})
		if err != nil {
			return err
		}
		defer func() {
			for _, c := range conns {
				c.close()
			}
		}()
		return bulkInsert(ctx, conns, "shop.items", o.records)
	}
	return nil
}

// mysqlTable makes shop.items afresh on the server at addr.
func mysqlTable(ctx context.Context, o *options, addr string) error {
	c, err := mysqlDial(ctx, addr, o.mysqlUser, o.mysqlPassword)
	if err != nil {
		return err
	}
	defer c.close()
	for _, q := range []string{"CREATE DATABASE IF NOT EXISTS shop", "DROP TABLE IF EXISTS shop.items", mysqlSchema} {
		if err := c.exec(ctx, q); err != nil {
			return fmt.Errorf("%s: %s: %w", addr, q, err)
		}
	}
	return nil
}

// emptyDir makes dir, or removes what it holds.
func emptyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// esCreate makes the index at url afresh.
func esCreate(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	for _, step := range []struct {
		method, body string
		ok           func(int) bool
	}{
		{http.MethodDelete, "", func(s int) bool { return s == http.StatusOK || s == http.StatusNotFound }},
		{http.MethodPut, esIndex, func(s int) bool { return s == http.StatusOK }},
	} {
		req, err := http.NewRequestWithContext(ctx, step.method, url, strings.NewReader(step.body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if !step.ok(resp.StatusCode) {
			return fmt.Errorf("%s %s: %s: %s", step.method, url, resp.Status, strings.TrimSpace(string(body)))
		}
	}
	return nil
}
