package ui

import (
	"strings"
	"testing"
)

func TestChromaCodeInlined(t *testing.T) {
	for name, page := range map[string]string{"index": Index, "overlay": string(Overlay)} {
		if strings.Contains(page, chromaPlaceholder) || !strings.Contains(page, "function createKeyer") {
			t.Errorf("%s.html: chroma.js not inlined", name)
		}
	}
}
