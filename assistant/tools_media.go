package assistant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// The media tools (I152) read the media libraries, channels and playback
// sessions of I132–I139 through the Admin API in process, as the workbench's
// media pages do, and propose what a person may then apply: a rescan, the
// end of playback sessions, a key's rotation, a change of a library's files
// (proposals_media.go). None hands the model a key, a signature or a
// playback URL: the endpoints they read show none, and the redaction takes
// out a signed URL wherever one is; a session's subject goes as a hash of
// it unless data.send_subjects is set.

// The bounds of the media tools' own work.
const (
	// defaultMediaItems and maxMediaItems bound media_library_search's
	// page of items.
	defaultMediaItems = 25
	maxMediaItems     = 100
	// defaultMediaSessions and maxMediaSessions bound playback_sessions'
	// page of sessions.
	defaultMediaSessions = 50
	maxMediaSessions     = 200
	// subjectPrefix starts a subject's hash.
	subjectPrefix = "subject-"
)

// sessionsPage is the page the tools read the Admin API's sessions in, and
// maxSessionsScan the most sessions they read of a policy, to count each
// subject's and to find a subject given as its hash.
var (
	sessionsPage    = 1000
	maxSessionsScan = 20000
)

// mediaTools returns the media tools: the read tools here, and the propose
// tools of proposals_media.go.
func mediaTools() []*Tool {
	return append([]*Tool{
		{Name: "media_library_search", Group: GroupMedia, Level: config.AssistantRead,
			Description: "The media libraries (I132): without library, each library with its scan's status, items, errors, problems, bytes and file pool; with library, its status, the catalog's problems and a page of its items searched (q, in the ID or title) and filtered (codec, as h264 or opus; errors, items with or without a probe error; min_height and max_height of the largest rendition).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"library":{"type":"string","description":"the library's name; all libraries in short when left out"},"q":{"type":"string"},"codec":{"type":"string"},"errors":{"type":"boolean","description":"true: only items whose file or a rendition, audio or subtitle failed to probe; false: only those without"},"min_height":{"type":"integer"},"max_height":{"type":"integer"},"offset":{"type":"integer"},"limit":{"type":"integer","description":"the most items, 25 when left out, at most 100"}}}`),
			Run:         toolMediaLibrarySearch},
		{Name: "media_item_get", Group: GroupMedia, Level: config.AssistantRead,
			Description: "A media library's item whole: its files, tracks and codecs, renditions, audio, subtitles, poster, sidecar and probe errors; what uses it now (the channels whose list or schedule names it, those playing it now or next, and the playback sessions playing it); and, with sidecar, its sidecar as the library reads it, or a draft of one from its files' names, with the files under its directory.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"library":{"type":"string"},"item":{"type":"string","description":"the item's ID, as films/bbb"},"sidecar":{"type":"boolean","description":"include the item's sidecar, or a draft of one"}},"required":["library","item"]}`),
			Run:         toolMediaItemGet},
		{Name: "channels_status", Group: GroupMedia, Level: config.AssistantRead,
			Description: "The streaming role's channels (I134): each one's state, its error, what it plays now and next with the position, the schedule's next slot, its counters, and its push targets' state, bytes, bitrate and last error; and the push targets of streaming's own streams. A push target's URL shows its scheme, host and app only.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string","description":"one channel by its stream's name, as tv/films; all when left out"}}}`),
			Run:         toolChannelsStatus},
		{Name: "playback_sessions", Group: GroupMedia, Level: config.AssistantRead,
			Description: "The media_auth policies' playback (I135): each policy's url_signing settings (ttl, play_grace, session_idle, max_sessions, on_limit), its keys' names, the sessions playing and the URLs refused by reason (expired, signature, address, referer, origin, session_limit, session_ended); the resources played most; the vod routes' cache; and, for a policy, a page of its sessions by subject and start, with each subject's sessions playing set against max_sessions. Subjects are shown as subject-<hash> unless the settings send them; give one back as shown.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"policy":{"type":"string","description":"the policy whose sessions to list; the only one that signs URLs when left out"},"resource":{"type":"string","description":"only sessions of resources starting with this, as /vod/films/films/bbb/"},"subject":{"type":"string","description":"only this subject's sessions, as shown"},"offset":{"type":"integer"},"limit":{"type":"integer","description":"the most sessions, 50 when left out, at most 200"}}}`),
			Run:         toolPlaybackSessions},
	}, mediaProposeTools()...)
}

// subjectsSent reports whether s lets subjects reach the model as they are.
func subjectsSent(s *config.Assistant) bool { return s != nil && s.Data != nil && s.Data.SendSubjects }

// subjectOut returns subject as the model is shown it under s: as it is,
// or as subject- and 16 hex digits of an HMAC of it with the assistant's
// key, the same for one subject while fit runs, which the propose tools
// match again.
func (a *Assistant) subjectOut(s *config.Assistant, subject string) string {
	if subjectsSent(s) {
		return subject
	}
	m := hmac.New(sha256.New, a.subjectKey)
	m.Write([]byte(subject))
	return subjectPrefix + hex.EncodeToString(m.Sum(nil)[:8])
}

// subjectIs reports whether given, a subject as the model gave it, names
// subject: its hash, or, while the settings send subjects, itself.
func (a *Assistant) subjectIs(s *config.Assistant, given, subject string) bool {
	if given == subject && subjectsSent(s) {
		return true
	}
	m := hmac.New(sha256.New, a.subjectKey)
	m.Write([]byte(subject))
	return given == subjectPrefix+hex.EncodeToString(m.Sum(nil)[:8])
}

// apiText reads path from the Admin API, a JSON answer, and returns it
// compact, as a tool's result.
func (a *Assistant) apiText(ctx context.Context, path string) (string, error) {
	body, err := a.apiJSON(ctx, "GET", path, "", nil)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if json.Compact(&b, body) != nil {
		return string(body), nil
	}
	return b.String(), nil
}

// apiGet reads path from the Admin API into v.
func (a *Assistant) apiGet(ctx context.Context, path string, v any) error {
	body, err := a.apiJSON(ctx, "GET", path, "", nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("GET %s: the answer is not what it should be: %v", path, err)
	}
	return nil
}

// libraryPath returns the Admin API's path of library, or an error for a
// name that is no library's.
func libraryPath(library string) (string, error) {
	if library == "" {
		return "", errors.New("library is the library's name, as media_library_search lists it")
	}
	return "/media/library/" + url.PathEscape(library), nil
}

// checkItem refuses an ID that is no item's.
func checkItem(id string) error {
	if !medialib.ValidID(id) {
		return fmt.Errorf("%q is not an item's ID: parts of letters, digits, '.', '_', '~' and '-' joined by '/', as films/bbb", id)
	}
	return nil
}

// pageArgs checks a page's offset and limit, limit def when 0.
func pageArgs(offset, limit, def, most int) (int, int, error) {
	switch {
	case offset < 0:
		return 0, 0, errors.New("offset is 0 or more")
	case limit < 0 || limit > most:
		return 0, 0, fmt.Errorf("limit is 1 to %d", most)
	case limit == 0:
		limit = def
	}
	return offset, limit, nil
}

func toolMediaLibrarySearch(c *Call) (string, error) {
	var args struct {
		Library   string `json:"library"`
		Q         string `json:"q"`
		Codec     string `json:"codec"`
		Errors    *bool  `json:"errors"`
		MinHeight *int   `json:"min_height"`
		MaxHeight *int   `json:"max_height"`
		Offset    int    `json:"offset"`
		Limit     int    `json:"limit"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	if args.Library == "" {
		return c.a.apiText(c.Context, "/media/library")
	}
	offset, limit, err := pageArgs(args.Offset, args.Limit, defaultMediaItems, maxMediaItems)
	if err != nil {
		return "", err
	}
	path := "/media/library/" + url.PathEscape(args.Library)
	q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}
	if args.Q != "" {
		q.Set("q", args.Q)
	}
	if args.Codec != "" {
		q.Set("codec", args.Codec)
	}
	if args.Errors != nil {
		q.Set("error", strconv.FormatBool(*args.Errors))
	}
	for name, v := range map[string]*int{"min_height": args.MinHeight, "max_height": args.MaxHeight} {
		if v != nil {
			if *v < 0 {
				return "", fmt.Errorf("%s is 0 or more", name)
			}
			q.Set(name, strconv.Itoa(*v))
		}
	}
	return c.a.apiText(c.Context, path+"?"+q.Encode())
}

func toolMediaItemGet(c *Call) (string, error) {
	var args struct {
		Library string `json:"library"`
		Item    string `json:"item"`
		Sidecar bool   `json:"sidecar"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	path, err := libraryPath(args.Library)
	if err != nil {
		return "", err
	}
	if err := checkItem(args.Item); err != nil {
		return "", err
	}
	item, err := c.a.apiJSON(c.Context, "GET", path+"/items/"+args.Item, "", nil)
	if err != nil {
		return "", err
	}
	out := map[string]json.RawMessage{"item": item}
	if usage, err := c.a.apiJSON(c.Context, "GET", path+"/usage?item="+url.QueryEscape(args.Item), "", nil); err == nil {
		out["usage"] = usage
	} else {
		out["usage_error"], _ = json.Marshal(err.Error())
	}
	if args.Sidecar {
		if sc, err := c.a.apiJSON(c.Context, "GET", path+"/sidecar?item="+url.QueryEscape(args.Item), "", nil); err == nil {
			out["sidecar"] = sc
		} else {
			out["sidecar_error"], _ = json.Marshal(err.Error())
		}
	}
	return jsonText(out), nil
}

func toolChannelsStatus(c *Call) (string, error) {
	var args struct {
		Channel string `json:"channel"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	out := map[string]json.RawMessage{}
	if args.Channel != "" {
		ch, err := c.a.apiJSON(c.Context, "GET", "/streaming/channels/"+escapePath(args.Channel), "", nil)
		if err != nil {
			return "", err
		}
		out["channel"] = ch
	} else {
		var list struct {
			Channels json.RawMessage `json:"channels"`
		}
		if err := c.a.apiGet(c.Context, "/streaming/channels", &list); err != nil {
			return "", err
		}
		out["channels"] = list.Channels
	}
	var push struct {
		Push []json.RawMessage `json:"push"`
	}
	if err := c.a.apiGet(c.Context, "/streaming/push", &push); err == nil {
		// A channel's targets are with the channel; streaming's own here.
		own := []json.RawMessage{}
		for _, p := range push.Push {
			var head struct {
				Channel string `json:"channel"`
			}
			if json.Unmarshal(p, &head) == nil && head.Channel == "" {
				own = append(own, p)
			}
		}
		out["streaming_push"], _ = json.Marshal(own)
	}
	return jsonText(out), nil
}

// escapePath escapes each segment of a slash path.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// authPolicyView is a policy as GET /media/auth answers it, as far as the
// tools read it: never a secret, nor a key's file.
type authPolicyView struct {
	Name        string            `json:"name"`
	URLSigning  bool              `json:"url_signing"`
	Settings    json.RawMessage   `json:"settings,omitempty"`
	Keys        []authKeyView     `json:"keys"`
	Sessions    int               `json:"sessions"`
	Subjects    int               `json:"subjects"`
	Refusals    map[string]uint64 `json:"refusals"`
	TokenRoutes json.RawMessage   `json:"token_routes"`
}

// authKeyView is a policy's key: its name, "" for a single secret, and
// whether it is kept in a file, which a rotation writes.
type authKeyView struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
	File    bool   `json:"file"`
}

// authPolicies reads GET /media/auth.
func (a *Assistant) authPolicies(ctx context.Context) ([]authPolicyView, error) {
	var res struct {
		Policies []authPolicyView `json:"policies"`
	}
	if err := a.apiGet(ctx, "/media/auth", &res); err != nil {
		return nil, err
	}
	return res.Policies, nil
}

// sessionView is a playback session as GET /media/auth/{policy}/sessions
// answers it.
type sessionView struct {
	Subject  string `json:"subject"`
	ID       string `json:"id"`
	Resource string `json:"resource"`
	Address  string `json:"address"`
	Start    string `json:"start"`
	Last     string `json:"last"`
	Ended    bool   `json:"ended,omitempty"`
}

// policySessions reads the sessions of policy, at most maxSessionsScan, by
// subject and start, and says how many there are; subject, when not "", is
// a subject as it is, which the Admin API filters by.
func (a *Assistant) policySessions(ctx context.Context, policy, subject string) (list []sessionView, maxSessions, total int, err error) {
	for offset := 0; offset < maxSessionsScan; offset += sessionsPage {
		q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(sessionsPage)}}
		if subject != "" {
			q.Set("subject", subject)
		}
		var page struct {
			Matched     int           `json:"matched"`
			MaxSessions int           `json:"max_sessions"`
			Page        []sessionView `json:"page"`
		}
		if err := a.apiGet(ctx, "/media/auth/"+url.PathEscape(policy)+"/sessions?"+q.Encode(), &page); err != nil {
			return nil, 0, 0, err
		}
		list, maxSessions, total = append(list, page.Page...), page.MaxSessions, page.Matched
		if len(page.Page) < sessionsPage || len(list) >= total {
			break
		}
	}
	return list, maxSessions, total, nil
}

// mediaStatsView is GET /media/stats's answer, as far as the tools read it.
type mediaStatsView struct {
	Libraries json.RawMessage `json:"libraries"`
	VOD       json.RawMessage `json:"vod"`
	Policies  []struct {
		Name      string          `json:"name"`
		Resources json.RawMessage `json:"resources"`
	} `json:"policies"`
}

func toolPlaybackSessions(c *Call) (string, error) {
	var args struct {
		Policy   string `json:"policy"`
		Resource string `json:"resource"`
		Subject  string `json:"subject"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	offset, limit, err := pageArgs(args.Offset, args.Limit, defaultMediaSessions, maxMediaSessions)
	if err != nil {
		return "", err
	}
	a, s := c.a, c.Settings
	pols, err := a.authPolicies(c.Context)
	if err != nil {
		return "", err
	}
	var stats mediaStatsView
	_ = a.apiGet(c.Context, "/media/stats", &stats)
	type policyOut struct {
		Name        string            `json:"name"`
		URLSigning  bool              `json:"url_signing"`
		Settings    json.RawMessage   `json:"settings,omitempty"`
		KeyNames    []string          `json:"key_names"`
		RotatesKeys []string          `json:"rotatable_key_names"`
		Playing     int               `json:"sessions_playing"`
		Subjects    int               `json:"subjects_playing"`
		Refusals    map[string]uint64 `json:"refused_by_reason"`
		TokenRoutes json.RawMessage   `json:"token_routes,omitempty"`
		Resources   json.RawMessage   `json:"top_resources,omitempty"`
	}
	out := map[string]any{"subjects_hashed": !subjectsSent(s)}
	list := []policyOut{}
	signing := []string{}
	for _, p := range pols {
		po := policyOut{Name: p.Name, URLSigning: p.URLSigning, Settings: p.Settings, KeyNames: []string{}, RotatesKeys: []string{},
			Playing: p.Sessions, Subjects: p.Subjects, Refusals: p.Refusals, TokenRoutes: p.TokenRoutes}
		for _, k := range p.Keys {
			name := k.Name
			if name == "" {
				name = "(the single secret)"
			}
			po.KeyNames = append(po.KeyNames, name)
			if k.File {
				po.RotatesKeys = append(po.RotatesKeys, name)
			}
		}
		for _, sp := range stats.Policies {
			if sp.Name == p.Name {
				po.Resources = sp.Resources
			}
		}
		if p.URLSigning {
			signing = append(signing, p.Name)
		}
		list = append(list, po)
	}
	out["policies"] = list
	if len(stats.VOD) > 0 {
		out["vod_cache"] = stats.VOD
	}
	if len(stats.Libraries) > 0 {
		out["library_pools"] = stats.Libraries
	}
	policy := args.Policy
	if policy == "" && len(signing) == 1 {
		policy = signing[0]
	}
	if policy == "" {
		if len(pols) == 0 {
			out["note"] = "no media_auth policy runs here"
		} else {
			out["note"] = "name a policy to list its sessions: " + strings.Join(signing, ", ")
		}
		return jsonText(out), nil
	}
	if !slices.ContainsFunc(pols, func(p authPolicyView) bool { return p.Name == policy }) {
		return "", fmt.Errorf("no media_auth policy named %q runs here", policy)
	}
	// A subject as it is narrows the Admin API's list; a hash is matched
	// here.
	plain := ""
	if args.Subject != "" && subjectsSent(s) && !strings.HasPrefix(args.Subject, subjectPrefix) {
		plain = args.Subject
	}
	all, maxSessions, total, err := a.policySessions(c.Context, policy, plain)
	if err != nil {
		return "", err
	}
	read := len(all)
	// Each subject's sessions playing, of every resource, against
	// max_sessions; then the list narrowed.
	counts := map[string]int{}
	for _, ss := range all {
		if !ss.Ended {
			counts[ss.Subject]++
		}
	}
	all = slices.DeleteFunc(all, func(ss sessionView) bool {
		return !strings.HasPrefix(ss.Resource, args.Resource) || args.Subject != "" && plain == "" && !a.subjectIs(s, args.Subject, ss.Subject)
	})
	type countOut struct {
		Subject string `json:"subject"`
		Playing int    `json:"playing"`
		AtCap   bool   `json:"at_max_sessions,omitempty"`
	}
	page := []sessionView{}
	subjects := []countOut{}
	for i := offset; i < len(all) && len(page) < limit; i++ {
		ss := all[i]
		if len(subjects) == 0 || subjects[len(subjects)-1].Subject != a.subjectOut(s, ss.Subject) {
			subjects = append(subjects, countOut{Subject: a.subjectOut(s, ss.Subject), Playing: counts[ss.Subject],
				AtCap: maxSessions > 0 && counts[ss.Subject] >= maxSessions})
		}
		ss.Subject = a.subjectOut(s, ss.Subject)
		page = append(page, ss)
	}
	out["policy"], out["max_sessions"], out["matched"], out["offset"], out["limit"] = policy, maxSessions, len(all), offset, limit
	out["sessions"], out["per_subject"] = page, subjects
	if read < total {
		out["note"] = fmt.Sprintf("the policy has %d sessions, and the %d first by subject were read", total, read)
	}
	return jsonText(out), nil
}
