package config

import "time"

// Pprof turns on the runtime profiles the Admin API serves at
// /debug/pprof/, and says how blocking and mutex contention are sampled.
// The CPU, heap, allocation, goroutine and thread profiles and execution
// traces need no setting; the block and mutex profiles are empty until
// their rate is set, since sampling costs every blocking event a little.
type Pprof struct {
	BlockProfileRate     int      `json:"block_profile_rate,omitempty" min:"0" doc:"Sample goroutines blocking on channels, selects and locks for the block profile: one event per this many nanoseconds spent blocked, on average, 1 for every one. 0, none, when left out; set it only while looking, since it costs every blocking event."`
	MutexProfileFraction int      `json:"mutex_profile_fraction,omitempty" min:"0" doc:"Sample one in this many contended mutexes for the mutex profile, 1 for every one. 0, none, when left out."`
	MaxDuration          Duration `json:"max_duration,omitempty" default:"\"1m\"" doc:"The longest a profile's seconds may ask for: a CPU profile or a trace samples for that long, and a delta profile waits that long, holding its connection to the Admin API all the while; a request for longer is refused with 400. 1m when left out."`

	At Value `json:"-" fit:"self"`
}

// DefaultPprofMaxDuration is how long a profile may sample when pprof does
// not say.
var DefaultPprofMaxDuration = Duration(time.Minute)

// MaxSeconds returns the longest a profile may sample, its default when
// left out.
func (p *Pprof) MaxSeconds() time.Duration {
	if p.MaxDuration <= 0 {
		return DefaultPprofMaxDuration.Std()
	}
	return p.MaxDuration.Std()
}
