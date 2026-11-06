package medialib

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
)

const secret = "a library's secret of 16 bytes or more"

// TestKeys derives a key for each library, kind, resource and epoch, the
// same from the same secret, inline or from a file, and none of them, nor
// the secret, shown by the keys' ID or String.
func TestKeys(t *testing.T) {
	k, err := NewKeys("films", secret)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[[16]byte]string{}
	other, _ := NewKeys("shows", secret)
	again, _ := NewKeys("films", secret+"!")
	for _, c := range []struct {
		k              *Keys
		kind, resource string
		epoch          uint64
	}{
		{k, KeyItem, "films/bbb", 0}, {k, KeyItem, "films/bbc", 0}, {k, KeyChannel, "films/bbb", 0},
		{k, KeyChannel, "tv/s1", 0}, {k, KeyChannel, "tv/s1", 1}, {other, KeyItem, "films/bbb", 0}, {again, KeyItem, "films/bbb", 0},
	} {
		key := c.k.Key(c.kind, c.resource, c.epoch)
		name := fmt.Sprint(c.k.ID(), c.kind, c.resource, c.epoch)
		if prev, ok := seen[key]; ok {
			t.Fatalf("%s has the key of %s", name, prev)
		}
		seen[key] = name
	}
	if k2, _ := NewKeys("films", secret); k.Key(KeyItem, "films/bbb", 0) != k2.Key(KeyItem, "films/bbb", 0) {
		t.Fatal("a key is not the same of the same secret")
	}
	if k.ID() == other.ID() || k.ID() == again.ID() || len(k.ID()) != 12 || (*Keys)(nil).ID() != "" {
		t.Fatalf("IDs %q %q %q", k.ID(), other.ID(), again.ID())
	}
	if s := fmt.Sprint(k); strings.Contains(s, secret) || !strings.Contains(s, k.ID()) {
		t.Fatalf("%s", s)
	}
	if _, err := NewKeys("films", "short"); err == nil {
		t.Fatal("a short secret")
	}

	file := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(file, []byte(secret+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		lib  config.MediaLibrary
		want string // the error, or "" for k's ID, or "none"
	}{
		{config.MediaLibrary{Name: "films", Secret: secret}, ""},
		{config.MediaLibrary{Name: "films", SecretFile: file}, ""},
		{config.MediaLibrary{Name: "films"}, "none"},
		{config.MediaLibrary{Name: "films", Secret: secret, SecretFile: file}, "one of the two"},
		{config.MediaLibrary{Name: "films", SecretFile: file + ".missing"}, "secret_file"},
		{config.MediaLibrary{Name: "films", Secret: "short"}, "at least 16"},
	} {
		got, err := KeysOf(&c.lib)
		switch {
		case c.want == "none":
			if got != nil || err != nil {
				t.Errorf("%+v: %v %v", c.lib, got, err)
			}
		case c.want == "":
			if err != nil || got.ID() != k.ID() {
				t.Errorf("%+v: %v %v", c.lib, got, err)
			}
		default:
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%+v: %v, want %q", c.lib, err, c.want)
			}
		}
	}
	short := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(short, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := KeysOf(&config.MediaLibrary{Name: "films", SecretFile: short}); err == nil || !strings.Contains(err.Error(), short) {
		t.Errorf("a short secret_file: %v", err)
	}
}

// TestEncrypt encrypts as a player decrypts METHOD=AES-128: CBC under the
// IV of the segment's number, PKCS#7 padded, a whole block of padding for a
// segment of whole blocks; in place when there is room.
func TestEncrypt(t *testing.T) {
	key := [16]byte{1, 2, 3}
	for _, n := range []int{0, 1, 15, 16, 17, 188 * 7} {
		plain := bytes.Repeat([]byte{0x47}, n)
		seg := make([]byte, n, n+16)
		copy(seg, plain)
		enc := Encrypt(key, 42, seg)
		if len(enc) != (n/16+1)*16 || &enc[0] != &seg[:1][0] {
			t.Fatalf("%d bytes: %d encrypted, in place %t", n, len(enc), &enc[0] == &seg[:1][0])
		}
		block, _ := aes.NewCipher(key[:])
		var iv [16]byte
		binary.BigEndian.PutUint64(iv[8:], 42)
		out := make([]byte, len(enc))
		cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(out, enc)
		pad := int(out[len(out)-1])
		if pad < 1 || pad > 16 || !bytes.Equal(out[:len(out)-pad], plain) {
			t.Fatalf("%d bytes: decrypted into %x", n, out)
		}
	}
}

// BenchmarkEncrypt is what encrypting a segment costs, by its size: a
// second of 2 Mbit/s video is 256 KiB, 4s of it 1 MiB, 4s of 8 Mbit/s
// 4 MiB. copy is the channel's, which encrypts a copy of the Muxer's
// segment; in place is vod's, encrypting what it has just muxed.
func BenchmarkEncrypt(b *testing.B) {
	k, _ := NewKeys("films", secret)
	for _, size := range []int{256 << 10, 1 << 20, 4 << 20} {
		src := make([]byte, size)
		for _, how := range []string{"in-place", "copy"} {
			b.Run(fmt.Sprintf("%dKiB/%s", size>>10, how), func(b *testing.B) {
				buf := make([]byte, size, size+16)
				b.SetBytes(int64(size))
				b.ReportAllocs()
				for b.Loop() {
					key := k.Key(KeyItem, "films/bbb", 0)
					if how == "copy" {
						out := make([]byte, size, size+16)
						copy(out, src)
						Encrypt(key, 7, out)
						continue
					}
					Encrypt(key, 7, buf[:size])
				}
			})
		}
	}
}

// TestLibraryKeys gives a library the keys of its secret, and new ones
// when a reload changes it.
func TestLibraryKeys(t *testing.T) {
	key := new(fib.Engine)
	root := t.TempDir()
	if err := Apply(key, []config.MediaLibrary{{Name: "lib", Roots: []string{root}}}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Remove(key) })
	l := Lookup(key, "lib")
	if l.Keys() != nil {
		t.Fatal("keys without a secret")
	}
	if err := Apply(key, []config.MediaLibrary{{Name: "lib", Roots: []string{root}, Secret: secret}}, nil); err != nil {
		t.Fatal(err)
	}
	want, _ := NewKeys("lib", secret)
	if l.Keys().ID() != want.ID() {
		t.Fatalf("%v", l.Keys())
	}
	err := Apply(key, []config.MediaLibrary{{Name: "lib", Roots: []string{root}, SecretFile: filepath.Join(root, "missing")}}, nil)
	if err == nil || l.Keys().ID() != want.ID() {
		t.Fatalf("a secret_file missing: %v, keys %v", err, l.Keys())
	}
}
