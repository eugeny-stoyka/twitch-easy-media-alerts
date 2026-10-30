package overlay

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"twitch_easy_media_alerts/src/config"
)

func TestOverlayServer(t *testing.T) {
	file := filepath.Join(t.TempDir(), "clip.mp4")
	os.WriteFile(file, []byte("0123456789"), 0o644)
	reports := make(chan Report, 1)
	s, _, err := Start(0, "0123456789abcdef", func(id, part string) (string, bool) {
		return file, id == "clip" && part == ""
	}, func(r Report) { reports <- r }, nil)
	if err != nil {
		t.Fatal(err)
	}

	get := func(url string, header ...string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", url, nil)
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	if resp, body := get(s.PageURL(PageOBS)); resp.StatusCode != 200 || !strings.Contains(body, "EventSource") {
		t.Errorf("overlay: %d", resp.StatusCode)
	}
	noToken := s.base[:strings.Index(s.base[7:], "/")+8] + PageOBS
	if resp, _ := get(noToken); resp.StatusCode != 404 {
		t.Errorf("overlay without token: %d, want 404", resp.StatusCode)
	}
	if resp, body := get(s.base+"media/clip", "Range", "bytes=2-4"); resp.StatusCode != 206 || body != "234" ||
		resp.Header.Get("Content-Type") != "video/mp4" || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("media range: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if resp, _ := get(s.base + "media/other"); resp.StatusCode != 404 {
		t.Errorf("unknown media: %d, want 404", resp.StatusCode)
	}

	resp, err := http.Post(s.base+"report", "text/plain", strings.NewReader(`{"type":"ended","play":7}`))
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("report: %v %v", resp, err)
	}
	if r := <-reports; r.Type != "ended" || r.Play != 7 {
		t.Errorf("report = %+v", r)
	}

	// A play sent before any page is connected is delivered on connect,
	// after the current config.
	s.SetConfig(config.VideoSettings{Width: 1280, Height: 720, Background: "#00ff00"})
	s.Send(map[string]any{"type": "play", "play": 1}, PageWindow)
	ev, err := http.Get(s.base + "events?kind=window")
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Body.Close()
	sc := bufio.NewScanner(ev.Body)
	var data []string
	for len(data) < 2 && sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			data = append(data, d)
		}
	}
	if len(data) < 2 || !strings.Contains(data[0], `"config"`) || !strings.Contains(data[1], `"play"`) {
		t.Errorf("events = %q", data)
	}
}
