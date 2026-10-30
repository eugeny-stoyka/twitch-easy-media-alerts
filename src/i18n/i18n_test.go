package i18n

import (
	"errors"
	"testing"
)

func TestLanguage(t *testing.T) {
	defer SetLanguage("en")
	base := NewError("busy", "занято")
	wrapped := WrapError("a video is playing", "играет видео", base)
	if L("yes", "да") != "yes" || wrapped.Error() != "a video is playing" {
		t.Error("English is not the default")
	}
	SetLanguage("ru")
	if L("yes", "да") != "да" || base.Error() != "занято" || !errors.Is(wrapped, base) {
		t.Error("Russian texts not picked")
	}
}
