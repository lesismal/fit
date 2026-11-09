package mysqlwire

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestBinlogStream(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.login(fakeCaps)
		if c, body := f.cmd(); c != ComRegisterSlave || string(body) != "reg" {
			t.Errorf("command %x %q", c, body)
		}
		f.ok(0, 0, 0)
		if c, body := f.cmd(); c != ComBinlogDumpGTID || string(body) != "dump" {
			t.Errorf("command %x %q", c, body)
		}
		f.write([]byte{0, 'e', '1'})
		f.write([]byte{0, 'e', '2'})
		f.write([]byte{0xfe, 0, 0, 0, 0})
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	ctx := ctxT(t)
	if _, err := c.ReadEvent(ctx); err == nil {
		t.Fatal("an event before the stream")
	}
	if err := c.Command(ctx, ComRegisterSlave, []byte("reg")); err != nil {
		t.Fatal(err)
	}
	if err := c.StartBinlog(ctx, ComBinlogDumpGTID, []byte("dump")); err != nil {
		t.Fatal(err)
	}
	if err := c.StartBinlog(ctx, ComBinlogDump, nil); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := c.Command(ctx, ComRegisterSlave, nil); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	for _, want := range []string{"e1", "e2"} {
		ev, err := c.ReadEvent(ctx)
		if err != nil || !bytes.Equal(ev, []byte(want)) {
			t.Fatalf("%q %v", ev, err)
		}
	}
	if _, err := c.ReadEvent(ctx); err != io.EOF {
		t.Fatal(err)
	}
	if _, err := c.ReadEvent(ctx); err == nil {
		t.Fatal("a read after the stream's end")
	}
	c.Close()
}

func TestBinlogStreamErrors(t *testing.T) {
	for name, answer := range map[string][]byte{
		"refused":   {0xff, 0xd4, 0x04, '#', 'H', 'Y', '0', '0', '0', 'g', 'o', 'n', 'e'},
		"malformed": {7},
		"cut":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			addr := serve(t, func(f *fake) {
				f.login(fakeCaps)
				f.cmd()
				if answer != nil {
					f.write(answer)
				}
			})
			c := dialT(t, Config{Addr: addr, User: "u"})
			if err := c.StartBinlog(ctxT(t), ComBinlogDump, nil); err != nil {
				t.Fatal(err)
			}
			_, err := c.ReadEvent(ctxT(t))
			var se *Error
			if err == nil || name == "refused" && (!errors.As(err, &se) || se.Code != 1236) {
				t.Fatal(err)
			}
			if c.Err() == nil {
				t.Fatal("the connection is still usable")
			}
			if err := c.StartBinlog(ctxT(t), ComBinlogDump, nil); err == nil {
				t.Fatal("a stream on a broken connection")
			}
		})
	}
}

func TestStartBinlogWriteFails(t *testing.T) {
	addr := serve(t, func(f *fake) { f.login(fakeCaps) })
	c := dialT(t, Config{Addr: addr, User: "u"})
	c.raw.Conn.Close()
	if err := c.StartBinlog(ctxT(t), ComBinlogDump, nil); err == nil {
		t.Fatal("no error")
	}
}
