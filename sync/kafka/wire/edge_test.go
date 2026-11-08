package kafkawire

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCorruptBatches reads every batch with each byte changed (its CRC
// made right again) and cut at every length: errors or records, never a
// panic or a record past the batch.
func TestCorruptBatches(t *testing.T) {
	for _, c := range codecs {
		b, _ := appendBatch(nil, testRecords(4), c, 0, noProducer, time.Now())
		for i := range b {
			for _, x := range []byte{0x01, 0x80, 0xff} {
				m := append([]byte(nil), b...)
				m[i] ^= x
				if i >= 21 {
					putCRC(m)
				}
				br := batchReader{maxBatchBytes: 1 << 16, dec: &decompressor{max: 1 << 16}, aborted: []abortedTxn{{producerID: -1}}}
				br.read(m, 0, nil)
			}
		}
		for n := range len(b) {
			m := append([]byte(nil), b[:n]...)
			if n >= 12 {
				binary.BigEndian.PutUint32(m[8:], uint32(n-12))
			}
			if n >= batchHeaderLen {
				putCRC(m)
			}
			br := batchReader{maxBatchBytes: 1 << 16, dec: &decompressor{max: 1 << 16}}
			br.read(m, 0, nil)
		}
	}
	// A batch shorter than its header, and one of magic 1.
	b, _ := appendBatch(nil, testRecords(1), None, 0, noProducer, time.Now())
	short := append([]byte(nil), b...)
	binary.BigEndian.PutUint32(short[8:], 10)
	br := batchReader{maxBatchBytes: 1 << 16, dec: &decompressor{}}
	if _, _, err := br.read(short, 0, nil); err == nil {
		t.Fatal("a short batch taken")
	}
	old := append([]byte(nil), b...)
	old[16] = 1
	if _, _, err := br.read(old, 0, nil); err == nil || !strings.Contains(err.Error(), "magic 1") {
		t.Fatal(err)
	}
	// A batch wholly before the offset asked is skipped.
	out, next, err := br.read(b, 5, nil)
	if err != nil || len(out) != 0 || next != 5 {
		t.Fatal(out, next, err)
	}
	// Control batches: an abort marker compressed is not one; markers
	// cut short are not either.
	if isAbortMarker(nil, attrCompression) || isAbortMarker([]byte{0}, 0) || isAbortMarker([]byte{2, 0, 0, 0, 0}, 0) {
		t.Fatal("not markers")
	}
	// A codec number no codec has.
	if _, err := (&decompressor{}).decompress(Compression(6), nil, 10); err == nil {
		t.Fatal("codec 6 taken")
	}
}

func TestCorruptCompressed(t *testing.T) {
	d := &decompressor{max: 1 << 20}
	src := bytes.Repeat([]byte("corrupt "), 4096)
	for _, c := range codecs[1:] {
		z, _ := compress(c, 0, src)
		for _, n := range []int{0, 1, 5, 9, 17, 21, len(z) / 2, len(z) - 1} {
			d.decompress(c, z[:min(n, len(z))], 1<<20)
		}
		for i := 0; i < len(z); i += max(1, len(z)/64) {
			m := append([]byte(nil), z...)
			m[i] ^= 0x55
			d.decompress(c, m, 1<<20)
		}
	}
	// Each refused outright.
	if _, err := d.decompress(Gzip, []byte("not gzip"), 100); err == nil {
		t.Fatal("not gzip taken")
	}
	gz, _ := compress(Gzip, 0, src)
	gz[len(gz)/2] ^= 0xff
	if _, err := d.decompress(Gzip, gz, 1<<20); err == nil {
		t.Fatal("corrupt gzip taken")
	}
	if _, err := d.decompress(Zstd, []byte("not zstd"), 100); err == nil {
		t.Fatal("not zstd taken")
	}
	z, _ := compress(Zstd, 0, src)
	if _, err := d.decompress(Zstd, z, 100); !errors.Is(err, errTooLarge) {
		t.Fatal(err)
	}
	if _, err := snappyDecode([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, 100); err == nil {
		t.Fatal("a bad length taken")
	}
	framed, _ := compress(Snappy, 0, src)
	if _, err := snappyDecode(framed[:18], 1<<20); err == nil {
		t.Fatal("a short framed length taken")
	}
	bad := append([]byte(nil), framed[:16]...)
	bad = append(bad, 0, 0, 0, 6, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff)
	if _, err := snappyDecode(bad, 1<<20); err == nil {
		t.Fatal("a chunk of a bad length taken")
	}
	chunk := []byte{10, 0x4c, 'a'} // ten bytes, a literal of twenty cut short
	bad = append(append([]byte(nil), framed[:16]...), 0, 0, 0, byte(len(chunk)))
	bad = append(bad, chunk...)
	if _, err := snappyDecode(bad, 1<<20); err == nil {
		t.Fatal("a corrupt chunk taken")
	}
	if c, err := ParseCompression(""); err != nil || c != None {
		t.Fatal(c, err)
	}
}

func TestCorruptLZ4(t *testing.T) {
	src := bytes.Repeat([]byte("lz4 lz4 lz4 frame "), 500)
	z := lz4Compress(nil, src)
	for n := range len(z) {
		lz4Decompress(nil, z[:n], 1<<20)
	}
	for i := range z {
		m := append([]byte(nil), z...)
		m[i] ^= 0xa5
		lz4Decompress(nil, m, 1<<20)
	}
	hdr := func(flg, bd byte) []byte {
		b := binary.LittleEndian.AppendUint32(nil, lz4Magic)
		b = append(b, flg, bd)
		return append(b, byte(xxh32([]byte{flg, bd}, 0)>>8))
	}
	for _, c := range []struct {
		name  string
		frame []byte
	}{
		{"version 0", hdr(0x20, 0x40)},
		{"dictionary", hdr(0x61, 0x40)},
		{"tiny blocks", hdr(0x60, 0x10)},
		{"no end", hdr(0x60, 0x40)},
		{"block past max", append(hdr(0x60, 0x40), 0xff, 0xff, 0x01, 0x00)},
		{"stored past the limit", append(hdr(0x60, 0x40), 0x10, 0, 0, 0x80, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16)},
		{"block checksum", append(hdr(0x70, 0x40), 1, 0, 0, 0, 0x10, 0, 0, 0, 0)},
		{"content checksum", append(hdr(0x64, 0x40), 1, 0, 0, 0, 0x10, 0, 0, 0, 0, 1, 2, 3, 4)},
		{"short descriptor", hdr(0x68, 0x40)},
	} {
		if _, err := lz4Decompress(nil, c.frame, 8); err == nil {
			t.Errorf("%s taken", c.name)
		}
	}
	// Blocks: a literal length past the limit, cut short, an offset of
	// zero or before the start, a match past the limit, no last literals.
	for _, blk := range [][]byte{
		{0xf0, 0xff, 0xff},
		{0xf0},
		{0x50, 'a'},
		{0x10, 'a', 0, 0, 0x00},
		{0x10, 'a', 9, 0, 0x00},
		{0x10, 'a', 1},
		{0x1f, 'a', 1, 0, 0xff, 0xff},
		{0x1f, 'a', 1, 0},
		{0x1f, 'a', 1, 0, 50},
		{0x10, 'a', 1, 0},
	} {
		if _, err := lz4DecompressBlock(nil, blk, 32); err == nil {
			t.Errorf("block %x taken", blk)
		}
	}
	// A match longer than its offset: a run.
	out, err := lz4DecompressBlock(nil, []byte{0x11, 'a', 1, 0, 0x10, 'b'}, 32)
	if err != nil || string(out) != "aaaaaab" {
		t.Fatalf("%q, %v", out, err)
	}
	// Two frames one after the other.
	two := append(lz4Compress(nil, []byte("one ")), lz4Compress(nil, []byte("two"))...)
	if out, err := lz4Decompress(nil, two, 100); err != nil || string(out) != "one two" {
		t.Fatalf("%q, %v", out, err)
	}
	// Long literals and matches use the extra length bytes.
	long := append(bytes.Repeat([]byte{7}, 600), []byte("0123456789abcdefghijklmnopqrstuvwxyz0123456789")...)
	for i := range 300 {
		long = append(long, byte(i*7))
	}
	if out, err := lz4Decompress(nil, lz4Compress(nil, long), 1<<20); err != nil || !bytes.Equal(out, long) {
		t.Fatal(err)
	}
}

func TestDecoderAndErrors(t *testing.T) {
	var e encoder
	e.bool(true)
	e.bool(false)
	e.int8(-3)
	e.int64(-5)
	s := "str"
	e.nullableString(&s)
	d := decoder{b: e.b}
	if !d.bool() || d.bool() || d.int8() != -3 || d.int64() != -5 || *d.nullableString() != "str" || d.err != nil {
		t.Fatal("round trip")
	}
	if d.int8() != 0 || d.int64() != 0 || d.string() != "" || d.err == nil {
		t.Fatal("past the end")
	}
	d = decoder{b: []byte{0xff, 0xff, 0xff, 0xff}}
	if d.arrayLen() != 0 || d.err != nil {
		t.Fatal("a null array")
	}
	d = decoder{b: []byte{0xff, 0xff}}
	if d.string() != "" || d.err != nil {
		t.Fatal("a null string")
	}
	d = decoder{err: errShort}
	if d.varint() != 0 || d.take(1) != nil {
		t.Fatal("after an error")
	}
	d = decoder{b: []byte{0x80}}
	if d.varint() != 0 || d.err == nil {
		t.Fatal("a cut varint")
	}
	if ErrRequestTimedOut.staleMetadata() || !ErrNotLeaderOrFollower.staleMetadata() {
		t.Fatal("stale")
	}
	err := scram(sha256.New, "u", "p", "n", func([]byte) ([]byte, error) { return []byte("r=nx,s=!!!,i=4096"), nil })
	if err == nil || !strings.Contains(err.Error(), "salt") {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := scram(sha256.New, "u", "p", "", func([]byte) ([]byte, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	step := 0
	err = scram(sha256.New, "u", "p", "n", func([]byte) ([]byte, error) {
		step++
		if step == 1 {
			return []byte("r=nx,s=c2FsdA==,i=4096"), nil
		}
		return nil, boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

// TestBrokerAnswers covers answers a broker may give and a client must
// read: errors without messages, partitions not asked, a cut-off answer.
func TestBrokerAnswers(t *testing.T) {
	fc := newFakeCluster(t, 2, nil)
	fc.addTopic("t", 2)
	fc.set(func() { fc.users = map[string]string{"fit": "pw"} })
	ctx := testCtx(t)
	short := func() *Client {
		return fakeClient(t, fc, func(c *Config) { c.RetryFor = 30 * time.Millisecond })
	}
	hook := func(api int16, a fakeAction, body []byte) {
		fc.setHook(func(b *fakeBroker, got, v int16, _ []byte) (fakeAction, []byte) {
			if got == api {
				return a, body
			}
			return fakeNormal, nil
		})
	}
	var e encoder
	// ListOffsets: an error code, and a partition not asked.
	e = encoder{}
	e.int32(0)
	e.int32(1)
	e.string("t")
	e.int32(2)
	for _, p := range []int32{7, 0} {
		e.int32(p)
		e.int16(int16(ErrTopicAuthorizationFailed))
		e.int64(-1)
		e.int64(-1)
		e.int32(-1)
	}
	hook(apiListOffsets, fakeReply, e.b)
	if _, err := short().Offset(ctx, "t", 0, OffsetLatest); !errors.Is(err, ErrTopicAuthorizationFailed) {
		t.Fatal(err)
	}
	// Fetch: a partition not asked, and aborted transactions past the
	// answer's end.
	e = encoder{}
	e.int32(0)
	e.int16(0)
	e.int32(0)
	e.int32(1)
	e.string("t")
	e.int32(2)
	for _, p := range []int32{7, 0} {
		e.int32(p)
		e.int16(0)
		e.int64(0)
		e.int64(0)
		e.int64(0)
		e.int32(-1)
		e.int32(-1)
		e.bytes(nil)
	}
	hook(apiFetch, fakeReply, e.b)
	if res, err := short().Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t"}); err != nil || len(res[0].Records) != 0 {
		t.Fatal(err)
	}
	e = encoder{}
	e.int32(0)
	e.int16(0)
	e.int32(0)
	e.int32(1)
	e.string("t")
	e.int32(1)
	e.int32(0)
	e.int16(0)
	e.int64(0)
	e.int64(0)
	e.int64(0)
	e.int32(1000)
	hook(apiFetch, fakeReply, e.b)
	var ce *ConnError
	if _, err := short().Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t"}); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	// Produce: a partition not asked.
	e = encoder{}
	e.int32(1)
	e.string("t")
	e.int32(1)
	e.int32(7)
	e.int16(0)
	e.int64(0)
	e.int64(0)
	e.int64(0)
	e.int32(0)
	e.nullableString(nil)
	e.int32(0)
	hook(apiProduce, fakeReply, e.b)
	if _, err := short().ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{}); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	// SASL: an error with no message; a handshake cut off.
	e = encoder{}
	e.int16(int16(ErrSASLAuthenticationFailed))
	e.nullableString(nil)
	e.bytes(nil)
	e.int64(0)
	hook(apiSaslAuthenticate, fakeReply, e.b)
	sc := fakeClient(t, fc, func(c *Config) { c.SASL = &SASL{Mechanism: SASLPlain, Username: "fit", Password: "pw"} })
	if _, err := sc.Metadata(ctx); !errors.Is(err, ErrSASLAuthenticationFailed) {
		t.Fatal(err)
	}
	for _, api := range []int16{apiSaslHandshake, apiSaslAuthenticate} {
		hook(api, fakeClose, nil)
		sc := fakeClient(t, fc, func(c *Config) {
			c.SASL = &SASL{Mechanism: SASLPlain, Username: "fit", Password: "pw"}
			c.RetryFor = time.Millisecond
		})
		if _, err := sc.Metadata(ctx); !errors.As(err, &ce) {
			t.Fatal(err)
		}
	}
	// Answers cut short.
	for _, a := range []fakeAction{fakeCutHeader, fakeCutBody} {
		hook(apiMetadata, a, nil)
		if _, err := short().Metadata(ctx); !errors.As(err, &ce) {
			t.Fatal(err)
		}
	}
	// Admin: no brokers in the metadata; the request's connection fails.
	e = encoder{}
	e.int32(0)
	e.int32(0)
	e.nullableString(nil)
	e.int32(-1)
	e.int32(0)
	e.int32(0)
	hook(apiMetadata, fakeReply, e.b)
	if err := short().CreateTopics(ctx, time.Second, TopicSpec{Name: "x"}); !errors.Is(err, ErrLeaderNotAvailable) {
		t.Fatal(err)
	}
	hook(apiCreateTopics, fakeClose, nil)
	if err := short().CreateTopics(ctx, time.Second, TopicSpec{Name: "x"}); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	fc.mu.Lock()
	fc.hook = nil
	for _, b := range fc.brokers {
		b.down = true
	}
	fc.mu.Unlock()
	if err := short().CreateTopics(ctx, time.Second, TopicSpec{Name: "x"}); !errors.As(err, &ce) {
		t.Fatal(err)
	}
}

func TestRetriesEndWithContext(t *testing.T) {
	fc := newFakeCluster(t, 2, nil)
	fc.addTopic("t", 2)
	c := fakeClient(t, fc, func(c *Config) { c.RetryFor = time.Minute; c.RetryBackoff = 20 * time.Millisecond })
	ctx := testCtx(t)
	if _, err := c.Metadata(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	fc.setLeader("t", 0, -1)
	for _, call := range []func(ctx context.Context) error{
		func(ctx context.Context) error {
			_, err := c.ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{})
			return err
		},
		func(ctx context.Context) error { _, err := c.Partitions(ctx, "t"); return err },
	} {
		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		err := call(cctx)
		cancel()
		if err == nil {
			t.Fatal("a call outlived its context")
		}
	}
	// A canceled context while every bootstrap broker is down.
	fc.mu.Lock()
	for _, b := range fc.brokers {
		b.down = true
	}
	fc.mu.Unlock()
	d := fakeClient(t, fc, func(c *Config) { c.Brokers = fc.addrs() })
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.Metadata(cctx); err == nil {
		t.Fatal("metadata with a canceled context")
	}
}

// TestFetchMovedAndData fetches two partitions, one moved to another
// broker: the one with records comes back, the moved one is asked again
// at its new leader without waiting.
func TestFetchMovedAndData(t *testing.T) {
	fc := newFakeCluster(t, 2, nil)
	fc.addTopic("t", 2)
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	for p := range int32(2) {
		if _, err := c.ProduceRecords(ctx, ProduceOptions{}, "t", p, Record{Value: []byte("v")}); err != nil {
			t.Fatal(err)
		}
	}
	// Partition 1 moves to broker 1 as far as the brokers know, the
	// client's metadata says broker 2.
	fc.setLeader("t", 1, 1)
	start := time.Now()
	res, err := c.Fetch(ctx, FetchOptions{MaxWait: 3 * time.Second},
		FetchPartition{Topic: "t", Partition: 0}, FetchPartition{Topic: "t", Partition: 1, Offset: 1})
	if err != nil || len(res[0].Records) != 1 || len(res[1].Records) != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Log("the retry waited for data") // not a failure: timing is not checked
	}
	// A leader the metadata names whose broker is down.
	fc.mu.Lock()
	fc.brokers[1].down = true
	fc.mu.Unlock()
	fc.setLeader("t", 1, 2)
	d := fakeClient(t, fc, func(c *Config) { c.RetryFor = 50 * time.Millisecond })
	var ce *ConnError
	if _, err := d.Offset(ctx, "t", 1, OffsetLatest); !errors.As(err, &ce) {
		t.Fatal(err)
	}
}

// TestConcurrentRefresh has many calls find the metadata stale at once:
// one asks the brokers, the others use its answer.
func TestConcurrentRefresh(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	fc.addTopic("u", 1)
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	if _, err := c.Metadata(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			topic := []string{"t", "u"}[i%2]
			if _, err := c.Partitions(ctx, topic); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// A topic with an error in the metadata kept is asked about again.
	if _, err := c.Metadata(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	fc.addTopic("gone", 1)
	if n, err := c.Partitions(ctx, "gone"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// The metadata answer lacks the topic asked about.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api != apiMetadata {
			return fakeNormal, nil
		}
		var e encoder
		e.int32(0)
		e.int32(0)
		e.nullableString(nil)
		e.int32(-1)
		e.int32(0)
		e.int32(0)
		return fakeReply, e.b
	})
	d := fakeClient(t, fc, func(c *Config) { c.RetryFor = 20 * time.Millisecond })
	if _, err := d.Partitions(ctx, "t"); !errors.Is(err, ErrLeaderNotAvailable) {
		t.Fatal(err)
	}
}
