package syncmssql

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
	"github.com/lesismal/fit/sync/mssql/wire/krb5"
)

// certFile writes a PEM file of an RSA certificate and its key.
func certFile(t *testing.T) string {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fit"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	path := filepath.Join(t.TempDir(), "sp.pem")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	b = append(b, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})...)
	os.WriteFile(path, b, 0o600)
	return path
}

// TestAuthParams: each method's params make its login, the others'
// params and missing ones are refused.
func TestAuthParams(t *testing.T) {
	t.Setenv("FIT_TEST_MSSQL_PASSWORD", "pw")
	t.Setenv("FIT_TEST_MSSQL_SECRET", "s3cret")
	t.Setenv("FIT_TEST_MSSQL_TOKEN", " tok-env \n")
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "token")
	os.WriteFile(tokFile, []byte("tok-file\n"), 0o600)
	conf := filepath.Join(dir, "krb5.conf")
	os.WriteFile(conf, []byte("[libdefaults]\n default_realm = FIT.TEST\n[realms]\n FIT.TEST = {\n  kdc = kdc.fit.test\n }\n"), 0o600)
	kt := filepath.Join(dir, "u.keytab")
	os.WriteFile(kt, []byte{5, 2}, 0o600)
	cert := certFile(t)
	entra := func(a map[string]any) map[string]any {
		return map[string]any{"username": "", "encrypt": "on", "auth": a}
	}
	// Good ones.
	for _, tc := range []struct {
		p     map[string]any
		check func(s *Source) bool
	}{
		{entra(map[string]any{"method": "entra", "token_env": "FIT_TEST_MSSQL_TOKEN"}), func(s *Source) bool {
			tok, err := s.cfg.AccessToken(context.Background())
			return err == nil && tok == "tok-env" && s.cfg.User == ""
		}},
		{entra(map[string]any{"method": "entra", "token_file": tokFile}), func(s *Source) bool {
			tok, err := s.cfg.AccessToken(context.Background())
			return err == nil && tok == "tok-file"
		}},
		{entra(map[string]any{"method": "entra", "tenant_id": "t", "client_id": "c", "client_secret_env": "FIT_TEST_MSSQL_SECRET", "authority": "https://login.microsoftonline.us"}), func(s *Source) bool {
			return s.cfg.AccessToken != nil
		}},
		{entra(map[string]any{"method": "entra", "tenant_id": "t", "client_id": "c", "client_certificate_file": cert}), func(s *Source) bool { return s.cfg.AccessToken != nil }},
		{entra(map[string]any{"method": "entra", "managed_identity": true}), func(s *Source) bool { return s.cfg.AccessToken != nil }},
		{map[string]any{"username": "fituser", "password_env": "FIT_TEST_MSSQL_PASSWORD", "auth": map[string]any{"method": "kerberos", "realm": "FIT.TEST", "kdc": []string{"k1"}, "spn": "MSSQLSvc/db.fit.test:1433"}}, func(s *Source) bool {
			in, ok := s.cfg.SSPI().(*krb5.Initiator)
			return ok && in.Client.Realm == "FIT.TEST" && in.Client.User == "fituser" && in.Client.Password == "pw" && s.cfg.SPN == "MSSQLSvc/db.fit.test:1433" && s.cfg.User == "" && s.cfg.Password == ""
		}},
		{map[string]any{"username": "fituser@FIT.TEST", "auth": map[string]any{"method": "kerberos", "krb5_conf": conf, "keytab_file": kt}}, func(s *Source) bool {
			in := s.cfg.SSPI().(*krb5.Initiator)
			return in.Client.Realm == "FIT.TEST" && in.Client.User == "fituser" && in.Client.Keytab != nil && in.Client.KDCs[0] == "kdc.fit.test"
		}},
		{map[string]any{"username": `DOM\u`, "password_env": "FIT_TEST_MSSQL_PASSWORD", "auth": map[string]any{"method": "ntlm"}}, func(s *Source) bool {
			n, ok := s.cfg.SSPI().(*mssqlwire.NTLM)
			return ok && n.Domain == "DOM" && n.User == "u" && n.Password == "pw" && s.cfg.User == ""
		}},
	} {
		s, err := provision(t, tc.p)
		if err != nil || !tc.check(s) {
			t.Fatalf("%v: %v", tc.p, err)
		}
	}
	os.Remove(tokFile)
	s, _ := provision(t, entra(map[string]any{"method": "entra", "token_file": tokFile}))
	if _, err := s.cfg.AccessToken(context.Background()); err == nil {
		t.Fatal("a token of no file")
	}
	t.Setenv("FIT_TEST_MSSQL_TOKEN", "")
	s, _ = provision(t, entra(map[string]any{"method": "entra", "token_env": "FIT_TEST_MSSQL_TOKEN"}))
	if _, err := s.cfg.AccessToken(context.Background()); err == nil {
		t.Fatal("a token of no variable")
	}
	// Bad ones, each with what the error names.
	for _, tc := range []struct {
		p    map[string]any
		want string
	}{
		{map[string]any{"username": ""}, "SQL login is needed"},
		{map[string]any{"auth": map[string]any{"method": "basic"}}, "is not one of"},
		{map[string]any{"auth": map[string]any{"method": "ntlm", "realm": "R"}}, "not a param of method ntlm"},
		{map[string]any{"auth": map[string]any{"method": "ntlm"}}, "needs password_env"},
		{map[string]any{"username": "", "password_env": "FIT_TEST_MSSQL_PASSWORD", "auth": map[string]any{"method": "ntlm"}}, "needs username"},
		{map[string]any{"auth": map[string]any{"method": "entra", "token_env": "FIT_TEST_MSSQL_TOKEN"}}, "takes no username"},
		{map[string]any{"username": "", "encrypt": "off", "auth": map[string]any{"method": "entra", "token_env": "X"}}, "needs encrypt on"},
		{entra(map[string]any{"method": "entra"}), "not 0"},
		{entra(map[string]any{"method": "entra", "token_env": "X", "token_file": "y"}), "not 2"},
		{entra(map[string]any{"method": "entra", "token_env": "1x"}), "token_env"},
		{entra(map[string]any{"method": "entra", "client_secret_env": "FIT_TEST_MSSQL_SECRET"}), "needs tenant_id and client_id"},
		{entra(map[string]any{"method": "entra", "tenant_id": "t", "client_id": "c", "client_secret_env": "1x"}), "client_secret_env"},
		{entra(map[string]any{"method": "entra", "tenant_id": "t", "client_id": "c", "client_secret_env": "FIT_TEST_MSSQL_NONE"}), "not set"},
		{entra(map[string]any{"method": "entra", "tenant_id": "t", "client_id": "c", "client_certificate_file": tokFile}), "client_certificate_file"},
		{map[string]any{"auth": map[string]any{"method": "kerberos", "realm": "R", "kdc": []string{"k"}}}, "needs keytab_file or password_env"},
		{map[string]any{"password_env": "FIT_TEST_MSSQL_PASSWORD", "auth": map[string]any{"method": "kerberos", "realm": "R", "kdc": []string{"k"}, "keytab_file": kt}}, "not both"},
		{map[string]any{"auth": map[string]any{"method": "kerberos", "realm": "R", "kdc": []string{"k"}, "keytab_file": filepath.Join(dir, "none")}}, "none"},
		{map[string]any{"auth": map[string]any{"method": "kerberos", "krb5_conf": filepath.Join(dir, "none")}}, "krb5.conf"},
		{map[string]any{"auth": map[string]any{"method": "kerberos", "krb5_conf": conf, "realm": "OTHER"}}, "realm and its KDCs"},
		{map[string]any{"auth": map[string]any{"method": "kerberos", "kdc": []string{"k"}}}, "krb5.conf"},
	} {
		_, err := provision(t, tc.p)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.p, err, tc.want)
		}
	}
}

// TestAuthSink: the sink takes auth too.
func TestAuthSink(t *testing.T) {
	t.Setenv("FIT_TEST_MSSQL_PASSWORD", "pw")
	s, err := provisionSink(t, map[string]any{"username": `DOM\u`, "password_env": "FIT_TEST_MSSQL_PASSWORD", "auth": map[string]any{"method": "ntlm"}})
	if err != nil || s.cfg.SSPI == nil {
		t.Fatal(err)
	}
}
