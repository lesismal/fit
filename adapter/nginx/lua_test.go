package nginx

import (
	"slices"
	"strings"
	"testing"
)

// TestLuaBlocks: OpenResty's *_by_lua_block directives hold Lua, not
// directives: their braces in strings, comments and long brackets do not
// end them, the config around them converts, and each is reported at its
// line as not converted.
func TestLuaBlocks(t *testing.T) {
	src := `http {
    init_by_lua_block { require "cjson" }
    server {
        listen 8080;
        access_by_lua_block {
            -- a } in a comment
            local s = "a } in a string, \" and an escaped quote"
            local t = 'and } here'
            local u = [[a long
            } string]]
            local v = [==[ ]] } ]==]
            --[[ a long
            } comment ]]
            local w = {a = {1}}; if s and w[1] then ngx.exit(403) end
        }
        location /a {
            content_by_lua_block { ngx.say("{") }
            proxy_pass http://10.0.0.1:80;
        }
        set_by_lua $x 'return 1';
        location /b {
            return 200 "b";
        }
    }
}`
	out, warns := convert(t, src)
	if len(out.Routes) != 3 {
		t.Fatalf("%+v", out.Routes)
	}
	if got := warnedLines(warns); !slices.Equal(got, []int{2, 5, 17, 20}) {
		t.Errorf("%v\n%s", got, strings.Join(warns, "\n"))
	}
	for _, w := range warns {
		if !strings.Contains(w, "OpenResty's Lua is not converted") {
			t.Error(w)
		}
	}

	for _, c := range []struct{ src, err string }{
		{"http { content_by_lua_block { ngx.say(1) ", "line 1: content_by_lua_block: a block of Lua that does not end"},
		{"http {\n server {\n access_by_lua_block { local s = [[ never closed\n\n } } }", "line 3: access_by_lua_block: a block of Lua that does not end"},
		// A string left open ends at its line, as Lua's do.
		{"http { content_by_lua_block { local s = \"unended\n } }", "the config has no server to convert"},
	} {
		if _, err := Convert([]byte(c.src)); err == nil || err.Error() != c.err {
			t.Errorf("%q: %v", c.src, err)
		}
	}
}

// TestAuthBasicClosed: what auth_basic guards answers 401 rather than
// serving it to anyone, since its users are not converted; auth_basic off
// opens a location again, and return answers before auth, as in nginx.
func TestAuthBasicClosed(t *testing.T) {
	out, warns := convert(t, `http {
    server {
        listen 8080;
        root /srv;
        auth_basic "Staff";
        auth_basic_user_file /etc/nginx/htpasswd;
        location /api/ {
            proxy_pass http://10.0.0.1:80;
        }
        location /public/ {
            auth_basic off;
        }
        location = /ping {
            return 200 "pong";
        }
    }
}`)
	got := map[string]string{}
	for _, r := range out.Routes {
		got[r.Name] = r.Handler.Module
	}
	want := map[string]string{"server1-api": "respond", "server1-public": "static", "server1-ping": "respond", "server1-default": "respond"}
	for name, module := range want {
		if got[name] != module {
			t.Errorf("%s: %s, want %s (%v)", name, got[name], module, got)
		}
	}
	for _, r := range out.Routes {
		if r.Name != "server1-public" && r.Name != "server1-ping" && r.Handler.Params["status"] != float64(401) {
			t.Errorf("%s: %+v", r.Name, r.Handler)
		}
	}
	if !slices.Equal(warnedLines(warns), []int{5, 6}) || !strings.Contains(warns[0], "answers 401") || !strings.Contains(warns[1], "does not read htpasswd files") {
		t.Errorf("%q", warns)
	}
}
