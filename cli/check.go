package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/config"
)

// checkResult is GET /config/check's answer, the parts fit check reads: the
// running config's version and ETag, for If-Match, and the differences
// between what fit runs and the config it runs from.
type checkResult struct {
	Version     uint64       `json:"version"`
	ETag        string       `json:"etag"`
	CheckedAt   time.Time    `json:"checked_at"`
	Differences []difference `json:"differences"`
	Summary     checkSummary `json:"summary"`
}

// checkSummary is how many differences are drift, which is an inconsistency
// a fix changes, and how many are info, runtime facts that are no
// inconsistency.
type checkSummary struct {
	Drift int `json:"drift"`
	Info  int `json:"info"`
}

// difference is one way what fit runs and the config it runs from disagree,
// or one runtime fact that only looks like a disagreement.
type difference struct {
	ID       string          `json:"id"`
	Category string          `json:"category"` // settings, listeners, ...
	Kind     string          `json:"kind"`     // drift or info
	Name     string          `json:"name"`
	Detail   string          `json:"detail"`
	Config   json.RawMessage `json:"config,omitempty"`
	Running  json.RawMessage `json:"running,omitempty"`
	Fixes    []fix           `json:"fixes,omitempty"`
}

// fix is one way a difference can be settled; the client picks at most one
// of them, or none.
type fix struct {
	Action string       `json:"action"` // add, delete, replace or reapply
	Label  string       `json:"label"`
	Patch  *configPatch `json:"patch,omitempty"`
}

// configPatch is one change to the base config: a JSON pointer and, for add
// and replace, the value there.
type configPatch struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// checkFix is one fix fit check asks the Admin API to apply: which
// difference, and which of its fixes.
type checkFix struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// checkApplied is POST /config/reconcile's answer, as PUT /config's: the
// version the change made, its ETag and the warnings it gave.
type checkApplied struct {
	Version  uint64   `json:"version"`
	ETag     string   `json:"etag"`
	Warnings []string `json:"warnings,omitempty"`
}

// checkError is the Admin API's answer when it refuses a request.
type checkError struct {
	Error string `json:"error"`
}

// checkAdminOf returns where the bootstrap config data, standard JSON, says
// the Admin API listens, the token file its requests carry a token from,
// and its TLS files.
func checkAdminOf(data []byte) (config.Admin, error) {
	adminJSON, _, err := admin.SplitAdmin(data)
	if err != nil {
		return config.Admin{}, err
	}
	if adminJSON == nil {
		return config.Admin{}, errors.New("the config sets no admin: fit check reaches a running fit through its Admin API")
	}
	// Only the address, the token file and the TLS files are read: the rest
	// of admin has fields whose JSON is fit's own (console durations and the
	// like), which plain encoding/json does not read, and fit check has no
	// use for them.
	var a struct {
		Address      string `json:"address"`
		TokenFile    string `json:"token_file"`
		CertFile     string `json:"cert_file"`
		KeyFile      string `json:"key_file"`
		ClientCAFile string `json:"client_ca_file"`
	}
	if err := json.Unmarshal(adminJSON, &a); err != nil {
		return config.Admin{}, fmt.Errorf("admin: %w", err)
	}
	return config.Admin{Address: a.Address, TokenFile: a.TokenFile, CertFile: a.CertFile, KeyFile: a.KeyFile, ClientCAFile: a.ClientCAFile}, nil
}

// checkTLS is how fit check verifies an Admin API served over TLS, and the
// certificate it presents to one that asks for it: the -ca, -server-name,
// -cert and -key flags.
type checkTLS struct {
	CA, ServerName, Cert, Key string
}

// set says whether any of the flags is given.
func (o checkTLS) set() bool { return o != checkTLS{} }

// config returns the TLS config fit check reaches a's Admin API with, over
// TLS 1.2 or later: the server's certificate is verified with the CAs of
// o.CA or, without it, the system's and the certificates of a's own
// cert_file, when fit check can read it, as on the host fit runs on; for
// o.ServerName or, without it, the address's host, localhost when that is
// unspecified, as in ":2019" or "0.0.0.0:2019".
func (o checkTLS) config(a config.Admin, host string) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.ServerName}
	if tc.ServerName == "" {
		tc.ServerName = strings.Trim(host, "[]")
		if ip, err := netip.ParseAddr(tc.ServerName); tc.ServerName == "" || err == nil && ip.IsUnspecified() {
			tc.ServerName = "localhost"
		}
	}
	if o.CA != "" {
		data, err := os.ReadFile(o.CA)
		if err != nil {
			return nil, fmt.Errorf("-ca: %w", err)
		}
		tc.RootCAs = x509.NewCertPool()
		if !tc.RootCAs.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("-ca %s: no PEM certificate found", o.CA)
		}
	} else if data, err := os.ReadFile(a.CertFile); err == nil {
		// The certificate fit serves, trusted as it is: verified against
		// itself, for the name asked for.
		if tc.RootCAs, err = x509.SystemCertPool(); err != nil {
			tc.RootCAs = x509.NewCertPool()
		}
		tc.RootCAs.AppendCertsFromPEM(data)
	}
	if (o.Cert == "") != (o.Key == "") {
		return nil, errors.New("-cert and -key go together")
	}
	if o.Cert != "" {
		cert, err := tls.LoadX509KeyPair(o.Cert, o.Key)
		if err != nil {
			return nil, fmt.Errorf("-cert and -key: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	} else if a.ClientCAFile != "" {
		return nil, errors.New("admin.client_ca_file asks for a client certificate: give it with -cert and -key")
	}
	return tc, nil
}

// checkMaxBytes bounds an answer fit check reads.
const checkMaxBytes = 64 << 20

// checkAPI is the Admin API fit check talks to: its base URL, the client
// that reaches it and the token every request carries.
type checkAPI struct {
	client  *http.Client
	baseURL string
	token   string
}

// newCheckAPI returns the client fit check reaches a's Admin API with: HTTP
// over TCP, or HTTPS, verified as o says, when admin sets cert_file; or, for
// an address under config.AdminUnixPrefix, plain HTTP over the Unix socket
// it names.
func newCheckAPI(a config.Admin, o checkTLS, timeout time.Duration) (*checkAPI, error) {
	api := &checkAPI{client: &http.Client{Timeout: timeout}}
	network, address := a.Network()
	if o.set() && (network == "unix" || !a.TLS()) {
		return nil, errors.New("-ca, -server-name, -cert and -key are for an Admin API served over TLS, and admin sets no cert_file")
	}
	if a.TokenFile != "" {
		data, err := os.ReadFile(a.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("admin.token_file: %w", err)
		}
		api.token = strings.TrimSpace(string(data))
		if api.token == "" {
			return nil, fmt.Errorf("admin.token_file %s is empty", a.TokenFile)
		}
	}
	if network != "unix" {
		host, port, _ := net.SplitHostPort(address)
		if ip, err := netip.ParseAddr(host); host == "" || err == nil && ip.IsUnspecified() {
			// Every address, as ":2019": reached on loopback, as not every
			// system dials an unspecified address.
			loopback := "127.0.0.1"
			if err == nil && ip.Is6() {
				loopback = "::1"
			}
			address = net.JoinHostPort(loopback, port)
		}
		if !a.TLS() {
			api.baseURL = "http://" + address
			return api, nil
		}
		tc, err := o.config(a, host)
		if err != nil {
			return nil, err
		}
		api.baseURL = "https://" + address
		api.client.Transport = &http.Transport{
			TLSClientConfig:     tc,
			TLSHandshakeTimeout: timeout,
			MaxIdleConns:        1,
		}
		return api, nil
	}
	api.baseURL = "http://unix"
	api.client.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", address)
	}}
	return api, nil
}

// close lets go of the connections the check kept.
func (api *checkAPI) close() {
	if transport, ok := api.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

// check makes GET /config/check and returns its answer with the body it came
// in.
func (api *checkAPI) check() (*checkResult, []byte, error) {
	data, status, err := api.do(http.MethodGet, "/config/check", nil, "")
	if err != nil {
		return nil, nil, err
	}
	if status != http.StatusOK {
		return nil, nil, api.refused(status, data)
	}
	var res checkResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, nil, fmt.Errorf("the Admin API's answer is not a check result: %w", err)
	}
	return &res, data, nil
}

// errCheckChanged is the answer to a fix sent with an ETag of a config that
// is no longer the running one: nothing was applied.
var errCheckChanged = errors.New("the running config changed since the check, so nothing was applied: run fit check again")

// reconcile sends the fixes chosen to POST /config/reconcile, with etag as
// the config they were chosen from.
func (api *checkAPI) reconcile(etag string, fixes []checkFix) (*checkApplied, error) {
	// Strings only: marshalling them cannot fail.
	body, _ := json.Marshal(struct {
		Fixes []checkFix `json:"fixes"`
	}{fixes})
	data, status, err := api.do(http.MethodPost, "/config/reconcile", body, etag)
	if err != nil {
		return nil, err
	}
	if status == http.StatusPreconditionFailed {
		return nil, errCheckChanged
	}
	if status != http.StatusOK {
		return nil, api.refused(status, data)
	}
	var applied checkApplied
	if err := json.Unmarshal(data, &applied); err != nil {
		return nil, fmt.Errorf("the Admin API's answer is not an applied config: %w", err)
	}
	return &applied, nil
}

// refused returns the error the Admin API answered a request with.
func (api *checkAPI) refused(status int, body []byte) error {
	var e checkError
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("the Admin API answered %d: %s", status, e.Error)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("the Admin API answered %d: %s", status, msg)
}

// do sends a request to the Admin API and returns the body of its answer,
// whatever its status.
func (api *checkAPI) do(method, path string, body []byte, ifMatch string) ([]byte, int, error) {
	var in io.Reader
	if body != nil {
		in = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, api.baseURL+path, in)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if api.token != "" {
		req.Header.Set("Authorization", "Bearer "+api.token)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	resp, err := api.client.Do(req)
	if err != nil {
		if errors.As(err, new(*tls.CertificateVerificationError)) {
			err = fmt.Errorf("%w: give the CA the Admin API's certificate chains to with -ca, or the name it is for with -server-name", err)
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, checkMaxBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("reading the Admin API's answer: %w", err)
	}
	if int64(len(data)) > checkMaxBytes {
		return nil, 0, fmt.Errorf("the Admin API's answer is larger than %d bytes", checkMaxBytes)
	}
	return data, resp.StatusCode, nil
}

// runCheck asks the running fit the bootstrap config's admin names, through
// its Admin API, whether what it runs agrees with the config it runs from,
// and prints the differences. With -fix it asks, one difference at a time,
// which fix to apply and applies the ones chosen together.
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgPaths configPaths
	fs.Var(&cfgPaths, "c", "the bootstrap config file, or a directory of them, fit was started with, whose admin says where its Admin API is (fit.json when none is given); give -c again for more, which are merged")
	asJSON := fs.Bool("json", false, "print the check's answer as the JSON the Admin API sent, rather than as a listing")
	fix := fs.Bool("fix", false, "ask which fix to apply to each difference, one at a time, and apply the ones chosen together")
	timeout := fs.Duration("timeout", 30*time.Second, "how long fit check waits for the Admin API")
	var tlsOpts checkTLS
	fs.StringVar(&tlsOpts.CA, "ca", "", "with admin.cert_file, a PEM file with the CAs the Admin API's certificate is verified with; the system's and the certificate admin.cert_file holds, when it can be read, when left out")
	fs.StringVar(&tlsOpts.ServerName, "server-name", "", "with admin.cert_file, the name the Admin API's certificate is verified for; the host of admin.address, or localhost when that is unspecified, when left out")
	fs.StringVar(&tlsOpts.Cert, "cert", "", "with admin.client_ca_file, a PEM file with the client certificate fit check presents")
	fs.StringVar(&tlsOpts.Key, "key", "", "the private key of -cert")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "fit check: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *asJSON && *fix {
		fmt.Fprintln(stderr, "fit check: -json prints the check and -fix asks for fixes: give one of them")
		return 2
	}

	paths := cfgPaths.orDefault()
	name := strings.Join(paths, ", ")
	set, _, err := loadSet(paths)
	if err != nil {
		printConfigError(stderr, "fit check", name, err)
		return 2
	}
	a, err := checkAdminOf(set.JSON)
	if err != nil {
		fmt.Fprintf(stderr, "fit check: %v\n", err)
		return 2
	}
	api, err := newCheckAPI(a, tlsOpts, *timeout)
	if err != nil {
		fmt.Fprintf(stderr, "fit check: %v\n", err)
		return 2
	}
	defer api.close()

	res, raw, err := api.check()
	if err != nil {
		fmt.Fprintf(stderr, "fit check: %v\n", err)
		return 2
	}
	if *asJSON {
		_, _ = stdout.Write(raw)
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
		return checkCode(res, checkDrift(res))
	}
	printCheck(stdout, res)
	if !*fix || res.Summary.Drift == 0 {
		return checkCode(res, checkDrift(res))
	}

	chosen, err := askFixes(bufio.NewReader(stdin), stdout, res)
	if err != nil {
		fmt.Fprintf(stderr, "fit check: %v\n", err)
		return 2
	}
	if len(chosen) == 0 {
		fmt.Fprintln(stderr, "fit check: no fix was chosen: the config was not changed")
		return 1
	}
	applied, err := api.reconcile(res.ETag, chosen)
	if err != nil {
		fmt.Fprintf(stderr, "fit check: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "fit check: applied config version %d\n", applied.Version)
	for _, w := range applied.Warnings {
		fmt.Fprintf(stderr, "fit check: warning: %s\n", w)
	}
	res, _, err = api.check()
	if err != nil {
		fmt.Fprintf(stderr, "fit check: checking again: %v\n", err)
		return 2
	}
	fmt.Fprintln(stdout)
	printCheck(stdout, res)
	return checkCode(res, checkDrift(res))
}

// checkCode returns the exit code an answer means: 1 when drift remains, 0
// when only info does.
func checkCode(res *checkResult, drift int) int {
	if drift > 0 || res.Summary.Drift > 0 {
		return 1
	}
	return 0
}

// checkDrift returns how many of an answer's differences are drift.
func checkDrift(res *checkResult) int {
	n := 0
	for _, d := range res.Differences {
		if d.Kind == "drift" {
			n++
		}
	}
	return n
}

// printCheck prints the check's answer: the running config's version and
// when it was checked, how many differences of each kind there are, and each
// difference with its values and fixes.
func printCheck(w io.Writer, res *checkResult) {
	fmt.Fprintf(w, "fit check: config version %d, checked at %s\n", res.Version, res.CheckedAt.Format(time.RFC3339))
	fmt.Fprintf(w, "drift: %d, info: %d\n", res.Summary.Drift, res.Summary.Info)
	for i, d := range res.Differences {
		fmt.Fprintf(w, "[%d] %s %s/%s\n", i+1, d.Kind, d.Category, checkName(d))
		if d.Detail != "" {
			fmt.Fprintf(w, "    %s\n", d.Detail)
		}
		if len(d.Config) > 0 {
			fmt.Fprintf(w, "    %-9s%s\n", "config:", checkValue(d.Config))
		}
		if len(d.Running) > 0 {
			fmt.Fprintf(w, "    %-9s%s\n", "running:", checkValue(d.Running))
		}
		for j, f := range d.Fixes {
			label := ""
			if j == 0 {
				label = "fixes:"
			}
			fmt.Fprintf(w, "    %-9s%d %s: %s\n", label, j+1, f.Action, f.Label)
		}
	}
}

// checkName returns the short name a difference is listed under, its ID
// when it has none.
func checkName(d difference) string {
	if d.Name != "" {
		return d.Name
	}
	return d.ID
}

// checkValue returns a difference's side as one line of JSON.
func checkValue(v json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, v); err != nil {
		return strings.TrimSpace(string(v))
	}
	return b.String()
}

// askFixes asks, for each drift difference, which fix to apply, and returns
// the fixes chosen: a blank answer or "s" skips a difference, and none can
// be chosen for a difference that offers no fix.
func askFixes(in *bufio.Reader, out io.Writer, res *checkResult) ([]checkFix, error) {
	var chosen []checkFix
	for i, d := range res.Differences {
		if d.Kind != "drift" || len(d.Fixes) == 0 {
			continue
		}
		n, err := askFix(in, out, i+1, d)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			chosen = append(chosen, checkFix{ID: d.ID, Action: d.Fixes[n-1].Action})
		}
	}
	return chosen, nil
}

// askFix asks which of a difference's fixes to apply, until the answer is
// one of their numbers, s to skip it or a blank line. It returns the fix's
// number, 0 for skipping.
func askFix(in *bufio.Reader, out io.Writer, num int, d difference) (int, error) {
	for {
		fmt.Fprintf(out, "fix [%d] %s — choose ", num, d.ID)
		for j, f := range d.Fixes {
			if j > 0 {
				fmt.Fprint(out, ", ")
			}
			fmt.Fprintf(out, "%d) %s: %s", j+1, f.Action, f.Label)
			if f.Patch != nil && f.Patch.Path != "" {
				fmt.Fprintf(out, " (%s %s)", f.Patch.Op, f.Patch.Path)
			}
		}
		fmt.Fprint(out, ", s) skip [s]: ")
		line, err := in.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		switch answer := strings.ToLower(strings.TrimSpace(line)); {
		case answer == "" || answer == "s" || answer == "skip":
			return 0, nil
		default:
			if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(d.Fixes) {
				return n, nil
			}
			if errors.Is(err, io.EOF) {
				return 0, nil // no more input: skip the rest
			}
			fmt.Fprintf(out, "choose a number from 1 to %d, or s to skip\n", len(d.Fixes))
		}
	}
}
