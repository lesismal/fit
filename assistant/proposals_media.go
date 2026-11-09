package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// The media propose tools (I152). Each adds a step to the conversation's
// one pending proposal, as the config's propose tools do; none changes
// anything. Their steps are actions, not config: they go with a config
// change of any kind, as cache purges do, and are applied after it, in the
// order they were proposed, through the Admin API's media endpoints, as
// the person who applies them; then the purges.
const (
	ToolMediaScanPropose       = "media_scan_propose"
	ToolMediaSessionEndPropose = "media_session_end_propose"
	ToolMediaKeyRotatePropose  = "media_key_rotate_propose"
	ToolMediaFilePropose       = "media_file_propose"
)

// The operations of a proposal's media steps.
const (
	MediaScan        = "scan"
	MediaEndSessions = "end_sessions"
	MediaRotate      = "rotate"
	MediaRename      = "rename"
	MediaMove        = "move"
	MediaDelete      = "delete"
	MediaSidecar     = "sidecar"
)

// The bounds of the media steps.
const (
	// maxSessionRefs bounds the sessions one step ends.
	maxSessionRefs = 100
	// maxFileName bounds a rename's new name and a move's directory.
	maxFileName = 1024
)

// singleSecret is how playback_sessions names a policy's one secret, which
// a rotation names with no key.
const singleSecret = "(the single secret)"

// SessionRef is a playback session a step ends: its subject, as the model
// was shown it, and its ID.
type SessionRef struct {
	Subject string `json:"subject"`
	ID      string `json:"id"`
}

// ItemUsage is what uses a library's item, as GET
// /media/library/{name}/usage answers it.
type ItemUsage struct {
	Channels []string `json:"channels"`
	Playing  []string `json:"playing"`
	Sessions int      `json:"sessions"`
	Counted  bool     `json:"sessions_counted"`
	InUse    bool     `json:"in_use"`
}

// MediaAction is a media step of a proposal, checked on what runs now:
// what it does, and, for a change of files, what uses the item, which the
// person sees before applying.
type MediaAction struct {
	// Step is the proposal's step it is, from 1.
	Step    int    `json:"step"`
	Tool    string `json:"tool"`
	Op      string `json:"op"`
	Library string `json:"library,omitempty"`
	Item    string `json:"item,omitempty"`
	Name    string `json:"name,omitempty"`
	Dir     string `json:"dir,omitempty"`
	// Sidecar is the sidecar a sidecar step writes, and Diff its change
	// from the one the item has, or the draft of its files' names.
	Sidecar json.RawMessage `json:"sidecar,omitempty"`
	Diff    string          `json:"diff,omitempty"`
	Policy  string          `json:"policy,omitempty"`
	Key     string          `json:"key,omitempty"`
	All     bool            `json:"all,omitempty"`
	// Sessions are the sessions an end_sessions step ends, playing now.
	Sessions []sessionView `json:"sessions,omitempty"`
	// Usage is what uses a file step's item now, and Warning says it.
	Usage   *ItemUsage `json:"usage,omitempty"`
	Warning string     `json:"warning,omitempty"`
	// The BitTorrent steps' (proposals_bt.go): the torrents a pause or a
	// resume names, or that an add adds when its magnet link names it, and
	// what an add adds.
	Torrents   []btRef `json:"torrents,omitempty"`
	Magnet     string  `json:"magnet,omitempty"`
	URL        string  `json:"url,omitempty"`
	Category   string  `json:"category,omitempty"`
	Paused     bool    `json:"paused,omitempty"`
	Sequential bool    `json:"sequential,omitempty"`

	// modified is the sidecar's time the change is made from, "" when the
	// item has none.
	modified string
}

// mediaResult is a media step's outcome: the Admin API's answer, or why
// it failed.
type mediaResult struct {
	Step   int             `json:"step"`
	Tool   string          `json:"tool"`
	Op     string          `json:"op"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// mediaProposeTools returns the media propose tools.
func mediaProposeTools() []*Tool {
	note := " The step joins the conversation's one pending proposal, with any config change, and runs after it when the person applies the proposal, in the panel, after a second confirm. You cannot apply it."
	return []*Tool{
		{Name: ToolMediaScanPropose, Group: GroupMedia, Level: config.AssistantPropose,
			Description: "Propose scanning a media library again at once, as after files were added or fixed outside fit." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"library":{"type":"string"}},"required":["summary","library"]}`),
			Run:         toolPropose(ToolMediaScanPropose)},
		{Name: ToolMediaSessionEndPropose, Group: GroupMedia, Level: config.AssistantPropose,
			Description: "Propose ending playback sessions of a media_auth policy, as playback_sessions lists them: each by its subject, as shown, and its ID. A session ended stops being served at once; its viewer needs a new URL." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"policy":{"type":"string"},"sessions":{"type":"array","items":{"type":"object","properties":{"subject":{"type":"string"},"id":{"type":"string"}},"required":["subject","id"]},"minItems":1,"maxItems":100}},"required":["summary","policy","sessions"]}`),
			Run:         toolPropose(ToolMediaSessionEndPropose)},
		{Name: ToolMediaKeyRotatePropose, Group: GroupMedia, Level: config.AssistantPropose,
			Description: "Propose rotating a media_auth policy's signing key, written anew to its secret_file and used at once: one key of the keyring by name, all of them, or, with neither, the policy's single secret. Every URL and grant signed with an old key is refused from then on. Only a key kept in a file rotates; playback_sessions lists them. The new secret is never shown." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"policy":{"type":"string"},"key":{"type":"string","description":"a key of the keyring by name; left out for the single secret, or with all"},"all":{"type":"boolean"}},"required":["summary","policy"]}`),
			Run:         toolPropose(ToolMediaKeyRotatePropose)},
		{Name: ToolMediaFilePropose, Group: GroupMedia, Level: config.AssistantPropose,
			Description: "Propose a change of a media library's item's files: rename (name, the item's new file name without its directory), move (dir, a directory of the root, \"\" for its top), delete (every file of the item), or sidecar (the item's sidecar JSON written whole: id, title, poster, renditions, audio, subtitles, each file by its path from the sidecar's directory; media_item_get with sidecar reads it). The result says what uses the item now, the channels that name or play it and the viewers playing it, which the person is warned of before applying; say it too. One step per item." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"library":{"type":"string"},"item":{"type":"string","description":"the item's ID"},"op":{"type":"string","enum":["rename","move","delete","sidecar"]},"name":{"type":"string"},"dir":{"type":"string"},"sidecar":{"type":"object"}},"required":["summary","library","item","op"]}`),
			Run:         toolPropose(ToolMediaFilePropose)},
	}
}

// isMediaTool reports whether name is a media propose tool.
func isMediaTool(name string) bool {
	switch name {
	case ToolMediaScanPropose, ToolMediaSessionEndPropose, ToolMediaKeyRotatePropose, ToolMediaFilePropose:
		return true
	}
	return false
}

// mediaStep checks the media step i and adds what it does to b.
func (b *built) mediaStep(a *Assistant, ctx context.Context, i int, tool string, args stepArgs) error {
	switch tool {
	case ToolMediaScanPropose:
		return b.mediaScan(a, ctx, i, args)
	case ToolMediaSessionEndPropose:
		return b.mediaSessions(a, ctx, i, args)
	case ToolMediaKeyRotatePropose:
		return b.mediaRotate(a, ctx, i, args)
	default: // ToolMediaFilePropose
		return b.mediaFile(a, ctx, i, args)
	}
}

func (b *built) mediaScan(a *Assistant, ctx context.Context, i int, args stepArgs) error {
	path, err := libraryPath(args.Library)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(b.media, func(m MediaAction) bool { return m.Op == MediaScan && m.Library == args.Library }) {
		return fmt.Errorf("the proposal scans %s already", args.Library)
	}
	var lib struct {
		Name string `json:"name"`
	}
	if err := a.apiGet(ctx, path+"?limit=1", &lib); err != nil {
		return err
	}
	b.media = append(b.media, MediaAction{Step: i + 1, Tool: ToolMediaScanPropose, Op: MediaScan, Library: args.Library})
	return nil
}

// policy returns the policy name as GET /media/auth answers it, read once
// for the build.
func (b *built) policy(a *Assistant, ctx context.Context, name string) (*authPolicyView, error) {
	if name == "" {
		return nil, errors.New("policy is a media_auth policy's name, as playback_sessions lists them")
	}
	if b.policies == nil {
		pols, err := a.authPolicies(ctx)
		if err != nil {
			return nil, err
		}
		b.policies = append(pols, authPolicyView{}) // never nil again
	}
	for i := range b.policies {
		if b.policies[i].Name == name {
			return &b.policies[i], nil
		}
	}
	return nil, fmt.Errorf("no media_auth policy named %q runs here", name)
}

func (b *built) mediaSessions(a *Assistant, ctx context.Context, i int, args stepArgs) error {
	switch {
	case len(args.Sessions) == 0 || len(args.Sessions) > maxSessionRefs:
		return fmt.Errorf("sessions are 1 to %d sessions, each its subject and id", maxSessionRefs)
	case slices.ContainsFunc(b.media, func(m MediaAction) bool { return m.Op == MediaEndSessions && m.Policy == args.Policy }):
		return fmt.Errorf("the proposal ends sessions of %s already: add these to that step's, in a proposal of their own", args.Policy)
	}
	p, err := b.policy(a, ctx, args.Policy)
	if err != nil {
		return err
	}
	if !p.URLSigning {
		return fmt.Errorf("the policy %s has no url_signing, and so no playback sessions", p.Name)
	}
	ids := map[string]bool{}
	for _, ref := range args.Sessions {
		if ref.Subject == "" || ref.ID == "" || ids[ref.ID] {
			return errors.New("each session is its subject and id, as playback_sessions lists it, each once")
		}
		ids[ref.ID] = true
	}
	all, _, _, err := a.policySessions(ctx, p.Name, "")
	if err != nil {
		return err
	}
	s := a.Settings()
	m := MediaAction{Step: i + 1, Tool: ToolMediaSessionEndPropose, Op: MediaEndSessions, Policy: p.Name}
	var gone []string
	for _, ref := range args.Sessions {
		k := slices.IndexFunc(all, func(ss sessionView) bool { return ss.ID == ref.ID && a.subjectIs(s, ref.Subject, ss.Subject) })
		if k < 0 || all[k].Ended {
			gone = append(gone, ref.ID)
			continue
		}
		m.Sessions = append(m.Sessions, all[k])
	}
	if len(m.Sessions) == 0 {
		return fmt.Errorf("none of the sessions plays now under %s: read them again with playback_sessions", p.Name)
	}
	if len(gone) > 0 {
		m.Warning = fmt.Sprintf("%d of the sessions no longer play, and are left out: %s", len(gone), strings.Join(gone, ", "))
	}
	b.media = append(b.media, m)
	return nil
}

func (b *built) mediaRotate(a *Assistant, ctx context.Context, i int, args stepArgs) error {
	p, err := b.policy(a, ctx, args.Policy)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(b.media, func(m MediaAction) bool { return m.Op == MediaRotate && m.Policy == p.Name }) {
		return fmt.Errorf("the proposal rotates a key of %s already", p.Name)
	}
	key := args.Key
	if key == singleSecret {
		key = ""
	}
	if args.All && key != "" {
		return errors.New("name a key, or all, not both")
	}
	ring := slices.ContainsFunc(p.Keys, func(k authKeyView) bool { return k.Name != "" })
	var rotated []string
	for _, k := range p.Keys {
		switch {
		case args.All && ring, !args.All && key == k.Name:
			if !k.File {
				return fmt.Errorf("the key %q of %s is written in the config, not kept in a secret_file, so it is not rotated here: the person changes it in the config", k.Name, p.Name)
			}
			rotated = append(rotated, k.Name)
		}
	}
	switch {
	case args.All && !ring:
		return fmt.Errorf("the policy %s has a single secret, not a keyring: rotate it with neither key nor all", p.Name)
	case !args.All && key == "" && ring:
		return fmt.Errorf("the policy %s has a keyring: name the key to rotate, or all", p.Name)
	case len(rotated) == 0:
		return fmt.Errorf("the policy %s has no key %q", p.Name, key)
	}
	b.media = append(b.media, MediaAction{Step: i + 1, Tool: ToolMediaKeyRotatePropose, Op: MediaRotate, Policy: p.Name, Key: key, All: args.All})
	return nil
}

func (b *built) mediaFile(a *Assistant, ctx context.Context, i int, args stepArgs) error {
	path, err := libraryPath(args.Library)
	if err != nil {
		return err
	}
	if err := checkItem(args.Item); err != nil {
		return err
	}
	m := MediaAction{Step: i + 1, Tool: ToolMediaFilePropose, Op: args.Op, Library: args.Library, Item: args.Item}
	switch args.Op {
	case MediaRename:
		if args.Name == "" || len(args.Name) > maxFileName || strings.ContainsAny(args.Name, `/\`) {
			return errors.New("name is the item's new file name, without a directory")
		}
		m.Name = args.Name
	case MediaMove:
		if len(args.Dir) > maxFileName {
			return errors.New("dir is a directory of the item's root, \"\" for its top")
		}
		m.Dir = args.Dir
	case MediaDelete:
	case MediaSidecar:
		dec := json.NewDecoder(bytes.NewReader(args.Sidecar))
		dec.DisallowUnknownFields()
		var sd medialib.Sidecar
		if len(args.Sidecar) == 0 || dec.Decode(&sd) != nil {
			return errors.New("sidecar is the item's sidecar, a JSON object of id, title, poster, renditions, audio and subtitles, as media_item_get with sidecar shows it")
		}
		if sd.ID != "" && !medialib.ValidID(sd.ID) {
			return fmt.Errorf("the sidecar's id %q is not an item's ID", sd.ID)
		}
		m.Sidecar, _ = json.Marshal(sd)
	default:
		return errors.New("op is rename, move, delete or sidecar")
	}
	if slices.ContainsFunc(b.media, func(x MediaAction) bool {
		return x.Tool == ToolMediaFilePropose && x.Library == args.Library && x.Item == args.Item
	}) {
		return fmt.Errorf("the proposal changes the files of %s already; one change of an item's files a proposal, as the first changes its ID", args.Item)
	}
	if _, err := a.apiJSON(ctx, "GET", path+"/items/"+args.Item, "", nil); err != nil {
		return err
	}
	var u ItemUsage
	if err := a.apiGet(ctx, path+"/usage?item="+url.QueryEscape(args.Item), &u); err != nil {
		return err
	}
	m.Usage, m.Warning = &u, usageWarning(args.Op, args.Item, &u)
	if args.Op == MediaSidecar {
		var view struct {
			Exists   bool            `json:"exists"`
			Modified string          `json:"modified"`
			Sidecar  json.RawMessage `json:"sidecar"`
		}
		if err := a.apiGet(ctx, path+"/sidecar?item="+url.QueryEscape(args.Item), &view); err != nil {
			return err
		}
		if view.Exists {
			m.modified = view.Modified
		}
		from, to := "the sidecar now", "proposed"
		if !view.Exists {
			from = "a draft of the files' names (no sidecar now)"
		}
		m.Diff, _ = cutText(unifiedDiff(from, to, indentJSON(view.Sidecar), indentJSON(m.Sidecar)), maxDiffBytes)
	}
	b.media = append(b.media, m)
	return nil
}

// indentJSON returns data indented, with a final newline.
func indentJSON(data []byte) string {
	var out bytes.Buffer
	if json.Indent(&out, data, "", "  ") != nil {
		return string(data) + "\n"
	}
	out.WriteByte('\n')
	return out.String()
}

// usageWarning says what uses the item id that op changes, or "".
func usageWarning(op, id string, u *ItemUsage) string {
	if !u.InUse {
		return ""
	}
	var parts []string
	if len(u.Playing) > 0 {
		parts = append(parts, "the channel "+strings.Join(u.Playing, ", ")+" plays it now or next")
	}
	if named := slices.DeleteFunc(slices.Clone(u.Channels), func(c string) bool { return slices.Contains(u.Playing, c) }); len(named) > 0 {
		parts = append(parts, "the channel "+strings.Join(named, ", ")+" names it in its list or schedule")
	} else if len(u.Channels) > 0 && len(u.Playing) > 0 {
		parts[0] += ", and names it in its list or schedule"
	}
	if u.Sessions > 0 {
		parts = append(parts, fmt.Sprintf("%d viewers play it", u.Sessions))
	}
	what := map[string]string{
		MediaDelete:  "deleting it stops them, and a channel that names it skips it from then on",
		MediaRename:  "renaming it may change its ID, which a channel names it by and viewers' URLs carry",
		MediaMove:    "moving it changes its ID, which a channel names it by and viewers' URLs carry",
		MediaSidecar: "a sidecar that changes its ID loses it to what names it by the old one",
	}[op]
	return fmt.Sprintf("%s is in use: %s; %s.", id, strings.Join(parts, "; "), what)
}

// mediaTag is a digest of what uses the items the proposal's file steps
// change, as the person was shown it: whether each is in use, the channels
// that name or play it, and whether viewers play it. An apply that
// confirmed another is offered again; a channel's now and next moving
// between the channels that name the item, or viewers coming and going
// while some play, do not change it.
func mediaTag(media []MediaAction) string {
	h := sha256.New()
	n := 0
	for _, m := range media {
		if u := m.Usage; u != nil {
			chans := slices.Concat(u.Channels, u.Playing)
			slices.Sort(chans)
			data, _ := json.Marshal([]any{m.Step, m.Item, u.InUse, slices.Compact(chans), u.Sessions > 0})
			h.Write(data)
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// modelMedia returns the media steps as the model is shown them: the
// sessions' subjects as playback_sessions shows them.
func (a *Assistant) modelMedia(s *config.Assistant, media []MediaAction) []MediaAction {
	out := slices.Clone(media)
	for i := range out {
		if len(out[i].Sessions) == 0 {
			continue
		}
		ss := slices.Clone(out[i].Sessions)
		for k := range ss {
			ss[k].Subject = a.subjectOut(s, ss[k].Subject)
		}
		out[i].Sessions = ss
	}
	return out
}

// applyMedia applies a media step as user, the person who applies the
// proposal: the Admin API's media endpoints audit a change of files as
// theirs. A change of files forces what the person was warned of, and no
// more: an item in use that was not is refused 409.
func (a *Assistant) applyMedia(ctx context.Context, user string, m MediaAction) mediaResult {
	res := mediaResult{Step: m.Step, Tool: m.Tool, Op: m.Op}
	call := func(method, path string, body any) ([]byte, error) {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		ans, err := a.apiAs(ctx, user, method, path, "application/json", "", data)
		if err != nil {
			return nil, err
		}
		if ans.status/100 != 2 {
			return ans.body, ans.err()
		}
		return ans.body, nil
	}
	lib := "/media/library/" + url.PathEscape(m.Library)
	var out []byte
	var err error
	switch m.Op {
	case MediaScan:
		out, err = call("POST", lib+"/rescan", nil)
	case MediaEndSessions:
		ended, gone := 0, 0
		for _, ss := range m.Sessions {
			_, e := call("POST", "/media/auth/"+url.PathEscape(m.Policy)+"/sessions/end", map[string]string{"subject": ss.Subject, "id": ss.ID})
			var ae *apiError
			switch {
			case e == nil:
				ended++
			case errors.As(e, &ae) && ae.Status == 404:
				gone++
			case err == nil:
				err = e
			}
		}
		out, _ = json.Marshal(map[string]int{"ended": ended, "ended_already": gone})
	case MediaRotate:
		out, err = call("POST", "/media/auth/"+url.PathEscape(m.Policy)+"/rotate", map[string]any{"key": m.Key, "all": m.All})
	case MediaSidecar:
		body := map[string]any{"item": m.Item, "sidecar": m.Sidecar, "force": m.Usage != nil && m.Usage.InUse}
		if m.modified != "" {
			body["modified"] = m.modified
		}
		out, err = call("PUT", lib+"/sidecar", body)
	default: // rename, move, delete
		body := map[string]any{"item": m.Item, "force": m.Usage != nil && m.Usage.InUse}
		switch m.Op {
		case MediaRename:
			body["name"] = m.Name
		case MediaMove:
			body["dir"] = m.Dir
		}
		out, err = call("POST", lib+"/"+m.Op, body)
	}
	if err != nil {
		res.Error = err.Error()
		var ae *apiError
		if errors.As(err, &ae) {
			res.Error = ae.Msg
			if ae.Status == 409 && m.Tool == ToolMediaFilePropose && strings.Contains(string(out), `"usage"`) {
				res.Error = "the item came into use after the proposal was reviewed, so it was not changed: " + ae.Msg
			}
		}
	}
	if json.Valid(out) && len(out) > 0 {
		res.Result = out
	}
	return res
}
