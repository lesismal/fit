package admin

import (
	"context"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
)

// TestConsoleExtras: a manager's console (I148) asks its gate about each
// request beside the modules' own, and bounds the assistant's levels by
// the user's roles; it says who its users are and their levels.
func TestConsoleExtras(t *testing.T) {
	f := &consoleFixture{fixture: startEnv(t, doc("hi"), &config.Admin{}, runtime.Env{})}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	settings := &config.Assistant{DefaultLevel: config.AssistantApply}
	var mu sync.Mutex
	var asked []string
	x := ConsoleExtras{
		Gate: func(user, method, path, query string) string {
			mu.Lock()
			defer mu.Unlock()
			asked = append(asked, user+" "+method+" "+path+"?"+query)
			if method == "PUT" {
				return "not ada's to change"
			}
			return ""
		},
		AssistantCap: func(string) string { return config.AssistantRead },
	}
	cs, err := StartConsoleWith(testEngine(t), c, &config.Admin{Assistant: settings}, f.configs, nil, nil, nil, x)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Shutdown(context.Background()) })
	f.console, f.url = cs, strings.TrimSuffix(cs.URL(), "/")
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != 200 {
		t.Fatal(code)
	}
	if code, body, _ := b.do("PUT", "/api/m/admin/config?x=1", `{}`); code != 403 || !strings.Contains(body, "not ada's to change") {
		t.Errorf("%d %s", code, body)
	}
	if code, body, _ := b.do("GET", "/api/m/ai/tools", ""); code != 200 || !strings.Contains(body, `"level":"read"`) {
		t.Errorf("%d %s", code, body)
	}
	mu.Lock()
	if len(asked) != 2 || asked[0] != "ada PUT /api/m/admin/config?x=1" {
		t.Errorf("%q", asked)
	}
	mu.Unlock()
	if u := cs.Users(); len(u) != 1 || u[0] != "ada" || cs.AssistantLevel("ada") != config.AssistantApply {
		t.Errorf("%v %s", u, cs.AssistantLevel("ada"))
	}
	// Without the assistant, no level.
	c2 := *c
	cs2, err := StartConsole(testEngine(t), &c2, &config.Admin{}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs2.Shutdown(context.Background()) })
	if cs2.AssistantLevel("ada") != "" {
		t.Error("a level without the assistant")
	}
}
