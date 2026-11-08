package kafkawire

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// TopicSpec is a topic to create. Partitions and ReplicationFactor of 0
// (or -1) take the brokers' defaults.
type TopicSpec struct {
	Name              string
	Partitions        int32
	ReplicationFactor int16
	Configs           map[string]string
}

// CreateTopics creates topics and waits up to timeout for their leaders
// to be chosen. A topic that exists already is TOPIC_ALREADY_EXISTS among
// the errors, joined.
func (c *Client) CreateTopics(ctx context.Context, timeout time.Duration, topics ...TopicSpec) error {
	var e encoder
	e.int32(int32(len(topics)))
	for _, t := range topics {
		e.string(t.Name)
		e.int32(cmp.Or(t.Partitions, -1))
		e.int16(cmp.Or(t.ReplicationFactor, -1))
		e.int32(0) // assignments
		keys := make([]string, 0, len(t.Configs))
		for k := range t.Configs {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		e.int32(int32(len(keys)))
		for _, k := range keys {
			v := t.Configs[k]
			e.string(k)
			e.nullableString(&v)
		}
	}
	e.int32(int32(timeout / time.Millisecond))
	e.bool(false) // validate only
	return c.admin(ctx, apiCreateTopics, e.b, timeout, true)
}

// DeleteTopics deletes topics, waiting up to timeout.
func (c *Client) DeleteTopics(ctx context.Context, timeout time.Duration, names ...string) error {
	var e encoder
	e.int32(int32(len(names)))
	for _, n := range names {
		e.string(n)
	}
	e.int32(int32(timeout / time.Millisecond))
	return c.admin(ctx, apiDeleteTopics, e.b, timeout, false)
}

// admin sends a topic request to the controller (any broker forwards it,
// in KRaft), retrying NOT_CONTROLLER and failed connections.
func (c *Client) admin(ctx context.Context, api int16, body []byte, timeout time.Duration, create bool) error {
	return c.retry(ctx, func() error { return c.adminOnce(ctx, api, body, timeout, create) })
}

func (c *Client) adminOnce(ctx context.Context, api int16, body []byte, timeout time.Duration, create bool) error {
	m, err := c.requestMetadata(ctx, nil, false)
	if err != nil {
		return err
	}
	c.merge(m, false)
	addr := ""
	for _, b := range m.Brokers {
		if b.NodeID == m.ControllerID {
			addr = b.Addr()
		}
	}
	if addr == "" && len(m.Brokers) > 0 {
		addr = m.Brokers[0].Addr()
	}
	if addr == "" {
		return ErrLeaderNotAvailable
	}
	bc, err := c.conn(ctx, addr, false)
	if err != nil {
		return err
	}
	v, err := bc.version(api)
	if err != nil {
		return err
	}
	resp, err := bc.roundTrip(ctx, api, v, body, c.cfg.RequestTimeout+timeout)
	if err != nil {
		return err
	}
	d := decoder{b: resp}
	d.int32() // throttle
	var errs []error
	for range d.arrayLen() {
		name := d.string()
		code := Error(d.int16())
		var msg *string
		if create {
			msg = d.nullableString()
		}
		if d.err != nil || code == 0 {
			continue
		}
		if code == ErrNotController {
			return code
		}
		if msg != nil && *msg != "" {
			errs = append(errs, fmt.Errorf("kafka: topic %s: %s: %w", name, *msg, code))
		} else {
			errs = append(errs, fmt.Errorf("kafka: topic %s: %w", name, code))
		}
	}
	if d.err != nil {
		return &ConnError{Addr: bc.addr, Err: d.err}
	}
	return errors.Join(errs...)
}
