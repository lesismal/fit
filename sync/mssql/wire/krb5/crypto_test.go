package krb5

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// TestNfold checks n-fold against RFC 3961's vectors (A.1).
func TestNfold(t *testing.T) {
	for _, tc := range []struct {
		in   string
		bits int
		want string
	}{
		{"012345", 64, "be072631276b1955"},
		{"password", 56, "78a07b6caf85fa"},
		{"Rough Consensus, and Running Code", 64, "bb6ed30870b7f0e0"},
		{"password", 168, "59e4a8ca7c0385c3c37b3f6d2000247cb6e6bd5b3e"},
		{"MASSACHVSETTS INSTITVTE OF TECHNOLOGY", 192, "db3b0d8f0b061e603282b308a50841229ad798fab9540c1b"},
		{"Q", 168, "518a54a215a8452a518a54a215a8452a518a54a215"},
		{"ba", 168, "fb25d531ae8974499f52fd92ea9857c4ba24cf297e"},
		{"kerberos", 64, "6b65726265726f73"},
		{"kerberos", 128, "6b65726265726f737b9b5b2b93132b93"},
		{"kerberos", 168, "8372c236344e5f1550cd0747e15d62ca7a5a3bcea4"},
		{"kerberos", 256, "6b65726265726f737b9b5b2b93132b935c9bdcdad95c9899c4cae4dee6d6cae4"},
	} {
		if got := hex.EncodeToString(nfold([]byte(tc.in), tc.bits/8)); got != tc.want {
			t.Errorf("%d-fold(%q) = %s, want %s", tc.bits, tc.in, got, tc.want)
		}
	}
}

// TestStringToKey checks the keys of RFC 3962's vectors (B).
func TestStringToKey(t *testing.T) {
	for _, tc := range []struct {
		iter       int
		pass, salt string
		k128, k256 string
	}{
		{1, "password", "ATHENA.MIT.EDUraeburn", "42263c6e89f4fc28b8df68ee09799f15", "fe697b52bc0d3ce14432ba036a92e65bbb52280990a2fa27883998d72af30161"},
		{2, "password", "ATHENA.MIT.EDUraeburn", "c651bf29e2300ac27fa469d693bdda13", "a2e16d16b36069c135d5e9d2e25f896102685618b95914b467c67622225824ff"},
		{1200, "password", "ATHENA.MIT.EDUraeburn", "4c01cd46d632d01e6dbe230a01ed642a", "55a6ac740ad17b4846941051e1e8b0a7548d93b0ab30a8bc3ff16280382b8c2a"},
	} {
		k, err := StringToKey(ETypeAES128, tc.pass, tc.salt, tc.iter)
		if err != nil || hex.EncodeToString(k.Value) != tc.k128 {
			t.Errorf("aes128, %d iterations: %x %v, want %s", tc.iter, k.Value, err, tc.k128)
		}
		k, err = StringToKey(ETypeAES256, tc.pass, tc.salt, tc.iter)
		if err != nil || hex.EncodeToString(k.Value) != tc.k256 {
			t.Errorf("aes256, %d iterations: %x %v, want %s", tc.iter, k.Value, err, tc.k256)
		}
	}
	if _, err := StringToKey(ETypeAES128, "p", "s", maxIterations+1); err == nil {
		t.Fatal("too many iterations")
	}
	if _, err := StringToKey(23, "p", "s", 0); err != errEType {
		t.Fatal(err)
	}
}

// TestCTS checks AES-CTS against RFC 3962's vectors (B), key "chicken
// teriyaki", and that every length from a block to four goes round.
func TestCTS(t *testing.T) {
	key := []byte("chicken teriyaki")
	plain := []byte("I would like the General Gau's Chicken, please, and wonton soup.")
	for _, tc := range []struct {
		n    int
		want string
	}{
		{17, "c6353568f2bf8cb4d8a580362da7ff7f97"},
		{31, "fc00783e0efdb2c1d445d4c8eff7ed2297687268d6ecccc0c07b25e25ecfe5"},
		{32, "39312523a78662d5be7fcbcc98ebf5a897687268d6ecccc0c07b25e25ecfe584"},
		{47, "97687268d6ecccc0c07b25e25ecfe584b3fffd940c16a18c1b5549d2f838029e39312523a78662d5be7fcbcc98ebf5"},
		{48, "97687268d6ecccc0c07b25e25ecfe5849dad8bbb96c4cdc03bc103e1a194bbd839312523a78662d5be7fcbcc98ebf5a8"},
		{64, "97687268d6ecccc0c07b25e25ecfe58439312523a78662d5be7fcbcc98ebf5a84807efe836ee89a526730dbc2f7bc8409dad8bbb96c4cdc03bc103e1a194bbd8"},
	} {
		got, err := ctsEncrypt(key, plain[:tc.n])
		if err != nil || hex.EncodeToString(got) != tc.want {
			t.Errorf("%d bytes: %x %v, want %s", tc.n, got, err, tc.want)
		}
		back, err := ctsDecrypt(key, got)
		if err != nil || !bytes.Equal(back, plain[:tc.n]) {
			t.Errorf("%d bytes back: %q %v", tc.n, back, err)
		}
	}
	for n := 16; n <= 64; n++ {
		ct, err := ctsEncrypt(key, plain[:n])
		if err != nil {
			t.Fatal(err)
		}
		if back, err := ctsDecrypt(key, ct); err != nil || !bytes.Equal(back, plain[:n]) {
			t.Fatalf("%d: %q %v", n, back, err)
		}
	}
	if _, err := ctsEncrypt(key, plain[:15]); err == nil {
		t.Fatal("15 bytes encrypted")
	}
	if _, err := ctsDecrypt(key, plain[:15]); err == nil {
		t.Fatal("15 bytes decrypted")
	}
	if _, err := ctsEncrypt([]byte("short"), plain); err == nil {
		t.Fatal("a 5-byte key")
	}
	if _, err := ctsDecrypt([]byte("short"), plain); err == nil {
		t.Fatal("a 5-byte key")
	}
}

// TestEncrypt round-trips a message of each type and refuses one changed.
func TestEncrypt(t *testing.T) {
	for _, et := range []int32{ETypeAES128, ETypeAES256} {
		k, _ := StringToKey(et, "pw", "REALMuser", 0)
		for _, n := range []int{0, 1, 16, 33} {
			msg := bytes.Repeat([]byte{'m'}, n)
			ct, err := encrypt(k, 7, msg)
			if err != nil {
				t.Fatal(err)
			}
			back, err := decrypt(k, 7, ct)
			if err != nil || !bytes.Equal(back, msg) {
				t.Fatal(et, n, back, err)
			}
			if _, err := decrypt(k, 8, ct); err == nil {
				t.Fatal("another usage decrypted")
			}
			ct[0] ^= 1
			if _, err := decrypt(k, 7, ct); err == nil {
				t.Fatal("a changed message decrypted")
			}
		}
		if _, err := decrypt(k, 7, make([]byte, 20)); err == nil {
			t.Fatal("a short message")
		}
		c1, _ := checksum(k, 6, []byte("x"))
		c2, _ := checksum(k, 6, []byte("y"))
		if len(c1) != 12 || bytes.Equal(c1, c2) {
			t.Fatal(c1, c2)
		}
	}
	bad := Key{EType: 23, Value: make([]byte, 16)}
	if _, err := encrypt(bad, 1, nil); err != errEType {
		t.Fatal(err)
	}
	if _, err := decrypt(bad, 1, nil); err != errEType {
		t.Fatal(err)
	}
	if _, err := checksum(bad, 1, nil); err != errEType {
		t.Fatal(err)
	}
	short := Key{EType: ETypeAES128, Value: []byte("short")}
	if _, err := encrypt(short, 1, nil); err == nil {
		t.Fatal("a short key")
	}
	if _, err := decrypt(short, 1, make([]byte, 40)); err == nil {
		t.Fatal("a short key")
	}
	if _, err := checksum(short, 1, nil); err == nil {
		t.Fatal("a short key")
	}
}
