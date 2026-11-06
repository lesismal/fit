package assistant

import (
	"encoding/json"
	"slices"
	"sync/atomic"
)

// skillName returns the name of the skill the chat runs, or "".
func (c *chat) skillName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skillNameLocked()
}

// skillNameLocked is skillName with c.mu held.
func (c *chat) skillNameLocked() string {
	if c.skill == nil {
		return ""
	}
	return c.skill.Name
}

// loadSkill makes sk the chat's skill, as skill_load asks: from the next
// model call its prompt is in the system prompt and the tools are narrowed
// to its own, and the conversation runs it from then on. A call given up
// on past its timeout changes nothing. It returns the tools offered from
// then on, and those sk names that are not, with why.
func (c *chat) loadSkill(sk *Skill, expired *atomic.Bool) (ok, not []string) {
	tools := narrow(c.base, sk)
	not = c.notOffered(sk, tools)
	if expired.Load() {
		return toolNames(tools), not
	}
	c.mu.Lock()
	c.skill, c.tools, c.inputs = sk, tools, nil
	c.mu.Unlock()
	c.a.chats.mu.Lock()
	c.conv.Skill, c.conv.SkillInputs = sk.Name, nil
	c.a.chats.mu.Unlock()
	c.emit(Event{Type: EventSkill, Skill: sk.Name})
	return toolNames(tools), not
}

// notOffered returns the tools sk names that tools, the chat's, leave out,
// each with why.
func (c *chat) notOffered(sk *Skill, tools []*Tool) []string {
	var out []string
	for _, st := range c.a.skillTools(sk, c.level, c.s) {
		if slices.ContainsFunc(tools, func(t *Tool) bool { return t.Name == st.Name }) {
			continue
		}
		why := st.Why
		if why == "" {
			why = "the model calls no tools"
		}
		out = append(out, st.Name+" ("+why+")")
	}
	return out
}

// toolNames returns the tools' names.
func toolNames(tools []*Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

// inputsJSON returns a skill's inputs as the prompt carries them, as data,
// redacted; nil for none.
func (r *redactor) inputsJSON(in map[string]string) []byte {
	if len(in) == 0 {
		return nil
	}
	data, _ := json.Marshal(in)
	return []byte(r.text(string(data)))
}
