package admin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
)

// TestConsoleAssistant: admin.assistant serves the module ai beside the
// config's own, its settings saved to the store as a section of it; without
// admin.assistant there is no module, and nothing to call.
func TestConsoleAssistant(t *testing.T) {
	f := &consoleFixture{fixture: start(t, doc("one"), &config.Admin{})}
	st, err := store.Create(filepath.Join(t.TempDir(), "fit.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write([]byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	f.configs.UseSealer(st)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	cs, err := StartConsole(testEngine(t), c, &config.Admin{Assistant: &config.Assistant{}}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Shutdown(context.Background()) })
	f.console, f.url = cs, strings.TrimSuffix(cs.URL(), "/")
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != 200 {
		t.Fatal(code)
	}
	if code, body, _ := b.do("GET", "/api/workbench", ""); code != 200 || !strings.Contains(body, `"ai"`) {
		t.Fatalf("%d %s", code, body)
	}
	if code, body, _ := b.do("GET", "/api/m/ai/settings", ""); code != 200 || !strings.Contains(body, `"settings":{}`) || !strings.Contains(body, `"store":true`) {
		t.Fatalf("%d %s", code, body)
	}
	key := "sk-console-0123456789"
	code, body, _ := b.do("PUT", "/api/m/ai/settings", `{"settings": {"providers": [{"name": "o", "kind": "ollama"}]}, "keys": {"o": "`+key+`"}}`)
	if code != 200 || strings.Contains(body, key) {
		t.Fatalf("%d %s", code, body)
	}
	if sec := st.Section("assistant"); !strings.Contains(string(sec), key) {
		t.Errorf("the store's section: %s", sec)
	}
	if _, err := st.Read(); err != nil {
		t.Error(err)
	}

	// Without admin.assistant, no module.
	plain, err := StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Shutdown(context.Background()) })
	f.url = strings.TrimSuffix(plain.URL(), "/")
	b = f.browser(t)
	b.login("ada", "pw")
	if code, body, _ := b.do("GET", "/api/workbench", ""); code != 200 || strings.Contains(body, `"ai"`) {
		t.Errorf("%d %s", code, body)
	}
	if code, _, _ := b.do("GET", "/api/m/ai/settings", ""); code != 404 {
		t.Errorf("the module without admin.assistant: %d", code)
	}
}
