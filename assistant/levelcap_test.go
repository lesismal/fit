package assistant

import (
	"testing"

	"github.com/lesismal/fit/config"
)

// TestLevelCap: a user's level is the settings' bounded by LevelCap, as a
// manager's roles bound it (I148).
func TestLevelCap(t *testing.T) {
	s := &config.Assistant{DefaultLevel: config.AssistantPropose, Users: []config.AssistantUser{{Username: "ada", Level: config.AssistantRead}}}
	a := &Assistant{o: Options{LevelCap: func(user string) string {
		if user == "bob" {
			return config.AssistantRead
		}
		return config.AssistantApply
	}}}
	for user, want := range map[string]string{"ada": config.AssistantRead, "bob": config.AssistantRead, "cy": config.AssistantPropose} {
		if got := a.Level(s, user); got != want {
			t.Errorf("%s: %s", user, got)
		}
	}
	if got := (&Assistant{}).Level(s, "cy"); got != config.AssistantPropose {
		t.Error(got)
	}
}
