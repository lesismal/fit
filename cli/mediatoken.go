package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

// patternList is a flag given again for more patterns, each of which may
// list several, separated by commas.
type patternList []string

func (p *patternList) String() string { return strings.Join(*p, ",") }

func (p *patternList) Set(v string) error {
	for s := range strings.SplitSeq(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			*p = append(*p, s)
		}
	}
	return nil
}

// runMediaToken prints a token a media_auth policy's secret signs, for a
// publisher or player to send as a bearer token or the token query
// parameter, and when it expires, on standard error.
func runMediaToken(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("media-token", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var cfgPaths configPaths
	var publish, play patternList
	fs.Var(&cfgPaths, "c", fmt.Sprintf(configFlagUsage, "read the policy from"))
	policy := fs.String("policy", "", "the media_auth policy whose secret signs the token (required)")
	key := fs.String("key", "", "the key of the policy's keyring to sign with; the first when left out, and an error for a policy with a single secret")
	fs.Var(&publish, "publish", "a stream pattern the token may publish, as live/* or room1-*; give it again, or separate them with commas, for more")
	fs.Var(&play, "play", "a stream pattern the token may play, as -publish has them")
	ttl := fs.Duration("ttl", 0, "how long the token is good for; the policy's token_ttl when left out")
	subject := fs.String("subject", "media-token", "who the token is for, as logs name it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch {
	case fs.NArg() > 0:
		fmt.Fprintf(stderr, "fit media-token: unexpected argument %q\n", fs.Arg(0))
		return 2
	case *policy == "":
		fmt.Fprintln(stderr, "fit media-token: -policy is required")
		return 2
	case len(publish) == 0 && len(play) == 0:
		fmt.Fprintln(stderr, "fit media-token: give -publish, -play or both: a token that may do nothing is no use")
		return 2
	case *ttl < 0:
		fmt.Fprintln(stderr, "fit media-token: -ttl is negative")
		return 2
	}
	paths := cfgPaths.orDefault()
	name := strings.Join(paths, ", ")
	set, _, err := loadSet(paths)
	var c *config.Config
	if err == nil {
		c, err = config.Load(set.JSON)
		err = set.MapErrors(err)
	}
	if err != nil {
		printConfigError(stderr, "fit media-token", name, err)
		return 1
	}
	cfg := c.MediaAuthPolicy(*policy)
	switch {
	case cfg == nil:
		fmt.Fprintf(stderr, "fit media-token: %s has no media_auth policy named %q\n", name, *policy)
		return 1
	case cfg.Secret == "" && cfg.SecretFile == "" && len(cfg.Keys) == 0:
		fmt.Fprintf(stderr, "fit media-token: media_auth %q has neither secret, secret_file nor keys: tokens it would sign could not be checked by a running fit, which signs with a key of its own without one\n", *policy)
		return 1
	}
	p, err := mediaauth.New(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "fit media-token: media_auth %q: %v\n", *policy, err)
		return 1
	}
	var tok string
	var expiry time.Time
	grant := mediaauth.Grant{Publish: publish, Play: play}
	if *key != "" {
		if len(p.Keys()) == 0 {
			fmt.Fprintf(stderr, "fit media-token: media_auth %q has no keyring, so no key %q\n", *policy, *key)
			return 1
		}
		tok, expiry, err = p.IssueKey(*key, *subject, grant, *ttl)
	} else {
		tok, expiry, err = p.Issue(*subject, grant, *ttl)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fit media-token: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, tok)
	fmt.Fprintf(stderr, "fit media-token: good until %s\n", expiry.UTC().Format(time.RFC3339))
	return 0
}
