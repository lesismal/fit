package kafkawire

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/snappy"
)

var codecs = []Compression{None, Gzip, Snappy, LZ4, Zstd}

func testRecords(n int) []Record {
	base := time.UnixMilli(1_700_000_000_000)
	recs := make([]Record, n)
	for i := range recs {
		recs[i] = Record{
			Key:       []byte(fmt.Sprintf("key-%d", i)),
			Value:     bytes.Repeat([]byte(fmt.Sprintf("value %d ", i)), i%7+1),
			Timestamp: base.Add(time.Duration(i) * time.Millisecond),
		}
		if i%3 == 0 {
			recs[i].Headers = []Header{{Key: "h", Value: []byte("v")}, {Key: "nil"}}
		}
		if i%5 == 4 {
			recs[i].Value = nil // a tombstone
		}
	}
	recs[0].Key = nil
	return recs
}

func TestBatchRoundTrip(t *testing.T) {
	for _, c := range codecs {
		t.Run(c.String(), func(t *testing.T) {
			in := testRecords(100)
			b, err := appendBatch(nil, in, c, 0, noProducer, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if got := Compression(binary.BigEndian.Uint16(b[21:]) & attrCompression); got != c {
				t.Fatalf("codec in attributes %v", got)
			}
			// Two batches, the second at base offset 100, then half a third.
			set := append([]byte(nil), b...)
			second := append([]byte(nil), b...)
			binary.BigEndian.PutUint64(second, 100)
			set = append(set, second...)
			set = append(set, b[:len(b)/2]...)
			br := batchReader{maxBatchBytes: 1 << 20, dec: &decompressor{max: 1 << 20}}
			out, next, err := br.read(set, 50, nil)
			if err != nil {
				t.Fatal(err)
			}
			if next != 200 || len(out) != 150 {
				t.Fatalf("next %d, %d records", next, len(out))
			}
			for k, r := range out {
				want := in[(k+50)%100]
				if r.Offset != int64(k+50) || !bytes.Equal(r.Key, want.Key) || !bytes.Equal(r.Value, want.Value) ||
					(r.Value == nil) != (want.Value == nil) || (r.Key == nil) != (want.Key == nil) ||
					!r.Timestamp.Equal(want.Timestamp) || len(r.Headers) != len(want.Headers) {
					t.Fatalf("record %d: %+v, want %+v", k, r, want)
				}
				for h := range r.Headers {
					if r.Headers[h].Key != want.Headers[h].Key || !bytes.Equal(r.Headers[h].Value, want.Headers[h].Value) {
						t.Fatalf("record %d header %d", k, h)
					}
				}
			}
			// A flipped bit fails the CRC.
			bad := append([]byte(nil), b...)
			bad[len(bad)-1] ^= 1
			if _, _, err := br.read(bad, 0, nil); err == nil || !strings.Contains(err.Error(), "CRC") {
				t.Fatalf("corrupt batch: %v", err)
			}
		})
	}
}

func TestDecompressLimits(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 1<<20)
	for _, c := range codecs[1:] {
		z, err := compress(c, 0, big)
		if err != nil {
			t.Fatal(err)
		}
		d := &decompressor{max: 1 << 16}
		if _, err := d.decompress(c, z, 1<<16); !errors.Is(err, errTooLarge) {
			t.Errorf("%v: %v, want errTooLarge", c, err)
		}
		d = &decompressor{max: 2 << 20}
		out, err := d.decompress(c, z, 2<<20)
		if err != nil || !bytes.Equal(out, big) {
			t.Errorf("%v: %d bytes, %v", c, len(out), err)
		}
	}
}

func TestCompressionLevels(t *testing.T) {
	src := bytes.Repeat([]byte("level "), 1000)
	for _, c := range []Compression{Gzip, Zstd} {
		for _, l := range []int{0, 1, 9} {
			z, err := compress(c, l, src)
			if err != nil {
				t.Fatal(err)
			}
			out, err := (&decompressor{max: 1 << 20}).decompress(c, z, 1<<20)
			if err != nil || !bytes.Equal(out, src) {
				t.Fatalf("%v level %d: %v", c, l, err)
			}
		}
	}
	if _, err := compress(Gzip, 42, src); err == nil {
		t.Fatal("gzip level 42 taken")
	}
	if _, err := ParseCompression("ZSTD"); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCompression("brotli"); err == nil {
		t.Fatal("brotli taken")
	}
	if Compression(9).String() != "compression(9)" {
		t.Fatal(Compression(9).String())
	}
}

func TestSnappyRawAndFramed(t *testing.T) {
	src := bytes.Repeat([]byte("snappy, as other clients write it "), 3000)
	raw := snappy.Encode(nil, src)
	framed, _ := compress(Snappy, 0, src)
	if !bytes.HasPrefix(framed, xerialHeader) {
		t.Fatal("not in xerial's framing")
	}
	for _, z := range [][]byte{raw, framed} {
		out, err := snappyDecode(z, 1<<20)
		if err != nil || !bytes.Equal(out, src) {
			t.Fatalf("%d bytes, %v", len(out), err)
		}
		if _, err := snappyDecode(z, 1000); !errors.Is(err, errTooLarge) {
			t.Fatalf("limit: %v", err)
		}
	}
	if _, err := snappyDecode(append(framed[:16:16], 0, 0, 1, 0), 1<<20); err == nil {
		t.Fatal("a short chunk taken")
	}
}

func TestLZ4(t *testing.T) {
	want := "kafka lz4 kafka lz4 kafka lz4 hello hello hello hello world, the frame from the lz4 command line tool\n"
	// Frames from the lz4 command line tool: a content size and checksum;
	// block checksums and 4 MiB blocks.
	for _, h := range []string{
		"04224d186c4066000000000000000346000000af6b61666b61206c7a34200a00015f68656c6c6f060000f000776f726c642c20746865206672616d0600216f6d0f00004a00f003636f6d6d616e64206c696e6520746f6f6c0a00000000814e31b0",
		"04224d187040ad46000000af6b61666b61206c7a34200a00015f68656c6c6f060000f000776f726c642c20746865206672616d0600216f6d0f00004a00f003636f6d6d616e64206c696e6520746f6f6c0a3b0ede8900000000",
	} {
		b, _ := hex.DecodeString(h)
		out, err := lz4Decompress(nil, b, 1<<20)
		if err != nil || string(out) != want {
			t.Fatalf("%q, %v", out, err)
		}
		b[len(b)-6] ^= 1
		if _, err := lz4Decompress(nil, b, 1<<20); err == nil {
			t.Fatal("a corrupt frame taken")
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{0, 1, 12, 13, 100, 64 << 10, 64<<10 + 1, 300 << 10} {
		src := make([]byte, n)
		for i := range src {
			if rng.IntN(4) == 0 {
				src[i] = byte(rng.Uint32())
			} else {
				src[i] = "abcabcabd"[i%9]
			}
		}
		z := lz4Compress(nil, src)
		out, err := lz4Decompress(nil, z, len(src))
		if err != nil || !bytes.Equal(out, src) {
			t.Fatalf("%d bytes: %d out, %v", n, len(out), err)
		}
		if n > 0 {
			if _, err := lz4Decompress(nil, z, n-1); !errors.Is(err, errTooLarge) {
				t.Fatalf("%d bytes past the limit: %v", n, err)
			}
		}
	}
	// Random bytes are stored, not compressed.
	src := make([]byte, 1000)
	for i := range src {
		src[i] = byte(rng.Uint32())
	}
	z := lz4Compress(nil, src)
	if binary.LittleEndian.Uint32(z[7:])&(1<<31) == 0 {
		t.Fatal("incompressible block not stored")
	}
}

func TestXXH32(t *testing.T) {
	for _, c := range []struct {
		in   string
		seed uint32
		want uint32
	}{
		{"", 0, 0x02CC5D05},
		{"a", 0, 0x550D7456},
		{"abc", 0, 0x32D153FF},
		{"Nobody inspects the spammish repetition", 0, 0xE2293B2F},
	} {
		if got := xxh32([]byte(c.in), c.seed); got != c.want {
			t.Errorf("xxh32(%q) = %#x, want %#x", c.in, got, c.want)
		}
	}
}

func TestMurmur2(t *testing.T) {
	// The vectors of Kafka's UtilsTest.
	for in, want := range map[string]int32{
		"21":                         -973932308,
		"foobar":                     -790332482,
		"a-little-bit-long-string":   -985981536,
		"a-little-bit-longer-string": -1486304829,
		"lkjh234lh9fiuh90y23oiuhsafujhadof229phr9h19h89h8": -58897971,
		"abc": 479470107,
	} {
		if got := murmur2([]byte(in)); got != want {
			t.Errorf("murmur2(%q) = %d, want %d", in, got, want)
		}
	}
	if p := PartitionFor([]byte("21"), 7); p < 0 || p >= 7 {
		t.Fatal(p)
	}
	if PartitionFor(nil, 0) != 0 {
		t.Fatal("no partitions")
	}
}

func TestScramRFC7677(t *testing.T) {
	// RFC 7677's example of SCRAM-SHA-256.
	step := 0
	err := scram(sha256.New, "user", "pencil", "rOprNGfwEbeRWgbNEkqO", func(msg []byte) ([]byte, error) {
		step++
		switch step {
		case 1:
			if string(msg) != "n,,n=user,r=rOprNGfwEbeRWgbNEkqO" {
				t.Fatalf("client-first %q", msg)
			}
			return []byte("r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"), nil
		default:
			if string(msg) != "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ=" {
				t.Fatalf("client-final %q", msg)
			}
			return []byte("v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="), nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	// A wrong server signature, a server error, a nonce not extended.
	for _, c := range []struct{ first, final string }{
		{"r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", "v=AAAA"},
		{"r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", "e=invalid-proof"},
		{"r=rOprNGfwEbeRWgbNEkqO,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", ""},
		{"r=other,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096", ""},
		{"r=rOprNGfwEbeRWgbNEkqOx,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=99999999", ""},
		{"e=unknown-user", ""},
	} {
		step := 0
		err := scram(sha256.New, "user", "pencil", "rOprNGfwEbeRWgbNEkqO", func([]byte) ([]byte, error) {
			step++
			if step == 1 {
				return []byte(c.first), nil
			}
			return []byte(c.final), nil
		})
		if err == nil {
			t.Errorf("%q, %q taken", c.first, c.final)
		}
	}
}

func TestReadCommitted(t *testing.T) {
	now := time.Now()
	mk := func(base, pid int64, attrs int16, recs []Record) []byte {
		b, _ := appendBatch(nil, recs, None, 0, producer{id: pid, epoch: 0, sequence: 0}, now)
		binary.BigEndian.PutUint64(b, uint64(base))
		binary.BigEndian.PutUint16(b[21:], uint16(attrs))
		binary.BigEndian.PutUint32(b[17:], crcOf(b))
		return b
	}
	marker := func(typ uint16) []Record {
		k := make([]byte, 4)
		binary.BigEndian.PutUint16(k[2:], typ)
		return []Record{{Key: k, Value: []byte{0, 0, 0, 0, 0, 0}}}
	}
	one := func(v string) []Record { return []Record{{Value: []byte(v)}} }
	var set []byte
	set = append(set, mk(0, 7, attrTransactional, one("aborted"))...)
	set = append(set, mk(1, 8, attrTransactional, one("committed"))...)
	set = append(set, mk(2, 7, attrTransactional|attrControl, marker(0))...)
	set = append(set, mk(3, 8, attrTransactional|attrControl, marker(1))...)
	set = append(set, mk(4, 7, attrTransactional, one("later"))...)
	set = append(set, mk(5, -1, 0, one("plain"))...)
	read := func(aborted []abortedTxn) string {
		br := batchReader{maxBatchBytes: 1 << 20, dec: &decompressor{max: 1 << 20}, aborted: aborted}
		out, next, err := br.read(set, 0, nil)
		if err != nil || next != 6 {
			t.Fatalf("next %d, %v", next, err)
		}
		var vs []string
		for _, r := range out {
			vs = append(vs, string(r.Value))
		}
		return strings.Join(vs, ",")
	}
	if got := read([]abortedTxn{{producerID: 7, firstOffset: 0}}); got != "committed,later,plain" {
		t.Fatal(got)
	}
	if got := read(nil); got != "aborted,committed,later,plain" {
		t.Fatal(got)
	}
}

func crcOf(b []byte) uint32 {
	return crc32.Checksum(b[21:], castagnoli)
}

func TestDecoderBounds(t *testing.T) {
	d := decoder{b: []byte{0, 0, 0, 100, 1}}
	if n := d.arrayLen(); n != 0 || d.err == nil {
		t.Fatalf("%d, %v", n, d.err)
	}
	d = decoder{b: []byte{0x7f}}
	d.int32()
	if d.err == nil {
		t.Fatal("short int32 taken")
	}
	d = decoder{b: []byte{0x10, 1}}
	if b := d.varbytes(); b != nil || d.err == nil {
		t.Fatal("short varbytes taken")
	}
	var e encoder
	e.nullableString(nil)
	e.bytes(nil)
	e.varbytes(nil)
	d = decoder{b: e.b}
	if d.nullableString() != nil || d.bytes() != nil || d.varbytes() != nil || d.err != nil {
		t.Fatal("nulls")
	}
}

func TestErrors(t *testing.T) {
	if !strings.Contains(ErrNotLeaderOrFollower.Error(), "NOT_LEADER_OR_FOLLOWER") || Error(999).Error() != "kafka: error 999" {
		t.Fatal("names")
	}
	if !IsRetriable(fmt.Errorf("x: %w", ErrNotLeaderOrFollower)) || IsRetriable(ErrOffsetOutOfRange) ||
		!IsRetriable(&ConnError{Addr: "a", Err: errors.New("reset")}) || IsRetriable(errors.New("x")) {
		t.Fatal("retriable")
	}
}

func TestGzipFromOthers(t *testing.T) {
	// A gzip stream of several members, as some clients write.
	var buf bytes.Buffer
	for _, s := range []string{"one ", "two"} {
		w := gzip.NewWriter(&buf)
		w.Write([]byte(s))
		w.Close()
	}
	out, err := (&decompressor{}).decompress(Gzip, buf.Bytes(), 100)
	if err != nil || string(out) != "one two" {
		t.Fatalf("%q, %v", out, err)
	}
}
