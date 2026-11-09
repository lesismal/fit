package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/config"
)

// The BitTorrent propose tools (I204). As the media propose tools
// (proposals_media.go), each adds a step to the conversation's one pending
// proposal and changes nothing: their steps are actions on the
// bittorrent.client role, not config, which go with a config change of any
// kind and run after it, in the order proposed, through the Admin API's
// /bittorrent endpoints, as the person who applies the proposal after its
// second confirm. They share the media steps' place in the proposal
// (MediaAction), so that the panel, the apply and the audit treat them
// alike.
const (
	ToolBTAddPropose    = "bt_add_propose"
	ToolBTPausePropose  = "bt_pause_propose"
	ToolBTResumePropose = "bt_resume_propose"
)

// The operations of a proposal's BitTorrent steps.
const (
	BTAdd    = "bt_add"
	BTPause  = "bt_pause"
	BTResume = "bt_resume"
)

// The bounds of the BitTorrent steps.
const (
	// maxBTRefs bounds the torrents one pause or resume names.
	maxBTRefs = 100
	// maxBTSource bounds a magnet link's or a URL's length.
	maxBTSource = 8192
)

// btRef is a torrent a step pauses or resumes: its ID and its name.
type btRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// btProposeTools returns the BitTorrent propose tools.
func btProposeTools() []*Tool {
	note := " The step joins the conversation's one pending proposal, with any config change, and runs after it when the person applies the proposal, in the panel, after a second confirm. You cannot apply it."
	refs := `"torrents":{"type":"array","items":{"type":"string","description":"a torrent's ID, as bt_torrents lists it"},"minItems":1,"maxItems":100}`
	return []*Tool{
		{Name: ToolBTAddPropose, Group: GroupBitTorrent, Level: config.AssistantPropose,
			Description: "Propose adding a torrent to the BitTorrent client by a magnet link or the http(s) URL of a .torrent file, which the client downloads when the proposal is applied: in a category of the client's (its directory and limits; none when left out), paused, or downloading its pieces in order (sequential). A torrent the client holds already is refused, as is a category it does not have." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,"magnet":{"type":"string","description":"a magnet link, magnet:?xt=urn:btih:..."},"url":{"type":"string","description":"the http or https URL of a .torrent file"},"category":{"type":"string"},"paused":{"type":"boolean"},"sequential":{"type":"boolean"}},"required":["summary"]}`),
			Run:         toolPropose(ToolBTAddPropose)},
		{Name: ToolBTPausePropose, Group: GroupBitTorrent, Level: config.AssistantPropose,
			Description: "Propose pausing torrents of the client, by their IDs as bt_torrents lists them (at most 100): they stop downloading and seeding until resumed. Those paused already are left out, with a note." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,` + refs + `},"required":["summary","torrents"]}`),
			Run:         toolPropose(ToolBTPausePropose)},
		{Name: ToolBTResumePropose, Group: GroupBitTorrent, Level: config.AssistantPropose,
			Description: "Propose resuming paused torrents of the client, by their IDs (at most 100); the queue may still hold one past max_active_downloads or max_active_seeds. Those not paused are left out, with a note." + note,
			Parameters:  json.RawMessage(`{"type":"object","properties":{` + summarySchema + `,` + refs + `},"required":["summary","torrents"]}`),
			Run:         toolPropose(ToolBTResumePropose)},
	}
}

// isBTTool reports whether name is a BitTorrent propose tool.
func isBTTool(name string) bool {
	return name == ToolBTAddPropose || name == ToolBTPausePropose || name == ToolBTResumePropose
}

// btStep checks the BitTorrent step i and adds what it does to b.
func (b *built) btStep(a *Assistant, ctx context.Context, i int, tool string, args stepArgs) error {
	if tool == ToolBTAddPropose {
		return b.btAdd(a, ctx, i, args)
	}
	return b.btPauseResume(a, ctx, i, tool, args)
}

// btSourceID returns the ID of the torrent a magnet link names, checked.
func btSourceID(magnet string) (string, error) {
	m, err := metainfo.ParseMagnet(magnet)
	if err != nil || (m.InfoHash.IsZero() && m.InfoHashV2.IsZero()) {
		return "", errors.New("magnet is a magnet link that names its torrent: magnet:?xt=urn:btih:<info hash>")
	}
	if !m.InfoHash.IsZero() {
		return m.InfoHash.String(), nil
	}
	return m.InfoHashV2.Truncate().String(), nil
}

func (b *built) btAdd(a *Assistant, ctx context.Context, i int, args stepArgs) error {
	m := MediaAction{Step: i + 1, Tool: ToolBTAddPropose, Op: BTAdd, Category: args.Category, Paused: args.Paused, Sequential: args.Sequential}
	var id string
	switch {
	case (args.Magnet == "") == (args.URL == ""):
		return errors.New("give the torrent by magnet or by url, one of them")
	case len(args.Magnet) > maxBTSource || len(args.URL) > maxBTSource:
		return fmt.Errorf("a magnet link or a URL is at most %d bytes", maxBTSource)
	case args.Magnet != "":
		var err error
		if id, err = btSourceID(args.Magnet); err != nil {
			return err
		}
		m.Magnet = args.Magnet
	default:
		u, err := url.Parse(args.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("url is the http or https URL of a .torrent file")
		}
		m.URL = args.URL
	}
	if slices.ContainsFunc(b.media, func(x MediaAction) bool {
		return x.Op == BTAdd && (x.Magnet != "" && x.Magnet == m.Magnet || x.URL != "" && x.URL == m.URL)
	}) {
		return errors.New("the proposal adds this torrent already")
	}
	// The role runs here, and its client: GET /bittorrent/alt_speed
	// answers 404 or 503 otherwise.
	var alt json.RawMessage
	if err := a.apiGet(ctx, "/bittorrent/alt_speed", &alt); err != nil {
		return err
	}
	if args.Category != "" {
		var cfg struct {
			BitTorrent *struct {
				Client *struct {
					Categories []struct {
						Name string `json:"name"`
					} `json:"categories"`
				} `json:"client"`
			} `json:"bittorrent"`
		}
		_ = json.Unmarshal(b.cur.data, &cfg)
		var names []string
		if cfg.BitTorrent != nil && cfg.BitTorrent.Client != nil {
			for _, c := range cfg.BitTorrent.Client.Categories {
				names = append(names, c.Name)
			}
		}
		if !slices.Contains(names, args.Category) {
			if len(names) == 0 {
				return fmt.Errorf("the client has no categories, so not %q: leave category out", args.Category)
			}
			return fmt.Errorf("the client has no category %q; it has %s", args.Category, strings.Join(names, ", "))
		}
	}
	if id != "" {
		var st struct {
			Name      string `json:"name"`
			Temporary bool   `json:"temporary"`
		}
		err := a.apiGet(ctx, "/bittorrent/torrents/"+id, &st)
		var ae *apiError
		switch {
		case err == nil && st.Temporary:
			m.Warning = fmt.Sprintf("bt_stream added %s on the fly: adding it keeps it as an ordinary torrent, as it is, its category, pause and order not changed", st.Name)
		case err == nil:
			return fmt.Errorf("the client holds this torrent already, as %q (%s)", st.Name, id)
		case !errors.As(err, &ae) || ae.Status != 404:
			return err
		}
		m.Torrents = []btRef{{ID: id, Name: st.Name}}
	}
	b.media = append(b.media, m)
	return nil
}

func (b *built) btPauseResume(a *Assistant, ctx context.Context, i int, tool string, args stepArgs) error {
	op, paused := BTPause, true
	if tool == ToolBTResumePropose {
		op, paused = BTResume, false
	}
	if len(args.Torrents) == 0 || len(args.Torrents) > maxBTRefs {
		return fmt.Errorf("torrents are 1 to %d torrents' IDs, as bt_torrents lists them", maxBTRefs)
	}
	m := MediaAction{Step: i + 1, Tool: tool, Op: op}
	seen := map[string]bool{}
	var already []string
	for _, raw := range args.Torrents {
		id, err := btID(raw)
		if err != nil {
			return err
		}
		if seen[id] {
			return fmt.Errorf("the torrent %s is named twice", id)
		}
		seen[id] = true
		if slices.ContainsFunc(b.media, func(x MediaAction) bool {
			return x.Op == op && slices.ContainsFunc(x.Torrents, func(r btRef) bool { return r.ID == id })
		}) {
			return fmt.Errorf("the proposal's step %s names %s already", op, id)
		}
		var st struct {
			Name   string `json:"name"`
			Paused bool   `json:"paused"`
		}
		if err := a.apiGet(ctx, "/bittorrent/torrents/"+id, &st); err != nil {
			return err
		}
		if st.Paused == paused {
			already = append(already, st.Name)
			continue
		}
		m.Torrents = append(m.Torrents, btRef{ID: id, Name: st.Name})
	}
	word := map[bool]string{true: "paused", false: "not paused"}[paused]
	if len(m.Torrents) == 0 {
		return fmt.Errorf("every torrent named is %s already", word)
	}
	if len(already) > 0 {
		m.Warning = fmt.Sprintf("%d of the torrents are %s already, and are left out: %s", len(already), word, strings.Join(already, ", "))
	}
	b.media = append(b.media, m)
	return nil
}

// applyBT applies a BitTorrent step as user, the person who applies the
// proposal, through the Admin API's /bittorrent endpoints.
func (a *Assistant) applyBT(ctx context.Context, user string, m MediaAction) mediaResult {
	res := mediaResult{Step: m.Step, Tool: m.Tool, Op: m.Op}
	call := func(method, path string, body any) ([]byte, error) {
		data, _ := json.Marshal(body)
		ans, err := a.apiAs(ctx, user, method, path, "application/json", "", data)
		if err != nil {
			return nil, err
		}
		if ans.status/100 != 2 {
			return ans.body, ans.err()
		}
		return ans.body, nil
	}
	var out []byte
	var err error
	switch m.Op {
	case BTAdd:
		body := map[string]any{"category": m.Category, "paused": m.Paused, "sequential": m.Sequential}
		if m.Magnet != "" {
			body["magnet"] = m.Magnet
		} else {
			body["url"] = m.URL
		}
		var ans []byte
		if ans, err = call("POST", "/bittorrent/torrents", body); err == nil {
			var st struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				State string `json:"state"`
			}
			_ = json.Unmarshal(ans, &st)
			out, _ = json.Marshal(st)
		}
	default: // pause, resume
		action := strings.TrimPrefix(m.Op, "bt_")
		done, gone := 0, 0
		for _, t := range m.Torrents {
			_, e := call("POST", "/bittorrent/torrents/"+t.ID, map[string]string{"action": action})
			var ae *apiError
			switch {
			case e == nil:
				done++
			case errors.As(e, &ae) && ae.Status == 404:
				gone++
			case err == nil:
				err = e
			}
		}
		out, _ = json.Marshal(map[string]int{action + "d": done, "gone": gone})
	}
	if err != nil {
		res.Error = err.Error()
		var ae *apiError
		if errors.As(err, &ae) {
			res.Error = ae.Msg
		}
	}
	if len(out) > 0 && json.Valid(out) {
		res.Result = out
	}
	return res
}
