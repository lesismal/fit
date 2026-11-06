package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lesismal/fit/internal/linktoken"
)

const tokenUsage = `Usage: fit token <list|create|expiry|revoke> [flags]

Manage the link tokens a running fit's listeners issue, which the tunnel's
nodes and proxies and the LLM mesh's peers link with in place of a shared
password, through its Admin API. The roles that issue them are tunnel.hub,
llm.hub, llm.node and llm.proxy.

	fit token list   [-role R]                    the tokens, their expiry, last use,
	                                              members and links
	fit token create -role R -name N [-note T] [-ttl 30d | -expires TIME]
	                                              issue one: the token is printed on
	                                              standard output, this once
	fit token expiry -role R -id ID (-ttl 30d | -expires TIME | -never)
	fit token revoke -role R -id ID               its links close at once

Every command takes -c, -timeout and the TLS flags of fit check.
`

// tokenFlags are the flags every fit token command takes.
type tokenFlags struct {
	fs       *flag.FlagSet
	cfgPaths configPaths
	timeout  *time.Duration
	tls      checkTLS
	role, id *string
	asJSON   *bool
}

func newTokenFlags(name string, stderr io.Writer) *tokenFlags {
	f := &tokenFlags{fs: flag.NewFlagSet("token "+name, flag.ContinueOnError)}
	f.fs.SetOutput(stderr)
	f.fs.Var(&f.cfgPaths, "c", "the bootstrap config file, or a directory of them, fit was started with, whose admin says where its Admin API is (fit.json when none is given); give -c again for more, which are merged")
	f.timeout = f.fs.Duration("timeout", 60*time.Second, "how long fit token waits for the Admin API")
	f.fs.StringVar(&f.tls.CA, "ca", "", "with admin.cert_file, a PEM file with the CAs the Admin API's certificate is verified with")
	f.fs.StringVar(&f.tls.ServerName, "server-name", "", "with admin.cert_file, the name the Admin API's certificate is verified for")
	f.fs.StringVar(&f.tls.Cert, "cert", "", "with admin.client_ca_file, a PEM file with the client certificate fit token presents")
	f.fs.StringVar(&f.tls.Key, "key", "", "the private key of -cert")
	f.role = f.fs.String("role", "", "the role whose listener issues the tokens: tunnel.hub, llm.hub, llm.node or llm.proxy")
	f.id = f.fs.String("id", "", "the token's ID, 16 hex digits, as fit token list shows it")
	f.asJSON = f.fs.Bool("json", false, "print the Admin API's answer as JSON")
	return f
}

// api reaches the Admin API the bootstrap config names.
func (f *tokenFlags) api(stderr io.Writer) (*checkAPI, bool) {
	paths := f.cfgPaths.orDefault()
	set, _, err := loadSet(paths)
	if err != nil {
		printConfigError(stderr, "fit token", strings.Join(paths, ", "), err)
		return nil, false
	}
	a, err := checkAdminOf(set.JSON)
	if err == nil {
		var api *checkAPI
		if api, err = newCheckAPI(a, f.tls, *f.timeout); err == nil {
			return api, true
		}
	}
	fmt.Fprintf(stderr, "fit token: %s\n", strings.ReplaceAll(err.Error(), "fit check", "fit token"))
	return nil, false
}

// runToken manages a running fit's link tokens through its Admin API.
func runToken(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, tokenUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cmd := args[0]
	f := newTokenFlags(cmd, stderr)
	var name, note, ttl, expires *string
	var never *bool
	switch cmd {
	case "create":
		name = f.fs.String("name", "", "what the token is for, as the pages list it (required)")
		note = f.fs.String("note", "", "anything else about the token")
		fallthrough
	case "expiry":
		ttl = f.fs.String("ttl", "", "how long the token is good for, from now: a duration, as 720h, or days, as 30d")
		expires = f.fs.String("expires", "", "when the token expires, as RFC 3339, as 2027-01-01T00:00:00Z")
		if cmd == "expiry" {
			never = f.fs.Bool("never", false, "the token never expires")
		}
	case "list", "revoke":
	default:
		fmt.Fprintf(stderr, "fit token: unknown command %q\n\n%s", cmd, tokenUsage)
		return 2
	}
	if err := f.fs.Parse(args[1:]); err != nil {
		return 2
	}
	if usageErr := tokenArgs(cmd, f, name, ttl, expires, never); usageErr != "" {
		fmt.Fprintf(stderr, "fit token %s: %s\n", cmd, usageErr)
		return 2
	}
	var expiresAt int64
	if expires != nil && *expires != "" {
		at, err := time.Parse(time.RFC3339, *expires)
		if err != nil {
			fmt.Fprintf(stderr, "fit token %s: -expires %q is not an RFC 3339 time, as 2027-01-01T00:00:00Z\n", cmd, *expires)
			return 2
		}
		expiresAt = at.Unix()
	}
	api, ok := f.api(stderr)
	if !ok {
		return 2
	}
	defer api.close()
	role := url.PathEscape(*f.role)
	var method, path string
	var body any
	switch cmd {
	case "list":
		method, path = http.MethodGet, "/tokens"
		if *f.role != "" {
			path += "/" + role
		}
	case "create":
		method, path = http.MethodPost, "/tokens/"+role
		body = linktoken.IssueReq{Name: *name, Note: *note, TTL: *ttl, ExpiresAt: expiresAt}
	case "expiry":
		method, path = http.MethodPatch, "/tokens/"+role+"/"+url.PathEscape(*f.id)
		body = linktoken.ExpiryReq{TTL: *ttl, ExpiresAt: expiresAt}
	case "revoke":
		method, path = http.MethodDelete, "/tokens/"+role+"/"+url.PathEscape(*f.id)
	}
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body) // strings and numbers: cannot fail
	}
	out, status, err := api.do(method, path, data, "")
	if err == nil && status != http.StatusOK {
		err = api.refused(status, out)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fit token %s: %v\n", cmd, err)
		return 1
	}
	if *f.asJSON {
		_, _ = stdout.Write(out)
		return 0
	}
	return printToken(cmd, *f.role, out, stdout, stderr)
}

// tokenArgs checks the flags of cmd, and returns how they are wrong, or "".
func tokenArgs(cmd string, f *tokenFlags, name, ttl, expires *string, never *bool) string {
	switch {
	case f.fs.NArg() > 0:
		return fmt.Sprintf("unexpected argument %q", f.fs.Arg(0))
	case cmd != "list" && *f.role == "":
		return "-role is required"
	case (cmd == "expiry" || cmd == "revoke") && *f.id == "":
		return "-id is required"
	case cmd == "create" && strings.TrimSpace(*name) == "":
		return "-name is required"
	case ttl != nil && *ttl != "" && *expires != "":
		return "give -ttl or -expires, not both"
	case cmd == "expiry" && *ttl == "" && *expires == "" && !*never:
		return "give -ttl, -expires or -never"
	case cmd == "expiry" && *never && (*ttl != "" || *expires != ""):
		return "-never takes no -ttl or -expires"
	}
	if ttl != nil && *ttl != "" {
		if _, err := linktoken.ParseTTL(*ttl); err != nil {
			return err.Error()
		}
	}
	return ""
}

// printToken prints the Admin API's answer, out, to cmd.
func printToken(cmd, role string, out []byte, stdout, stderr io.Writer) int {
	switch cmd {
	case "create":
		var is linktoken.Issued
		if err := json.Unmarshal(out, &is); err != nil || is.Token == "" {
			fmt.Fprintf(stderr, "fit token create: the Admin API's answer is not a token: %s\n", out)
			return 1
		}
		fmt.Fprintln(stdout, is.Token)
		fmt.Fprintf(stderr, "fit token create: %s issued token %s (%q), %s; it is shown this once: give it to the member as its token\n",
			role, is.ID, is.Name, expiryText(is.ExpiresAt))
	case "expiry":
		var v linktoken.View
		_ = json.Unmarshal(out, &v)
		fmt.Fprintf(stdout, "token %s %s\n", v.ID, expiryText(v.ExpiresAt))
	case "revoke":
		fmt.Fprintln(stdout, "revoked: its links are closed")
	case "list":
		roles := map[string][]linktoken.View{}
		if role != "" {
			var one struct {
				Tokens []linktoken.View `json:"tokens"`
			}
			_ = json.Unmarshal(out, &one)
			roles[role] = one.Tokens
		} else {
			var all struct {
				Roles map[string][]linktoken.View `json:"roles"`
			}
			_ = json.Unmarshal(out, &all)
			roles = all.Roles
		}
		printTokenList(stdout, roles)
	}
	return 0
}

func expiryText(at int64) string {
	if at == 0 {
		return "never expires"
	}
	return "expires " + time.Unix(at, 0).UTC().Format(time.RFC3339)
}

// printTokenList prints each role's tokens as a table.
func printTokenList(w io.Writer, roles map[string][]linktoken.View) {
	if len(roles) == 0 {
		fmt.Fprintln(w, "no role here issues link tokens")
		return
	}
	ids := make([]string, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ROLE\tID\tNAME\tEXPIRES\tLAST USED\tMEMBERS\tLINKS")
	for _, id := range ids {
		for _, v := range roles[id] {
			exp := "never"
			if v.ExpiresAt != 0 {
				exp = time.Unix(v.ExpiresAt, 0).UTC().Format(time.RFC3339)
				if v.Expired {
					exp += " (expired)"
				}
			}
			used := "-"
			if v.LastUsed != 0 {
				used = time.Unix(v.LastUsed, 0).UTC().Format(time.RFC3339) + " from " + v.LastAddr
			}
			members := strings.Join(v.Members, ",")
			if members == "" {
				members = "-"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n", id, v.ID, v.Name, exp, used, members, len(v.Links))
		}
	}
	_ = tw.Flush()
}
