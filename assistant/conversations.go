package assistant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/assistant/model"
)

// The bounds of the conversations kept, beside the settings' limits.
var (
	// MaxConversationBytes bounds a conversation as it is kept: past it, its
	// earliest turns go, those a summary stands for first.
	MaxConversationBytes = 512 << 10
	// MaxUserBytes bounds a user's conversations together: past it, the
	// oldest go.
	MaxUserBytes = 8 << 20
	// MaxMessageBytes bounds a user's message.
	MaxMessageBytes = 32 << 10
)

// chatsSection is the prefix of the store's sections that keep the users'
// conversations, one section each, named by a hash of the user's name.
const chatsSection = "assistant.chats."

// Msg is a message of a conversation as it is kept: the model's message,
// and when it was made, and how it ended.
type Msg struct {
	model.Message
	// At is when it was made, in Unix milliseconds.
	At int64 `json:"at,omitempty"`
	// Stopped is an answer the user stopped, kept as far as it came.
	Stopped bool `json:"stopped,omitempty"`
	// Cut is a tool's result cut to its bound.
	Cut bool `json:"cut,omitempty"`
	// Error is an answer that failed, and why; it is not sent again.
	Error string `json:"error,omitempty"`
}

// Conversation is a user's conversation with the assistant.
type Conversation struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	Messages []Msg  `json:"messages"`
	// Summary stands for the messages before Folded in the calls made from
	// here: the early turns, summarized by the model when the conversation
	// outgrew its model's window.
	Summary string `json:"summary,omitempty"`
	Folded  int    `json:"folded,omitempty"`

	busy *chat // the chat under way, nil when none
}

// convSummary is a conversation as GET /api/conversations lists it.
type convSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
	Messages int    `json:"messages"`
	Busy     bool   `json:"busy"`
}

// userChats are a user's conversations and the tokens they spent today.
type userChats struct {
	loaded bool
	convs  []*Conversation // oldest first, by Updated
	day    string          // the UTC day tokens counts
	tokens int64
}

// keptChats is a user's section of the store.
type keptChats struct {
	Conversations []*Conversation `json:"conversations"`
	Day           string          `json:"day,omitempty"`
	Tokens        int64           `json:"tokens,omitempty"`
}

// chats are every user's conversations, loaded from the store as each user
// first asks for them, and written back by a saver of their own.
type chats struct {
	mu    sync.Mutex
	users map[string]*userChats
	dirty map[string]bool
	wake  chan struct{}
	stop  chan struct{}
	done  chan struct{}
}

func sectionOf(user string) string {
	h := sha256.Sum256([]byte(user))
	return chatsSection + hex.EncodeToString(h[:12])
}

func newID() string {
	var b [9]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func nowMs() int64 { return time.Now().UnixMilli() }

// today is the UTC day the tokens are counted for.
var today = func() string { return time.Now().UTC().Format("2006-01-02") }

// userLocked returns user's conversations, read from the store the first
// time: a.chats.mu is held. Reading a section copies it from memory, and
// decoding is bounded by the section's size; callers are on the pool.
func (a *Assistant) userLocked(user string) *userChats {
	u := a.chats.users[user]
	if u == nil {
		u = &userChats{}
		a.chats.users[user] = u
	}
	if u.loaded {
		return u
	}
	u.loaded = true
	if a.o.Store == nil {
		return u
	}
	sec := a.o.Store.Section(sectionOf(user))
	if sec == nil {
		return u
	}
	var k keptChats
	if err := json.Unmarshal(sec, &k); err != nil {
		a.o.Logf("assistant: the conversations kept for %q do not load, and are started again: %v", user, err)
		return u
	}
	for _, c := range k.Conversations {
		if c != nil && c.ID != "" {
			u.convs = append(u.convs, c)
		}
	}
	sort.SliceStable(u.convs, func(i, j int) bool { return u.convs[i].Updated < u.convs[j].Updated })
	u.day, u.tokens = k.Day, k.Tokens
	return u
}

// usedToday returns the tokens u spent today; a.chats.mu is held.
func (u *userChats) usedToday() int64 {
	if d := today(); u.day != d {
		u.day, u.tokens = d, 0
	}
	return u.tokens
}

// conversation returns u's conversation id, or nil.
func (u *userChats) conversation(id string) *Conversation {
	for _, c := range u.convs {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// boundLocked drops what is past the bounds: the oldest conversations that
// are not under way past max, or past MaxUserBytes together; a.chats.mu is
// held.
func (u *userChats) boundLocked(max int) {
	sort.SliceStable(u.convs, func(i, j int) bool { return u.convs[i].Updated < u.convs[j].Updated })
	total := 0
	sizes := make([]int, len(u.convs))
	for i, c := range u.convs {
		sizes[i] = convBytes(c)
		total += sizes[i]
	}
	for i := 0; i < len(u.convs) && (len(u.convs) > max || total > MaxUserBytes); {
		if u.convs[i].busy != nil {
			i++
			continue
		}
		total -= sizes[i]
		u.convs = append(u.convs[:i], u.convs[i+1:]...)
		sizes = append(sizes[:i], sizes[i+1:]...)
	}
}

// convBytes is about the size of c as it is kept: its text, and a little
// for each message's JSON around it. It is counted, not encoded, since it
// runs under a.chats.mu, on the goroutines that end chats.
func convBytes(c *Conversation) int {
	n := 128 + len(c.ID) + len(c.Title) + len(c.Summary)
	for i := range c.Messages {
		m := &c.Messages[i]
		n += 64 + len(m.Content) + len(m.Thinking) + len(m.ThinkingSignature) + len(m.ToolCallID) + len(m.ToolName) + len(m.Error)
		for _, tc := range m.ToolCalls {
			n += 48 + len(tc.ID) + len(tc.Name) + len(tc.Arguments) + len(tc.Signature)
		}
	}
	return n
}

// trimLocked keeps c within MaxConversationBytes: the messages a summary
// stands for go first, then the earliest turns; a.chats.mu is held.
func trimLocked(c *Conversation) {
	for convBytes(c) > MaxConversationBytes && len(c.Messages) > 1 {
		n := c.Folded
		if n == 0 {
			// The first turn: up to the next user message.
			n = 1
			for n < len(c.Messages) && c.Messages[n].Role != model.RoleUser {
				n++
			}
			if n >= len(c.Messages) {
				return // one turn, the one under way: left whole
			}
			if c.Summary == "" {
				c.Summary = "(The earliest turns of this conversation were dropped to keep it within its bound.)"
			}
		}
		c.Messages = append([]Msg(nil), c.Messages[n:]...)
		c.Folded = 0
	}
}

// title is a conversation's title from its first message.
func title(msg string) string {
	const n = 60
	s := msg
	for i, r := range s {
		if r == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) <= n {
		return s
	}
	for i := n; i > 0; i-- {
		if utf8.RuneStart(s[i]) {
			return s[:i] + "…"
		}
	}
	return s[:n]
}

// saveChats has user's conversations written to the store by the saver.
func (a *Assistant) saveChats(user string) {
	if a.o.Store == nil {
		return
	}
	a.chats.mu.Lock()
	a.chats.dirty[user] = true
	a.chats.mu.Unlock()
	select {
	case a.chats.wake <- struct{}{}:
	default:
	}
}

// saver writes the users' conversations that changed to the store, one
// user at a time, until the assistant closes, when it writes what is left.
func (a *Assistant) saver() {
	defer close(a.chats.done)
	for {
		select {
		case <-a.chats.wake:
			a.saveDirty()
		case <-a.chats.stop:
			a.saveDirty()
			return
		}
	}
}

func (a *Assistant) saveDirty() {
	for {
		a.chats.mu.Lock()
		var user string
		for u := range a.chats.dirty {
			user = u
			break
		}
		if user == "" {
			a.chats.mu.Unlock()
			return
		}
		delete(a.chats.dirty, user)
		u := a.chats.users[user]
		// A copy is taken under the lock, and encoded outside it: the
		// messages' own fields are never changed once made.
		k := keptChats{Conversations: make([]*Conversation, len(u.convs)), Day: u.day, Tokens: u.tokens}
		for i, c := range u.convs {
			cc := *c
			cc.Messages = slices.Clone(c.Messages)
			k.Conversations[i] = &cc
		}
		a.chats.mu.Unlock()
		data, _ := json.Marshal(k)
		if err := a.o.Store.SetSection(sectionOf(user), data); err != nil {
			a.o.Logf("assistant: the conversations of %q are kept in memory only: %v", user, err)
		}
	}
}
