//go:build wasip1

// Command sync is the plugin the sync modules' tests run: a transform, a
// codec of a line format of its own, a source reading a paged HTTP API and
// a sink posting to one, each chosen by its config, and misbehaving on
// purpose.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lesismal/fit/plugin/wasm/guest"
)

type settings struct {
	Mode string `json:"mode"`
	URL  string `json:"url"`
	Fail int64  `json:"fail"`
}

var (
	conf   settings
	loaded bool
)

func cfg() settings {
	if !loaded {
		_ = json.Unmarshal([]byte(guest.Config()), &conf)
		loaded = true
	}
	return conf
}

// hold keeps what grow allocates.
var hold [][]byte

func init() {
	guest.OnTransform(transform)
	guest.OnDecode(decode)
	guest.OnEncode(encode)
	guest.OnPoll(poll)
	guest.OnWrite(write)
}

func id(r *guest.Record) int64 {
	key, _ := guest.ParseRow(r.Key)
	v, _ := key.Get("id")
	n, _ := guest.AsInt(v)
	return n
}

func transform(batch []guest.Record) error {
	c := cfg()
	for i := range batch {
		r := &batch[i]
		if c.Mode == "none" {
			continue
		}
		if c.Mode == "route" {
			// The same work as the built-in route with a map.
			if r.Namespace == "shop.orders" {
				r.Namespace = "orders"
			}
			guest.Emit(i, r)
			continue
		}
		n := id(r)
		if n == c.Fail {
			switch c.Mode {
			case "loop":
				for {
				}
			case "grow":
				hold = append(hold, make([]byte, 64<<20))
			case "trap":
				panic("on purpose")
			case "http":
				_, err := guest.Do(guest.Request{URL: "http://127.0.0.1:1/"})
				return err
			case "refuse":
				guest.Fail(i, fmt.Sprintf("record %d refused", n))
				continue
			}
		}
		switch n % 5 {
		case 0: // dropped
		case 1: // split
			guest.Emit(i, r)
			r.Namespace += ".copy"
			guest.Emit(i, r)
		default: // changed
			row, err := guest.ParseRow(r.After)
			if err != nil {
				return err
			}
			if row != nil {
				row.Set("seen", guest.Bool(true))
				r.After = row.Value()
			}
			guest.Emit(i, r)
		}
	}
	return nil
}

// The line format: id|name|price, a line a record.

func decode(data []byte) error {
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			return fmt.Errorf("%q is not id|name|price", line)
		}
		n, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return err
		}
		guest.Emit(0, &guest.Record{Namespace: "lines", Op: "insert",
			Key:   guest.Row{f("id", guest.Int(n))}.Value(),
			After: guest.Row{f("id", guest.Int(n)), f("name", guest.String(parts[1])), f("price", []byte(`{"decimal":"`+parts[2]+`"}`))}.Value()})
	}
	return nil
}

func encode(batch []guest.Record) ([]byte, error) {
	var b bytes.Buffer
	for _, r := range batch {
		row, err := guest.ParseRow(r.After)
		if err != nil {
			return nil, err
		}
		idv, _ := row.Get("id")
		n, _ := guest.AsInt(idv)
		namev, _ := row.Get("name")
		name, _ := guest.AsString(namev)
		pricev, _ := row.Get("price")
		var price struct{ Decimal string }
		_ = json.Unmarshal(pricev, &price)
		fmt.Fprintf(&b, "%d|%s|%s\n", n, name, price.Decimal)
	}
	return b.Bytes(), nil
}

// The API: GET url?after=N gives {"items": [f("id", "name"}...], "more"},
// the items after id N, a page of them.

func poll(checkpoint []byte) ([]byte, bool, error) {
	var cp struct {
		After int64 `json:"after"`
	}
	if len(checkpoint) > 0 {
		if err := json.Unmarshal(checkpoint, &cp); err != nil {
			return nil, false, err
		}
	}
	c := cfg()
	resp, err := guest.Do(guest.Request{URL: c.URL + "?after=" + strconv.FormatInt(cp.After, 10)})
	if err != nil {
		return nil, false, err
	}
	if resp.Status != 200 {
		return nil, false, fmt.Errorf("the API answered %d", resp.Status)
	}
	var page struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
		More bool `json:"more"`
	}
	if err := json.Unmarshal(resp.Body, &page); err != nil {
		return nil, false, err
	}
	if len(page.Items) == 0 {
		return nil, true, nil
	}
	for _, it := range page.Items {
		guest.Emit(0, &guest.Record{Namespace: "api.items", Op: "insert",
			Key:   guest.Row{f("id", guest.Int(it.ID))}.Value(),
			After: guest.Row{f("id", guest.Int(it.ID)), f("name", guest.String(it.Name))}.Value()})
	}
	next := fmt.Appendf(nil, `{"after":%d}`, page.Items[len(page.Items)-1].ID)
	return next, !page.More, nil
}

func write(batch []guest.Record) error {
	c := cfg()
	for i := range batch {
		if id(&batch[i]) == c.Fail && c.Fail != 0 {
			return &guest.RecordError{Index: i, Msg: "refused"}
		}
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	resp, err := guest.Do(guest.Request{Method: "POST", URL: c.URL, Body: string(body),
		Headers: map[string]string{"Content-Type": "application/json"}})
	if err != nil {
		return err
	}
	if resp.Status != 200 {
		return errors.New("the API answered " + strconv.Itoa(resp.Status))
	}
	return nil
}

func f(name string, v []byte) guest.Field { return guest.Field{Name: name, Value: v} }

func main() {}
