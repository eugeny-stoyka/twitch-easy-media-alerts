package twitch

import (
	"strings"
	"unicode"
)

// normalizeWords lowercases text and collapses it to single-space separated
// words, dropping invisible characters chat clients append to bypass
// Twitch's duplicate-message filter.
func normalizeWords(text string) string {
	words := strings.Fields(strings.ToLower(text))
	out := words[:0]
	for _, w := range words {
		w = strings.TrimFunc(w, func(r rune) bool {
			return !unicode.IsPrint(r) || unicode.Is(unicode.Cf, r)
		})
		if w != "" {
			out = append(out, w)
		}
	}
	return strings.Join(out, " ")
}

// Command is a chat command (or reward) of any kind: a sound, a video or a GIF.
type Command interface {
	Activation() (enabled bool, trigger string)
}

// MatchCommand returns the index of the first enabled command whose trigger
// appears in the message as a whole word (or word sequence), or -1.
func MatchCommand[T Command](cmds []T, text string) int {
	msg := " " + normalizeWords(text) + " "
	for i, c := range cmds {
		enabled, text := c.Activation()
		if !enabled {
			continue
		}
		trigger := normalizeWords(text)
		if trigger != "" && strings.Contains(msg, " "+trigger+" ") {
			return i
		}
	}
	return -1
}

// RewardKey normalises a reward title for matching: case, extra spaces and
// a leading "!" don't matter.
func RewardKey(s string) string {
	return strings.TrimPrefix(normalizeWords(s), "!")
}
