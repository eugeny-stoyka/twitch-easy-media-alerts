// Package overlay is the local web server behind the video/GIF page: it
// serves the page to the app's capture window and to OBS browser sources,
// pushes play events to them (server-sent events), takes their reports and
// serves the media files. It listens on localhost only, behind a secret path
// prefix.
package overlay

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"twitch_easy_media_alerts/src/config"
	"twitch_easy_media_alerts/src/ui"
)

// Pages that show videos: the window the app opens for window capture, and
// OBS browser sources.
const (
	PageWindow = "window"
	PageOBS    = "obs"
)

// Report is what a page tells us about a play request.
type Report struct {
	Type     string  `json:"type"` // started, ended, error
	Play     int     `json:"play"`
	Duration float64 `json:"duration"` // seconds, for "started"
	Message  string  `json:"message"`  // for "error"
}

// Server serves the video page (to the app's window and to OBS
// browser sources), pushes events to it (server-sent events) and serves the
// video files. It listens on localhost only, behind a secret path prefix.
type Server struct {
	base    string // http://127.0.0.1:port/token/
	media   func(id, part string) (path string, ok bool)
	report  func(Report)
	onPages func(obsPages int) // a page connected or disconnected

	mu          sync.Mutex
	clients     map[chan []byte]string // page kind of each connected page
	config      []byte                 // last "config" event, replayed to new pages
	pending     []byte                 // "play" event for a page that is still loading
	pendingKind string
}

// Start listens on 127.0.0.1:port, or on a random port if that
// one is taken; portBusy then reports that the browser source URL differs.
func Start(port int, token string, media func(id, part string) (string, bool), report func(Report), onPages func(obsPages int)) (s *Server, portBusy bool, err error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		portBusy = true
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return nil, true, err
		}
	}
	s = &Server{
		base:    fmt.Sprintf("http://%s/%s/", ln.Addr(), token),
		media:   media,
		report:  report,
		onPages: onPages,
		clients: map[chan []byte]string{},
	}
	p := "/" + token + "/"
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+p+PageWindow, s.servePage)
	mux.HandleFunc("GET "+p+PageOBS, s.servePage)
	mux.HandleFunc("GET "+p+"events", s.serveEvents)
	mux.HandleFunc("POST "+p+"report", s.serveReport)
	mux.HandleFunc("GET "+p+"media/{id}", s.serveMedia)
	mux.HandleFunc("GET "+p+"media/{id}/{part}", s.serveMedia)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return s, portBusy, nil
}

// PageURL is the address of the page of the given kind.
func (s *Server) PageURL(kind string) string {
	return s.base + kind
}

// MediaBase is the address media files are served under.
func (s *Server) MediaBase() string {
	return s.base + "media/"
}

// Count returns how many pages of the given kind are connected.
func (s *Server) Count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range s.clients {
		if k == kind {
			n++
		}
	}
	return n
}

func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(ui.Overlay)
}

func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")

	kind := PageWindow
	if r.URL.Query().Get("kind") == PageOBS {
		kind = PageOBS
	}
	ch := make(chan []byte, 16)
	s.mu.Lock()
	s.clients[ch] = kind
	initial := [][]byte{s.config}
	if s.pending != nil && s.pendingKind == kind {
		initial = append(initial, s.pending)
		s.pending = nil
	}
	s.mu.Unlock()
	s.pagesChanged()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
		s.pagesChanged()
	}()

	write := func(b []byte) bool {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	fmt.Fprint(w, "retry: 1000\n\n")
	for _, b := range initial {
		if b != nil && !write(b) {
			return
		}
	}
	flusher.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			if !write(b) {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) pagesChanged() {
	if s.onPages != nil {
		s.onPages(s.Count(PageOBS))
	}
}

func (s *Server) serveReport(w http.ResponseWriter, r *http.Request) {
	var rep Report
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&rep); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.report(rep)
	w.WriteHeader(http.StatusNoContent)
}

var mediaTypes = map[string]string{
	".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/mp4",
	".webm": "video/webm", ".mkv": "video/x-matroska", ".ogv": "video/ogg",
	".gif": "image/gif", ".webp": "image/webp", ".png": "image/png", ".apng": "image/apng",
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".ogg": "audio/ogg", ".oga": "audio/ogg",
	".flac": "audio/flac", ".m4a": "audio/mp4",
}

func (s *Server) serveMedia(w http.ResponseWriter, r *http.Request) {
	path, ok := s.media(r.PathValue("id"), r.PathValue("part"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if t, ok := mediaTypes[strings.ToLower(filepath.Ext(path))]; ok {
		w.Header().Set("Content-Type", t)
	}
	// The app's own window reads frames (colour picker, chroma key preview),
	// which needs CORS; the secret path prefix still guards the files.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f) // handles Range for seeking
}

// Send pushes an event to the connected pages of the given kind, or to all
// pages if kind is "". A play event for a kind with no page connected yet
// (the window is still loading) waits for the first one.
func (s *Server) Send(ev map[string]any, kind string) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = nil
	delivered := false
	for ch, k := range s.clients {
		if kind != "" && k != kind {
			continue
		}
		delivered = true
		select {
		case ch <- b:
		default:
		}
	}
	if !delivered && ev["type"] == "play" {
		s.pending, s.pendingKind = b, kind
	}
}

// SetConfig updates the resolution and window background on every page.
func (s *Server) SetConfig(v config.VideoSettings) {
	b, _ := json.Marshal(map[string]any{
		"type": "config", "width": v.Width, "height": v.Height, "background": v.Background,
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = b
	for ch := range s.clients {
		select {
		case ch <- b:
		default:
		}
	}
}
