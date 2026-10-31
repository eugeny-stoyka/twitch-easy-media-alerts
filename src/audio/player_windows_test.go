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

func TestPlayerOverlays(t *testing.T) {
	if testing.Short() {
		t.Skip("plays (silently) on the real audio device")
	}
	const sound = `C:\Windows\Media\Alarm01.wav`
	if _, err := os.Stat(sound); err != nil {
		t.Skip("test sound not found")
	}

	p := NewPlayer()
	if err := p.Play("a", sound, "", 0, false, nil); err != nil {
		t.Fatal(err)
	}
	// Multi-play sounds start over it and over each other, up to MaxOverlays.
	ended := make(chan error, MaxOverlays)
	for i := range MaxOverlays {
		if err := p.PlayOverlay(sound, "", 0, func(_ string, err error) { ended <- err }); err != nil {
			t.Fatalf("multi-play sound %d: %v", i, err)
		}
	}
	if err := p.PlayOverlay(sound, "", 0, nil); !errors.Is(err, ErrTooMany) || !errors.Is(err, ErrBusy) {
		t.Fatalf("past the cap: %v, want ErrTooMany (which is an ErrBusy)", err)
	}
	if cur := p.Current(); cur != "a" {
		t.Errorf("Current = %q: multi-play sounds must not take the slot", cur)
	}
	// They don't make single-play sounds wait either: the slot is still a's.
	if err := p.Play("b", sound, "", 0, false, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second single-play sound: %v, want ErrBusy", err)
	}

	time.Sleep(300 * time.Millisecond) // let them all actually play at once
	p.Stop()                           // stops every sound
	for range MaxOverlays {
		select {
		case err := <-ended:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Stop did not end the multi-play sounds")
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for p.Active() {
		if time.Now().After(deadline) {
			t.Fatal("still active after Stop")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
