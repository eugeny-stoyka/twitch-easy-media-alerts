package audio

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlayerOneAtATime(t *testing.T) {
	if testing.Short() {
		t.Skip("plays (silently) on the real audio device")
	}
	const sound = `C:\Windows\Media\Alarm01.wav` // a few seconds long
	if _, err := os.Stat(sound); err != nil {
		t.Skip("test sound not found")
	}

	p := NewPlayer()
	var mu sync.Mutex
	var events []string
	p.OnChange = func(key string) {
		mu.Lock()
		events = append(events, key)
		mu.Unlock()
	}
	eventsSoFar := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(events, ",")
	}

	if err := p.Play("a", sound, "", 0, false, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let it actually start playing
	if err := p.Play("b", sound, "", 0, false, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second sound while busy: %v, want ErrBusy", err)
	}

	done := make(chan error, 1)
	if err := p.Play("c", sound, "", 0, true, func(_ string, err error) { done <- err }); err != nil {
		t.Fatal(err)
	}
	// The interrupted sound must be over before the new one starts.
	if got := eventsSoFar(); got != "a,,c" {
		t.Fatalf("events after interrupt = %q, want a,,c", got)
	}
	if cur := p.Current(); cur != "c" {
		t.Fatalf("Current = %q, want c", cur)
	}

	p.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not end playback")
	}
	if cur := p.Current(); cur != "" {
		t.Fatalf("Current after Stop = %q", cur)
	}
	if got := eventsSoFar(); got != "a,,c," {
		t.Fatalf("events = %q, want a,,c,", got)
	}
}
