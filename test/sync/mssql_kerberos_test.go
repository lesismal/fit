package synctest

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	"github.com/lesismal/fit/sync/mssql/wire/krb5"
)

// krb5Env is mssql.sh's KDC: its address and the keytabs' directory.
func krb5Env(t *testing.T) (kdc, keytabs string) {
	t.Helper()
	kdc, keytabs = os.Getenv("FIT_SYNC_KRB5_KDC"), os.Getenv("FIT_SYNC_KRB5_KEYTABS")
	if kdc == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" && os.Getenv("FIT_SYNC_MSSQL") != "" {
			t.Fatal("FIT_SYNC_KRB5_KDC is not set")
		}
		t.Skip("no KDC: FIT_SYNC_KRB5_KDC is not set (test/sync/mssql.sh sets it)")
	}
	return kdc, keytabs
}

// The principals of mssql.sh's KDC (test/sync/krb5/kdc.sh).
const (
	krb5Realm    = "FIT.TEST"
	krb5Password = "Fit-krb5-pw"
	krb5SPN      = "MSSQLSvc/mssql.fit.test:1433"
)

// krb5Clients are a client of fituser's password (pre-authentication
// required) and one of fitkt's keytab.
func krb5Clients(t *testing.T, kdc, keytabs string) map[string]*krb5.Client {
	t.Helper()
	kt, err := krb5.LoadKeytab(filepath.Join(keytabs, "fitkt.keytab"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]*krb5.Client{
		"password": {Realm: krb5Realm, User: "fituser", Password: krb5Password, KDCs: []string{kdc}},
		"keytab":   {Realm: krb5Realm, User: "fitkt", Keytab: kt, KDCs: []string{kdc}},
	}
}

// checkedSSPI counts the server tokens the initiator took.
type checkedSSPI struct {
	in      *krb5.Initiator
	checked int
}

func (c *checkedSSPI) Start(ctx context.Context, spn string) ([]byte, error) {
	return c.in.Start(ctx, spn)
}
func (c *checkedSSPI) Next(ctx context.Context, b []byte) ([]byte, error) {
	out, err := c.in.Next(ctx, b)
	if err == nil {
		c.checked++
	}
	return out, err
}

// TestMSSQLKerberosKDC logs in with Kerberos tickets of mssql.sh's MIT KDC
// (a password with pre-authentication, and a keytab) to a TDS server in
// the test that accepts them with the SPN's keytab and answers the AP-REP,
// which the client checks (mutual authentication).
func TestMSSQLKerberosKDC(t *testing.T) {
	kdc, keytabs := krb5Env(t)
	svc, err := krb5.LoadKeytab(filepath.Join(keytabs, "mssql.keytab"))
	if err != nil {
		t.Fatal(err)
	}
	for name, kc := range krb5Clients(t, kdc, keytabs) {
		t.Run(name, func(t *testing.T) {
			addr, who := fakeKerberosServer(t, &krb5.Acceptor{Keytab: svc})
			sspi := &checkedSSPI{in: &krb5.Initiator{Client: kc}}
			c, err := mssqlwire.Dial(context.Background(), mssqlwire.Config{Addr: addr, Encrypt: mssqlwire.EncryptOff, SPN: krb5SPN,
				SSPI: func() mssqlwire.SSPI { return sspi }, Timeout: 30 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			if got := <-who; got != kc.User+"@"+krb5Realm || sspi.checked != 1 {
				t.Fatalf("the server saw %q, the client checked %d AP-REPs", got, sspi.checked)
			}
		})
	}
}

// TestMSSQLKerberosServer sends SQL Server 2022 (given the SPN's keytab by
// mssql.sh) a ticket of the KDC: it takes it and answers the AP-REP, which
// the client checks, then refuses the login (18452, an untrusted domain):
// with no Active Directory, SQL Server on Linux has no Windows login to
// map the principal to. A login through to the end needs AD (or Samba's)
// and is not tested.
func TestMSSQLKerberosServer(t *testing.T) {
	kdc, keytabs := krb5Env(t)
	srv := os.Getenv("FIT_SYNC_MSSQL_KRB5")
	_, addr, ok := strings.Cut(srv, "=")
	if !ok {
		t.Skip("no SQL Server with the SPN's keytab: FIT_SYNC_MSSQL_KRB5 is not set")
	}
	for name, kc := range krb5Clients(t, kdc, keytabs) {
		t.Run(name, func(t *testing.T) {
			sspi := &checkedSSPI{in: &krb5.Initiator{Client: kc}}
			_, err := mssqlwire.Dial(context.Background(), mssqlwire.Config{Addr: addr, Encrypt: mssqlwire.EncryptOff, SPN: krb5SPN, Database: "master",
				SSPI: func() mssqlwire.SSPI { return sspi }, Timeout: time.Minute})
			if err == nil || !strings.Contains(err.Error(), "18452") || sspi.checked != 1 {
				t.Fatalf("%v; the client checked %d AP-REPs", err, sspi.checked)
			}
		})
	}
}

// fakeKerberosServer is a TDS server of one login: no encryption, LOGIN7's
// SSPI token taken by acc, its answer sent with a LOGINACK. The client's
// principal comes on who.
func fakeKerberosServer(t *testing.T, acc *krb5.Acceptor) (string, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	who := make(chan string, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		nc.SetDeadline(time.Now().Add(time.Minute))
		read := func() []byte {
			var out []byte
			for {
				h := make([]byte, 8)
				if _, err := io.ReadFull(nc, h); err != nil {
					return nil
				}
				b := make([]byte, int(binary.BigEndian.Uint16(h[2:]))-8)
				if _, err := io.ReadFull(nc, b); err != nil {
					return nil
				}
				out = append(out, b...)
				if h[1]&1 != 0 {
					return out
				}
			}
		}
		send := func(b []byte) {
			h := []byte{4, 1, 0, 0, 0, 0, 1, 0}
			binary.BigEndian.PutUint16(h[2:], uint16(8+len(b)))
			nc.Write(append(h, b...))
		}
		if read() == nil {
			who <- ""
			return
		}
		// VERSION, then ENCRYPTION not supported.
		send([]byte{0, 0, 11, 0, 6, 1, 0, 17, 0, 1, 0xff, 16, 0, 0x10, 0x2d, 0, 0, 2})
		login := read()
		if len(login) < 94 || login[25]&0x80 == 0 {
			who <- ""
			return
		}
		ib, cb := int(binary.LittleEndian.Uint16(login[78:])), int(binary.LittleEndian.Uint16(login[80:]))
		reply, client, err := acc.Accept(login[ib : ib+cb])
		if err != nil {
			t.Error(err)
			who <- ""
			return
		}
		who <- client
		var b []byte
		b = append(b, 0xed)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(reply)))
		b = append(b, reply...)
		// LOGINACK: interface, TDS 7.4, the program's name, its version.
		ack := []byte{1, 0x74, 0, 0, 4, 4}
		for _, r := range "fake" {
			ack = append(ack, byte(r), 0)
		}
		ack[5] = 4
		ack = append(ack, 16, 0, 0, 0)
		b = append(b, 0xad)
		b = binary.LittleEndian.AppendUint16(b, uint16(len(ack)))
		b = append(b, ack...)
		b = append(b, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		send(b)
		read()
	}()
	return ln.Addr().String(), who
}
