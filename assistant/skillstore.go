package assistant

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// skillsSection is the store's section the users' skills are kept in, with
// which skills are disabled.
const skillsSection = "assistant.skills"

// keptSkills is the store's section of the skills.
type keptSkills struct {
	// User are the skills users added, as their Markdown.
	User []keptSkill `json:"user,omitempty"`
	// Disabled names the built-in skills turned off.
	Disabled []string `json:"disabled,omitempty"`
}

// keptSkill is a user's skill as the store keeps it.
type keptSkill struct {
	Source   string `json:"source"`
	Disabled bool   `json:"disabled,omitempty"`
	By       string `json:"by,omitempty"`
	At       int64  `json:"at,omitempty"`
}

// userSkill is a skill a user added, and who and when.
type userSkill struct {
	*Skill
	disabled bool
	by       string
	at       int64
}

// skillSnap is the skills as they stand: never changed once made, so that
// a chat reads them on a fib worker without a lock; a change makes a new
// one.
type skillSnap struct {
	builtin []*Skill
	user    []userSkill
	off     map[string]bool // the built-ins disabled
}

// skillSet is the assistant's skills.
type skillSet struct {
	cur atomic.Pointer[skillSnap]
	// mu orders the changes, each written to the store before it is used.
	mu sync.Mutex
}

// find returns the skill named name, whether it is built in, and whether it
// is enabled; nil when there is none.
func (s *skillSnap) find(name string) (sk *Skill, builtin, enabled bool) {
	for _, b := range s.builtin {
		if b.Name == name {
			return b, true, !s.off[name]
		}
	}
	for _, u := range s.user {
		if u.Name == name {
			return u.Skill, false, !u.disabled
		}
	}
	return nil, false, false
}

// enabled returns the enabled skills, the built-ins first.
func (s *skillSnap) enabled() []*Skill {
	var out []*Skill
	for _, b := range s.builtin {
		if !s.off[b.Name] {
			out = append(out, b)
		}
	}
	for _, u := range s.user {
		if !u.disabled {
			out = append(out, u.Skill)
		}
	}
	return out
}

// kept returns the snapshot as the store keeps it.
func (s *skillSnap) kept() keptSkills {
	var k keptSkills
	for _, b := range s.builtin {
		if s.off[b.Name] {
			k.Disabled = append(k.Disabled, b.Name)
		}
	}
	for _, u := range s.user {
		k.User = append(k.User, keptSkill{Source: u.Source, Disabled: u.disabled, By: u.by, At: u.at})
	}
	return k
}

// clone returns a copy of s to change.
func (s *skillSnap) clone() *skillSnap {
	c := &skillSnap{builtin: s.builtin, user: slices.Clone(s.user), off: map[string]bool{}}
	for k, v := range s.off {
		c.off[k] = v
	}
	return c
}

// loadSkills reads the built-in skills and the store's; a user's skill
// that no longer parses, as after an upgrade, is left out and said in the
// log, and is gone from the store at the next change.
func (a *Assistant) loadSkills() error {
	builtin, err := builtinSkills()
	if err != nil {
		return err
	}
	snap := &skillSnap{builtin: builtin, off: map[string]bool{}}
	if a.o.Store != nil {
		if sec := a.o.Store.Section(skillsSection); sec != nil {
			var k keptSkills
			if err := json.Unmarshal(sec, &k); err != nil {
				a.o.Logf("assistant: the skills kept in the store do not load, and only the built-in ones are offered: %v", err)
			} else {
				for _, name := range k.Disabled {
					snap.off[name] = true
				}
				for _, ks := range k.User {
					sk, err := ParseSkill([]byte(ks.Source))
					if err == nil {
						if have, _, _ := snap.find(sk.Name); have != nil {
							err = fmt.Errorf("the name %s is taken", sk.Name)
						}
					}
					if err != nil {
						a.o.Logf("assistant: a skill kept in the store does not load, and is left out: %v", err)
						continue
					}
					snap.user = append(snap.user, userSkill{Skill: sk, disabled: ks.Disabled, by: ks.By, at: ks.At})
				}
			}
		}
	}
	a.skills.cur.Store(snap)
	return nil
}

// skillSnapshot returns the skills as they stand.
func (a *Assistant) skillSnapshot() *skillSnap { return a.skills.cur.Load() }

// EnabledSkills returns the skills users may run, the built-ins first: what
// the chat's catalog lists, and what an MCP server (I130) serves as its
// prompts.
func (a *Assistant) EnabledSkills() []*Skill { return a.skillSnapshot().enabled() }

// EnabledSkill returns the enabled skill named name, or nil.
func (a *Assistant) EnabledSkill(name string) *Skill {
	if sk, _, on := a.skillSnapshot().find(name); on {
		return sk
	}
	return nil
}

// Catalog returns the catalog of the enabled skills, as the system prompt
// lists them for the model to pick from: names and English descriptions.
func (a *Assistant) Catalog() []CatalogEntry {
	var out []CatalogEntry
	for _, sk := range a.EnabledSkills() {
		out = append(out, CatalogEntry{Name: sk.Name, Description: sk.Description})
	}
	return out
}

// changeSkills makes a change to the skills: fn changes a copy, which is
// written to the store and then used; fn's status, not 0, refuses it. It
// writes a file: on the pool.
func (a *Assistant) changeSkills(fn func(s *skillSnap) (int, string)) (int, string) {
	a.skills.mu.Lock()
	defer a.skills.mu.Unlock()
	next := a.skillSnapshot().clone()
	if status, msg := fn(next); status != 0 {
		return status, msg
	}
	if a.o.Store != nil {
		data, _ := json.Marshal(next.kept())
		if err := a.o.Store.SetSection(skillsSection, data); err != nil {
			return 500, "saving the skills: " + err.Error()
		}
	}
	a.skills.cur.Store(next)
	return 200, ""
}

// skillTool is a tool a skill names, as a user may call it: ok, or why not.
type skillTool struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Why  string `json:"why,omitempty"`
}

// skillTools says, for each tool sk names, whether a user of level may call
// it under settings s.
func (a *Assistant) skillTools(sk *Skill, level string, s *config.Assistant) []skillTool {
	out := make([]skillTool, 0, len(sk.Tools))
	for _, name := range sk.Tools {
		st := skillTool{Name: name}
		if t := a.registry.Tool(name); t == nil {
			st.Why = "this fit has no such tool"
		} else {
			st.OK, st.Why = t.Allowed(level, s)
		}
		out = append(out, st)
	}
	return out
}

// narrow returns the tools of base a chat running sk offers: those it
// names, and ask_user.
func narrow(base []*Tool, sk *Skill) []*Tool {
	var out []*Tool
	for _, t := range base {
		if t.Name == "ask_user" || slices.Contains(sk.Tools, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// skillModel returns the provider and model a chat running sk calls: the
// skill's own when it names one, as "provider/model" of a provider the
// settings have or a model of the default provider; prov and mod else.
func skillModel(s *config.Assistant, sk *Skill, prov, mod string) (string, string) {
	if sk == nil || sk.Model == "" {
		return prov, mod
	}
	if p, m, ok := strings.Cut(sk.Model, "/"); ok && m != "" && s.Provider(p) != nil {
		return p, m
	}
	return prov, sk.Model
}

// skillView is a skill as the Skills page sees it.
type skillView struct {
	Skill
	Builtin bool        `json:"builtin"`
	Enabled bool        `json:"enabled"`
	By      string      `json:"by,omitempty"`
	At      int64       `json:"at,omitempty"`
	State   []skillTool `json:"tool_state"`
	Source  string      `json:"source,omitempty"`
}

// view returns sk as user, of level, sees it; with its prompt and Markdown
// when whole.
func (a *Assistant) skillViewOf(snap *skillSnap, sk *Skill, level string, s *config.Assistant, whole bool) skillView {
	_, builtin, enabled := snap.find(sk.Name)
	v := skillView{Skill: *sk, Builtin: builtin, Enabled: enabled, State: a.skillTools(sk, level, s)}
	for _, u := range snap.user {
		if u.Name == sk.Name {
			v.By, v.At = u.by, u.at
		}
	}
	if whole {
		v.Source = sk.Source
	} else {
		v.Prompt = ""
	}
	return v
}

// The level a user needs to change the skills: they change what the
// assistant does for every user.
const skillChangeLevel = config.AssistantApply

// skillsHandler answers GET /api/skills: every skill, built-in and added,
// enabled or not, with the tools each names as this user may call them.
func (a *Assistant) skillsHandler(r *console.Request) {
	s := a.Settings()
	level := s.Level(r.User)
	snap := a.skillSnapshot()
	list := []skillView{}
	for _, b := range snap.builtin {
		list = append(list, a.skillViewOf(snap, b, level, s, false))
	}
	for _, u := range snap.user {
		list = append(list, a.skillViewOf(snap, u.Skill, level, s, false))
	}
	_ = r.JSON(200, map[string]any{"skills": list, "level": level, "can_change": levelRank(level) >= levelRank(skillChangeLevel),
		"change_level": skillChangeLevel, "kept": a.o.Store != nil, "max": MaxUserSkills, "max_bytes": MaxSkillBytes})
}

// skillBody is a skill sent from the page: its Markdown.
type skillBody struct {
	Source string `json:"source"`
}

// skillRefused is a skill's Markdown refused, with the line it is about.
type skillRefused struct {
	Error string `json:"error"`
	Line  int    `json:"line,omitempty"`
}

// parseBody reads a skill from the request, answering 400 with the line of
// what is wrong; nil then.
func parseBody(r *console.Request) *Skill {
	var body skillBody
	if !r.Decode(&body) {
		return nil
	}
	sk, err := ParseSkill([]byte(body.Source))
	if err != nil {
		se := err.(*SkillError)
		_ = r.JSON(400, skillRefused{Error: se.Error(), Line: se.Line})
		return nil
	}
	return sk
}

// skillsPost answers POST /api/skills: a skill added, its name not taken,
// within MaxUserSkills.
func (a *Assistant) skillsPost(r *console.Request) {
	if !a.mayChangeSkills(r) {
		return
	}
	sk := parseBody(r)
	if sk == nil {
		return
	}
	user := r.User
	r.Go(func() {
		status, msg := a.changeSkills(func(s *skillSnap) (int, string) {
			if have, builtin, _ := s.find(sk.Name); have != nil {
				return 409, nameTaken(sk.Name, builtin)
			}
			if len(s.user) >= MaxUserSkills {
				return 409, fmt.Sprintf("there are %d skills added, as many as may be: delete one first", MaxUserSkills)
			}
			s.user = append(s.user, userSkill{Skill: sk, by: user, at: nowMs()})
			return 0, ""
		})
		a.answerSkill(r, status, msg, sk.Name, "added")
	})
}

func nameTaken(name string, builtin bool) string {
	if builtin {
		return "a built-in skill is named " + name + ": give yours another name, and disable the built-in one if yours replaces it"
	}
	return "a skill is named " + name + " already"
}

// answerSkill answers a change of the skill name: its view, or the error.
func (a *Assistant) answerSkill(r *console.Request, status int, msg, name, did string) {
	if status != 200 {
		_ = console.Error(r.Context, status, msg)
		return
	}
	a.o.Logf("console: %q %s the assistant's skill %s", r.User, did, name)
	s := a.Settings()
	snap := a.skillSnapshot()
	if sk, _, _ := snap.find(name); sk != nil {
		_ = r.JSON(200, a.skillViewOf(snap, sk, s.Level(r.User), s, true))
		return
	}
	_ = r.JSON(200, map[string]string{"deleted": name})
}

// mayChangeSkills answers 403 to a user whose level does not change skills.
func (a *Assistant) mayChangeSkills(r *console.Request) bool {
	if level := a.Settings().Level(r.User); levelRank(level) < levelRank(skillChangeLevel) {
		_ = console.Error(r.Context, 403, "changing the skills needs the level "+skillChangeLevel+"; "+r.User+"'s is "+level)
		return false
	}
	return true
}

// skillHandler answers /api/skills/{name}: GET reads it whole, PUT
// replaces a skill added (its name may change), DELETE removes one, and
// POST .../enable and .../disable turn any on or off.
func (a *Assistant) skillHandler(r *console.Request) {
	name, action, _ := strings.Cut(strings.TrimPrefix(r.Path, "/api/skills/"), "/")
	method := r.Request.Method
	switch {
	case method == "GET" && action == "":
		s := a.Settings()
		snap := a.skillSnapshot()
		sk, _, _ := snap.find(name)
		if sk == nil {
			_ = console.Error(r.Context, 404, "no skill "+name)
			return
		}
		_ = r.JSON(200, a.skillViewOf(snap, sk, s.Level(r.User), s, true))
	case method == "PUT" && action == "":
		if !a.mayChangeSkills(r) {
			return
		}
		sk := parseBody(r)
		if sk == nil {
			return
		}
		user := r.User
		r.Go(func() {
			status, msg := a.changeSkills(func(s *skillSnap) (int, string) {
				i := slices.IndexFunc(s.user, func(u userSkill) bool { return u.Name == name })
				if i < 0 {
					if _, builtin, _ := s.find(name); builtin {
						return 409, "a built-in skill is not changed: export it, give it another name, and add it as yours"
					}
					return 404, "no skill " + name + " was added"
				}
				if have, builtin, _ := s.find(sk.Name); have != nil && sk.Name != name {
					return 409, nameTaken(sk.Name, builtin)
				}
				s.user[i] = userSkill{Skill: sk, disabled: s.user[i].disabled, by: user, at: nowMs()}
				return 0, ""
			})
			a.answerSkill(r, status, msg, sk.Name, "changed")
		})
	case method == "DELETE" && action == "":
		if !a.mayChangeSkills(r) {
			return
		}
		r.Go(func() {
			status, msg := a.changeSkills(func(s *skillSnap) (int, string) {
				i := slices.IndexFunc(s.user, func(u userSkill) bool { return u.Name == name })
				if i < 0 {
					if _, builtin, _ := s.find(name); builtin {
						return 409, "a built-in skill is not deleted: disable it"
					}
					return 404, "no skill " + name + " was added"
				}
				s.user = slices.Delete(s.user, i, i+1)
				return 0, ""
			})
			a.answerSkill(r, status, msg, name, "deleted")
		})
	case method == "POST" && (action == "enable" || action == "disable"):
		if !a.mayChangeSkills(r) {
			return
		}
		off := action == "disable"
		r.Go(func() {
			status, msg := a.changeSkills(func(s *skillSnap) (int, string) {
				if i := slices.IndexFunc(s.user, func(u userSkill) bool { return u.Name == name }); i >= 0 {
					s.user[i].disabled = off
					return 0, ""
				}
				if _, builtin, _ := s.find(name); builtin {
					if off {
						s.off[name] = true
					} else {
						delete(s.off, name)
					}
					return 0, ""
				}
				return 404, "no skill " + name
			})
			a.answerSkill(r, status, msg, name, action+"d")
		})
	default:
		_ = console.Error(r.Context, 405, "GET, PUT or DELETE a skill, or POST its enable or disable")
	}
}

// skillLoadArgs are skill_load's arguments.
type skillLoadArgs struct {
	Name string `json:"name"`
}

// skillLoadTool is skill_load: an enabled skill's whole prompt, which from
// then on is the chat's current skill.
func skillLoadTool() *Tool {
	return &Tool{Name: "skill_load", Group: GroupAgent, Level: config.AssistantRead,
		Description: "Load a skill of the catalog the system prompt lists, by its name: its whole prompt, which says how to do the task, after which your tools are the ones it names that the person's level allows. Call it when the person's request fits a skill, before you start on the task; there is one skill at a time.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"the skill's name, as the catalog lists it"}},"required":["name"]}`),
		Run:         toolSkillLoad}
}

func toolSkillLoad(c *Call) (string, error) {
	var args skillLoadArgs
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	sk := c.a.EnabledSkill(args.Name)
	if sk == nil {
		return "", fmt.Errorf("no enabled skill is named %q; the catalog in the system prompt lists them", args.Name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The skill %s (%s) is loaded: follow it for this task.", sk.Name, sk.Title)
	if c.loadSkill != nil {
		ok, not := c.loadSkill(sk)
		fmt.Fprintf(&b, " Your tools now: %s.", strings.Join(ok, ", "))
		if len(not) > 0 {
			fmt.Fprintf(&b, " Tools it names that you may not call: %s.", strings.Join(not, "; "))
		}
	}
	b.WriteString("\n\n" + skillBlock(sk))
	return b.String(), nil
}

// skillBlock is a skill's prompt as the model is given it: fit's own
// instructions for the task, which cannot close their own block.
func skillBlock(sk *Skill) string {
	return "<skill name=\"" + sk.Name + "\">\n" + strings.ReplaceAll(sk.Prompt, "</skill", `<\/skill`) + "\n</skill>"
}
