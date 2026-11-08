package fitsync

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// CheckpointVersion is the version of the checkpoint file's format; a file
// of a later version is refused rather than misread.
const CheckpointVersion = 1

// Checkpoint is a pipeline's checkpoint file, <state_dir>/<pipeline>.json.
type Checkpoint struct {
	Version  int             `json:"version"`
	Pipeline string          `json:"pipeline"`
	Position json.RawMessage `json:"position,omitempty"`
	Saved    time.Time       `json:"saved"`
	// Job is the progress of a pipeline of mode once.
	Job *Progress `json:"job,omitempty"`
	// Lineage names the line of positions the checkpoint is of, for the
	// sinks that keep the checkpoint too (CheckpointSink): a reset, a
	// position set by hand and a new job start another.
	Lineage string `json:"lineage,omitempty"`
}

// store keeps one pipeline's checkpoint.
type store struct {
	dir, name string
	// crash, when not nil, is asked at CrashCheckpointWritten.
	crash func(Point) bool
	// lineage is what save writes as the checkpoint's Lineage.
	lineage string
}

func (s *store) path() string { return filepath.Join(s.dir, s.name+".json") }

func (s *store) tempPattern() string { return "." + s.name + ".json.tmp-*" }

// load reads the checkpoint and the job's progress, nil when there is none
// yet, and removes the temporary files a crash left.
func (s *store) load() (Position, *Progress, error) {
	if olds, _ := filepath.Glob(filepath.Join(s.dir, s.tempPattern())); len(olds) > 0 {
		for _, o := range olds {
			_ = os.Remove(o)
		}
	}
	data, err := os.ReadFile(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, nil, fmt.Errorf("the checkpoint %s: %w", s.path(), err)
	}
	if c.Version < 1 || c.Version > CheckpointVersion {
		return nil, nil, fmt.Errorf("the checkpoint %s is of version %d, which this fit does not read", s.path(), c.Version)
	}
	if len(c.Position) == 0 || string(c.Position) == "null" {
		return nil, c.Job, nil
	}
	pos, err := appendCompact(nil, c.Position)
	return Position(pos), c.Job, err
}

// errCrashed is a save a crash hook stopped.
var errCrashed = errors.New("sync: crashed")

// save writes pos as the checkpoint, with the job's progress if any: to a
// temporary file, synced, then renamed over the last, so that a crash
// leaves the old one or the new one whole.
func (s *store) save(pos Position, job *Progress) error {
	data, err := json.MarshalIndent(Checkpoint{Version: CheckpointVersion, Pipeline: s.name, Position: json.RawMessage(pos), Saved: time.Now().UTC(), Job: job, Lineage: s.lineage}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, s.tempPattern())
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(append(data, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && s.crash != nil && s.crash(CrashCheckpointWritten) {
		return errCrashed // the temporary file stays, as after kill -9
	}
	if err == nil {
		err = os.Rename(tmp, s.path())
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// The rename is made durable by syncing the directory, where the
	// system can; Windows cannot, and its rename is durable as it is.
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
