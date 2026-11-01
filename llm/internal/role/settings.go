package role

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
)

// Duration checks v, a duration the pages set as Go writes it, and returns
// it as Go writes it, or "" when v is empty or equal to def, which the
// member then leaves out. A duration that does not parse, or is below lo,
// is a bad request naming field.
func Duration(field, v string, lo, def time.Duration) (string, error) {
	if v == "" {
		return "", nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d < 0 {
		return "", mesh.BadRequest("%s is a duration of at least %s, as 30s or 5m", field, lo)
	}
	if d == def || d == 0 {
		return "", nil
	}
	return d.String(), nil
}

// Count checks n, a count the pages set, which may not be negative, and
// returns it, or 0 when it equals def, which the member then leaves out.
func Count[T int | int64](field string, n, def T) (T, error) {
	if n < 0 {
		return 0, mesh.BadRequest("%s may not be negative", field)
	}
	if n == def {
		return 0, nil
	}
	return n, nil
}

// Object builds the members of a JSON object from pairs of names and
// values, leaving out the empty and zero ones.
func Object(pairs ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		switch v := pairs[i+1].(type) {
		case string:
			if v == "" {
				continue
			}
		case int:
			if v == 0 {
				continue
			}
		case int64:
			if v == 0 {
				continue
			}
		}
		out[pairs[i].(string)] = pairs[i+1]
	}
	return out
}

// limits returns the role's bounds on its links, their defaults filled in.
func (b *Base) limits() model.Limits {
	b.mu.Lock()
	l := b.lim
	b.mu.Unlock()
	return model.Limits{MaxConns: l.Conns(), MaxInflight: l.Inflight(), PingInterval: l.Ping().String(),
		SlowTimeout: l.Slow().String(), RetryInterval: l.Retry().String(), StreamIdle: l.Idle().String()}
}

// setLimits writes the bounds on the role's links into its member, and
// answers them as applied.
func (b *Base) setLimits(ctx context.Context, _ model.Identity, req *model.Limits) (any, error) {
	conns, err := Count("max_conns", req.MaxConns, config.DefaultLLMMaxConns)
	if err != nil {
		return nil, err
	}
	inflight, err := Count("max_inflight", req.MaxInflight, config.DefaultLLMMaxInflight)
	if err != nil {
		return nil, err
	}
	ping, err := Duration("ping_interval", req.PingInterval, 100*time.Millisecond, config.DefaultLLMPingInterval.Std())
	if err != nil {
		return nil, err
	}
	slow, err := Duration("slow_timeout", req.SlowTimeout, 0, config.DefaultLLMSlowTimeout.Std())
	if err != nil {
		return nil, err
	}
	retry, err := Duration("retry_interval", req.RetryInterval, 100*time.Millisecond, config.DefaultLLMRetryInterval.Std())
	if err != nil {
		return nil, err
	}
	idle, err := Duration("stream_idle", req.StreamIdle, time.Second, config.DefaultLLMStreamIdle.Std())
	if err != nil {
		return nil, err
	}
	l := Object("max_conns", conns, "max_inflight", inflight, "ping_interval", ping, "slow_timeout", slow,
		"retry_interval", retry, "stream_idle", idle)
	if err := b.Edit(ctx, func(m map[string]json.RawMessage) error { return Set(m, "limits", l) }); err != nil {
		return nil, err
	}
	return b.limits(), nil
}

// registerSettings adds the methods of the settings every role has.
func (b *Base) registerSettings() {
	mesh.Handle(b.Router, model.MethodLimits, nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return b.limits(), nil
	})
	mesh.Handle(b.Router, model.MethodLimitsSet, nil, EditTimeout, b.setLimits)
}
