package twitch

import (
	"testing"
)

func TestParseChannel(t *testing.T) {
	ok := map[string]string{
		"https://www.twitch.tv/Some_Streamer":                "some_streamer",
		"twitch.tv/streamer?sr=a":                            "streamer",
		"https://m.twitch.tv/streamer/videos":                "streamer",
		"https://www.twitch.tv/popout/streamer/chat?popout=": "streamer",
		"https://www.twitch.tv/moderator/streamer":           "streamer",
		"  #Streamer  ": "streamer",
		"@streamer":     "streamer",
		"streamer":      "streamer",
	}
	for in, want := range ok {
		got, err := ParseChannel(in)
		if err != nil || got != want {
			t.Errorf("ParseChannel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "https://www.twitch.tv/", "кириллица", "a b", "https://youtube.com/@x y"} {
		if got, err := ParseChannel(in); err == nil {
			t.Errorf("ParseChannel(%q) = %q, want error", in, got)
		}
	}
}

func TestParseIRC(t *testing.T) {
	m := parseIRC(`@badge-info=;display-name=Some\sUser;emotes= :someuser!someuser@someuser.tmi.twitch.tv PRIVMSG #chan :hello !sad world`)
	if m.Command != "PRIVMSG" || m.nick() != "someuser" || m.Tags["display-name"] != "Some User" ||
		m.trailing() != "hello !sad world" || m.Params[0] != "#chan" {
		t.Errorf("unexpected parse: %+v", m)
	}
	if p := parseIRC("PING :tmi.twitch.tv"); p.Command != "PING" || p.trailing() != "tmi.twitch.tv" {
		t.Errorf("unexpected ping parse: %+v", p)
	}
	if j := parseIRC(":justinfan123!justinfan123@justinfan123.tmi.twitch.tv JOIN #chan"); j.Command != "JOIN" || j.nick() != "justinfan123" {
		t.Errorf("unexpected join parse: %+v", j)
	}
	if got := stripAction("\x01ACTION !sad\x01"); got != "!sad" {
		t.Errorf("stripAction = %q", got)
	}
}
