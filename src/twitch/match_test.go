package twitch

import (
	"testing"
	"twitch_easy_media_alerts/src/config"
)

func TestMatchCommand(t *testing.T) {
	cmds := []config.Command{
		{Enabled: false, Trigger: "!off"},
		{Enabled: true, Trigger: "!sad"},
		{Enabled: true, Trigger: "!Angry"},
		{Enabled: true, Trigger: "  "},
		{Enabled: true, Trigger: "good night"},
	}
	cases := map[string]int{
		"!sad":                  1,
		"ну всё   !SAD":         1,
		"!angry !sad":           1, // list order wins, not message order
		"!sadness":              -1,
		"x!sad":                 -1,
		"!off":                  -1,
		"!angry":                2,
		"!angry \U000E0000":     2, // 7TV/Chatterino duplicate bypass
		"Good   Night everyone": 4,
		"goodnight":             -1,
		"":                      -1,
	}
	for msg, want := range cases {
		if got := MatchCommand(cmds, msg); got != want {
			t.Errorf("MatchCommand(%q) = %d, want %d", msg, got, want)
		}
	}
}

func TestMatchVideoCommands(t *testing.T) {
	videos := []config.VideoCommand{{Enabled: false, Trigger: "!off"}, {Enabled: true, Trigger: "!Clip"}}
	if got := MatchCommand(videos, "смотри !clip"); got != 1 {
		t.Errorf("MatchCommand = %d, want 1", got)
	}
	if got := MatchCommand(videos, "!off"); got != -1 {
		t.Errorf("disabled video matched: %d", got)
	}
}

func TestRewardKey(t *testing.T) {
	for in, want := range map[string]string{"!Sad": "sad", "  sad ": "sad", "Sad   Song": "sad song", "!": ""} {
		if got := RewardKey(in); got != want {
			t.Errorf("RewardKey(%q) = %q, want %q", in, got, want)
		}
	}
}
