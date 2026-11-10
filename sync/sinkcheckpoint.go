package fitsync

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// CheckpointSink is a Sink that keeps the pipeline's checkpoint itself,
// written in the same transaction of its server's as the records it covers,
// as the MySQL and PostgreSQL sinks do with checkpoint sink (I191): a crash
// between the sink's commit and the local checkpoint file then replays
// nothing into it, for the run starts from the sink's position.
//
// A checkpoint the sink keeps is used only while it is of the local file's
// lineage, which a reset, a position set by hand and a new job change, so
// that those are not undone by a sink's older position; and only when every
// sink of the pipeline keeps the same position, the local file serving
// otherwise. A job reading a snapshot in chunks uses the local file alone.
type CheckpointSink interface {
	Sink
	// KeepsCheckpoint reports whether the sink keeps the checkpoint, as its
	// params say. It does not block.
	KeepsCheckpoint() bool
	// ReadCheckpoint is called before each run of a pipeline whose sink
	// keeps the checkpoint, on the pipeline's goroutine, and must return
	// once ctx ends. It returns the position the sink keeps for pipeline,
	// and the lineage it was written with, nil and "" when it holds none;
	// from then on, each transaction the sink commits writes, under
	// pipeline, the Position of its last record that ends a source
	// transaction, with lineage. What the sink held from an earlier run
	// and not committed is dropped: the run starts at or before it.
	ReadCheckpoint(ctx context.Context, pipeline, lineage string) (pos Position, kept string, err error)
}

// newLineage returns a lineage nobody had: 16 random bytes in hex.
func newLineage() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand's Read does not fail
	return hex.EncodeToString(b[:])
}

// mintLineage gives a stream's checkpoint a lineage when it has none and
// no position, as when it starts anew or was reset, and saves it at once,
// so that the sinks' names made with it (Record.Lineage) are the same
// after a restart. A checkpoint of a position and no lineage, saved by a
// fit before lineages, keeps none until it is reset, so that the names its
// sinks gave the records are the same after the upgrade.
func (p *pipe) mintLineage(from Position) error {
	if p.store.lineage != "" || from != nil {
		return nil
	}
	p.store.lineage, p.store.wrote = newLineage(), nil
	p.mu.Lock()
	job := p.job
	p.mu.Unlock()
	return p.store.save(from, job)
}

// keepsCheckpoint reports whether a sink of the pipeline keeps the
// checkpoint.
func (p *pipe) keepsCheckpoint() bool {
	for _, s := range p.sinks {
		if c, ok := s.(CheckpointSink); ok && c.KeepsCheckpoint() {
			return true
		}
	}
	return false
}

// sinkCheckpoint returns where a run starts, from being the local file's
// position: the sinks' when every sink keeps the checkpoint, at the same
// position and of the file's lineage, or, with no file at all, of a lineage
// they share. It gives the sinks the lineage they write from then on, the
// file's, or a new one when the file has none.
func (p *pipe) sinkCheckpoint(from Position) (Position, error) {
	if !p.keepsCheckpoint() {
		return from, p.mintLineage(from)
	}
	c, err := p.store.read()
	if err != nil {
		return nil, err
	}
	// A stream whose file is gone, as a state_dir lost, takes the sinks'
	// lineage; a job's file holds its plan, and is not made up.
	adopt := c == nil && !p.cfg.Once()
	lineage := ""
	if c != nil {
		lineage = c.Lineage
	}
	if lineage == "" {
		lineage = newLineage()
	}
	p.store.lineage = lineage
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping := p.stopping
	go func() {
		select {
		case <-stopping:
			cancel()
		case <-ctx.Done():
		}
	}()
	var agreed Position
	shared := ""
	all := true
	for i, s := range p.sinks {
		cs, ok := s.(CheckpointSink)
		if !ok || !cs.KeepsCheckpoint() {
			all = false
			continue
		}
		pos, kept, err := cs.ReadCheckpoint(ctx, p.cfg.Name, lineage)
		if err != nil {
			return nil, fmt.Errorf("reading sink %s's checkpoint: %w", p.cfg.Sinks[i].Name, err)
		}
		if pos == nil || kept == "" || !adopt && kept != lineage || shared != "" && kept != shared {
			all = false
			continue
		}
		compact, err := appendCompact(nil, pos)
		if err != nil {
			return nil, fmt.Errorf("sink %s's checkpoint %q: %w", p.cfg.Sinks[i].Name, pos, err)
		}
		if agreed != nil && !bytes.Equal(agreed, compact) {
			all = false
		}
		agreed, shared = compact, kept
	}
	switch {
	case !all || agreed == nil:
	case c != nil && from != nil && bytes.Equal(p.store.wrote, agreed):
		// The file is past the sinks' position by position-only records
		// alone, which no sink was handed.
	default:
		if cur, err := appendCompact(nil, from); err != nil || !bytes.Equal(cur, agreed) {
			p.rt.opts.Log.Infof("pipeline %s starts from its sinks' checkpoint %s, in preference to its file's", p.cfg.Name, agreed)
			p.store.wrote = nil
		}
		from = agreed
	}
	// A lineage new to the file is saved before the sinks write it, so
	// that a crash before the next checkpoint finds it there.
	if c == nil || c.Lineage != lineage {
		p.mu.Lock()
		job := p.job
		p.mu.Unlock()
		if err := p.store.save(from, job); err != nil {
			return nil, err
		}
	}
	return from, nil
}
