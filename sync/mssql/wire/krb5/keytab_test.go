package krb5

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/cryptobyte"
)

// keytabEntry is a keytab entry's bytes, with a 32-bit version when vno32.
func keytabEntry(realm string, parts []string, kvno uint8, k Key, vno32 uint32) []byte {
	str := func(b []byte, s string) []byte {
		b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
		return append(b, s...)
	}
	b := binary.BigEndian.AppendUint16(nil, uint16(len(parts)))
	b = str(b, realm)
	for _, p := range parts {
		b = str(b, p)
	}
	b = binary.BigEndian.AppendUint32(b, 1)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, kvno)
	b = binary.BigEndian.AppendUint16(b, uint16(k.EType))
	b = binary.BigEndian.AppendUint16(b, uint16(len(k.Value)))
	b = append(b, k.Value...)
	if vno32 != 0 {
		b = binary.BigEndian.AppendUint32(b, vno32)
	}
	return b
}

func keytabFile(entries ...[]byte) []byte {
	b := []byte{5, 2}
	for _, e := range entries {
		b = binary.BigEndian.AppendUint32(b, uint32(len(e)))
		b = append(b, e...)
	}
	return b
}

// TestKeytab reads entries, a hole, 8- and 32-bit versions, picks a
// principal's newest key of a type, and refuses malformed files.
func TestKeytab(t *testing.T) {
	k1 := Key{EType: ETypeAES256, Value: make([]byte, 32)}
	k2 := Key{EType: ETypeAES256, Value: append(make([]byte, 31), 2)}
	k3 := Key{EType: ETypeAES128, Value: make([]byte, 16)}
	file := keytabFile(keytabEntry("R", []string{"svc", "h"}, 1, k1, 0), keytabEntry("R", []string{"svc", "h"}, 255, k2, 300), keytabEntry("R", []string{"u"}, 3, k3, 0))
	// A hole between them.
	file = append(file, 0xff, 0xff, 0xff, 0xfc, 0, 0, 0, 0)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.keytab")
	os.WriteFile(path, file, 0o600)
	kt, err := LoadKeytab(path)
	if err != nil || len(kt.Entries) != 3 {
		t.Fatal(kt, err)
	}
	if k, ok := kt.Key("R", []string{"svc", "h"}, ETypeAES256); !ok || k.Value[31] != 2 || kt.Entries[1].KVNO != 300 {
		t.Fatal(k, ok)
	}
	if _, ok := kt.Key("R", []string{"svc", "h"}, ETypeAES128); ok {
		t.Fatal("a key of another type")
	}
	if _, err := LoadKeytab(filepath.Join(dir, "none")); err == nil {
		t.Fatal("a missing file")
	}
	os.WriteFile(path, file[:len(file)-12], 0o600)
	if _, err := LoadKeytab(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatal(err)
	}
	if _, err := ParseKeytab([]byte{5, 1}); err == nil || !strings.Contains(err.Error(), "0x501") {
		t.Fatal(err)
	}
	if _, err := ParseKeytab([]byte{4}); err == nil {
		t.Fatal("not a keytab")
	}
	// Every prefix of an entry is refused.
	e := keytabEntry("R", []string{"svc", "h"}, 1, k1, 0)
	for n := 0; n < len(e); n++ {
		if _, err := parseEntry(e[:n]); err == nil {
			t.Fatalf("a %d-byte entry", n)
		}
	}
	if _, err := ParseKeytab(append([]byte{5, 2}, 0, 0, 0, 9, 1)); err == nil {
		t.Fatal("an entry past the end")
	}
	if _, err := ParseKeytab(append([]byte{5, 2}, 0xff, 0xff, 0xff, 0xf0, 1)); err == nil {
		t.Fatal("a hole past the end")
	}
}

// TestConfig reads a krb5.conf's default realm and KDCs.
func TestConfig(t *testing.T) {
	text := `# a comment
[libdefaults]
	default_realm = FIT.TEST ; and a note
	dns_lookup_kdc = false
[realms]
	FIT.TEST = {
		kdc = kdc1.fit.test:88
		kdc = kdc2.fit.test
		admin_server = kdc1.fit.test
	}
	OTHER = {
		kdc = o
	}
	junk line
[domain_realm]
	kdc = not.a.realm
`
	path := filepath.Join(t.TempDir(), "krb5.conf")
	os.WriteFile(path, []byte(text), 0o600)
	c, err := LoadConfig(path)
	if err != nil || c.DefaultRealm != "FIT.TEST" || strings.Join(c.KDCs["FIT.TEST"], ",") != "kdc1.fit.test:88,kdc2.fit.test" || len(c.KDCs["OTHER"]) != 1 || len(c.KDCs) != 2 {
		t.Fatal(c, err)
	}
	if _, err := LoadConfig(path + ".none"); err == nil {
		t.Fatal("a missing file")
	}
}

// TestPrefixes feeds every prefix of good messages to their readers:
// each is refused, none panics.
func TestPrefixes(t *testing.T) {
	k := newKDC(t, ETypeAES256)
	in := &Initiator{Client: k.client()}
	tok, err := in.Start(context.Background(), "MSSQLSvc/db.fit.test:1433")
	if err != nil {
		t.Fatal(err)
	}
	acc := &Acceptor{Keytab: k.svcKeytab()}
	resp, _, err := acc.Accept(tok)
	if err != nil {
		t.Fatal(err)
	}
	_, rf := unwrapResp(t, resp)
	_, apRep, _ := unframe(rf)
	for n := 0; n < len(tok); n++ {
		if _, _, err := acc.Accept(tok[:n]); err == nil {
			t.Fatalf("a %d-byte token accepted", n)
		}
	}
	for n := 0; n < len(apRep)-2; n++ {
		in.done = false
		if err := in.checkAPRep(apRep[2 : 2+n]); err == nil {
			t.Fatalf("a %d-byte AP-REP", n)
		}
	}
	e := krbError(25, k.etypeInfo2())
	for n := 0; n < len(e); n++ {
		readError(e[:n])
	}
	info := k.etypeInfo2()
	pa, _ := readPAData(cryptobyte.String(info))
	for n := 0; n < len(pa[0].value); n++ {
		if _, ok := readETypeInfo2(pa[0].value[:n]); ok && n > 0 {
			t.Fatalf("a %d-byte ETYPE-INFO2", n)
		}
	}
	for n := 0; n < len(info); n++ {
		readPAData(cryptobyte.String(info[:n]))
	}
	// AS-REPs and TGS-REPs cut short, or of a wrong key.
	cname := Principal{Type: 1, Parts: []string{"fituser"}}
	sname := Principal{Type: 2, Parts: []string{"krbtgt", fakeRealm}}
	sk := k.sessionKey()
	end := time.Now().Add(time.Hour)
	tkt := ticketFor(k.tgtKey, sname, cname, sk, end)
	good := rep(appASRep, appEncASRepPart, usageASRepPart, k.userKey, cname, sname, tkt, sk, 7, end, k.etypeInfo2())
	c := k.client()
	if _, err := c.asRep(good, 7, nil); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(good); n++ {
		if _, err := c.asRep(good[:n], 7, nil); err == nil {
			t.Fatalf("a %d-byte AS-REP", n)
		}
	}
	other := rep(appASRep, appEncASRepPart, usageASRepPart, k.tgtKey, cname, sname, tkt, sk, 7, end, nil)
	if _, err := c.asRep(other, 7, []etypeInfo{{etype: ETypeAES256, salt: k.salt, hasSalt: true}}); err == nil {
		t.Fatal("a part of another key")
	}
	aes128, _ := StringToKey(ETypeAES128, "pw", k.salt, 0)
	if _, err := c.asRep(rep(appASRep, appEncASRepPart, usageASRepPart, aes128, cname, sname, tkt, sk, 7, end, nil), 7, nil); err != nil {
		t.Fatal(err)
	}
	// A session key that is not AES; a part of another tag.
	bad := Key{EType: 23, Value: make([]byte, 16)}
	if _, err := c.asRep(rep(appASRep, appEncASRepPart, usageASRepPart, k.userKey, cname, sname, tkt, bad, 7, end, nil), 7, nil); err == nil {
		t.Fatal("an RC4 session key")
	}
	if _, err := c.asRep(rep(appASRep, appEncAPRepPart, usageASRepPart, k.userKey, cname, sname, tkt, sk, 7, end, nil), 7, nil); err == nil {
		t.Fatal("an EncAPRepPart")
	}
	// The TGS's answers cut short.
	c = k.client()
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err != nil {
		t.Fatal(err)
	}
	svc := Principal{Type: 2, Parts: []string{"MSSQLSvc", "db.fit.test:1433"}}
	st := ticketFor(k.svcKey, svc, cname, sk, end)
	tgsRep := rep(appTGSRep, appEncTGSRepPart, usageTGSRepPart, c.tgt.Key, cname, svc, st, sk, 9, end, nil)
	for n := 0; n < len(tgsRep); n += 7 {
		cut := tgsRep[:n]
		k.answer = func(tag int, req []byte) []byte {
			if tag == appTGSReq {
				return cut
			}
			return nil
		}
		delete(c.tickets, "MSSQLSvc/db.fit.test:1433")
		if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err == nil {
			t.Fatalf("a %d-byte TGS-REP", n)
		}
	}
	k.answer = nil
	// An expired TGT is got again; the clock's default.
	c.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	delete(c.tickets, "MSSQLSvc/db.fit.test:1433")
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err != nil {
		t.Fatal(err)
	}
	// A TGT whose key is not AES cannot ask.
	c.tgt.Key = bad
	c.tgt.End = time.Now().Add(10 * time.Hour)
	delete(c.tickets, "MSSQLSvc/db.fit.test:1433")
	if _, err := c.ServiceTicket(context.Background(), "MSSQLSvc/db.fit.test:1433"); err == nil {
		t.Fatal("an RC4 TGT")
	}
	if (&Client{}).clock().IsZero() {
		t.Fatal("no clock")
	}
}
