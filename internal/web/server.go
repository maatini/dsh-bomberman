// Package web owns HTTP routing and server-side HTML rendering. Pages are
// classic html/template documents; live updates arrive as HTMX OOB fragments.
package web

import (
	"bufio"
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"bomberman/internal/lobby"
	webassets "bomberman/web"
)

const formBodyLimit = 32 << 10

// Server renders the browser UI and serves the embedded static assets.
type Server struct {
	manager *lobby.Manager
	logger  *slog.Logger
	tmpl    *template.Template
	static  http.Handler
}

// New parses all embedded templates immediately so configuration errors are
// fatal at startup instead of surfacing on the first request.
func New(manager *lobby.Manager, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	funcs := template.FuncMap{
		"seq": func(n int) []int {
			if n <= 0 {
				return nil
			}
			out := make([]int, n)
			for i := range out {
				out[i] = i + 1
			}
			return out
		},
		"initial": func(s string) string {
			for _, r := range strings.TrimSpace(s) {
				return string(unicode.ToUpper(r))
			}
			return "?"
		},
		"pct": func(n, total int) float64 {
			if total <= 0 {
				return 0
			}
			return float64(n) * 100 / float64(total)
		},
	}
	tmpl, err := template.New("bomberman").Funcs(funcs).ParseFS(webassets.FS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	staticFS, err := fs.Sub(webassets.FS, "static")
	if err != nil {
		return nil, fmt.Errorf("static fs: %w", err)
	}
	static := http.StripPrefix("/static/", http.FileServer(http.FS(staticFS)))

	return &Server{
		manager: manager,
		logger:  logger,
		tmpl:    tmpl,
		static:  static,
	}, nil
}

// Handler builds the full HTTP tree, including the WebSocket endpoint.
func (s *Server) Handler(wsHandler http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /lobby/create", s.handleCreate)
	mux.HandleFunc("POST /lobby/join", s.handleJoinByCode)
	mux.HandleFunc("GET /room/{code}", s.handleRoomPage)
	mux.HandleFunc("POST /room/{code}/join", s.handleRoomJoin)
	mux.HandleFunc("POST /room/{code}/start", s.handleStart)
	mux.HandleFunc("POST /room/{code}/rematch", s.handleRematch)
	mux.HandleFunc("POST /room/{code}/bots", s.handleBots)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("GET /static/", s.staticHandler())
	if wsHandler != nil {
		mux.Handle("GET /ws/room/{code}", wsHandler)
	}
	return s.recoverer(s.logRequests(s.securityHeaders(mux)))
}

// RenderLive renders one WebSocket frame. Every top-level node is an HTMX
// out-of-band swap; the screen and chat tree are morphed, not replaced.
func (s *Server) RenderLive(code, token string) ([]byte, error) {
	view, err := s.manager.View(code, token)
	if err != nil {
		return nil, err
	}
	data := roomPageData{View: view}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "live", data); err != nil {
		return nil, fmt.Errorf("execute live template: %w", err)
	}
	return buf.Bytes(), nil
}

type indexData struct {
	Title    string
	Name     string
	RoomName string
	JoinCode string
	Error    string
}

type joinData struct {
	Title string
	Room  lobby.Summary
	Error string
}

type roomPageData struct {
	lobby.View
	Title    string
	WSURL    string
	ShareURL string
}

type errorPageData struct {
	Title   string
	Code    int
	Message string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := indexData{
		Title:    "Bomber Grid",
		JoinCode: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("room"))),
	}
	s.renderPage(w, r, http.StatusOK, "index.html", data)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	name := r.PostFormValue("name")
	roomName := r.PostFormValue("room_name")
	room, token, err := s.manager.Create(name, roomName)
	if err != nil {
		s.formFailure(w, r, http.StatusBadRequest, "index.html", indexData{
			Title: "Bomber Grid", Name: name, RoomName: roomName, Error: err.Error(),
		}, err)
		return
	}
	s.redirect(w, r, roomURL(room.Code(), token))
}

func (s *Server) handleJoinByCode(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	name := r.PostFormValue("name")
	code := r.PostFormValue("code")
	room, token, _, err := s.manager.Join(code, name)
	if err != nil {
		s.formFailure(w, r, http.StatusBadRequest, "index.html", indexData{
			Title: "Bomber Grid", Name: name, JoinCode: normalizeCode(code), Error: err.Error(),
		}, err)
		return
	}
	s.redirect(w, r, roomURL(room.Code(), token))
}

func (s *Server) handleRoomPage(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	summary, ok := s.manager.Summary(code)
	if !ok {
		s.renderNotFound(w, r, "Dieser Raum existiert nicht (mehr).")
		return
	}

	token := r.URL.Query().Get("p")
	if token != "" {
		view, err := s.manager.View(code, token)
		if err == nil {
			data := roomPageData{
				View:     view,
				Title:    view.Code + " · Bomber Grid",
				WSURL:    "/ws/room/" + url.PathEscape(view.Code) + "?p=" + url.QueryEscape(token),
				ShareURL: absoluteShareURL(r, view.SharePath),
			}
			s.renderPage(w, r, http.StatusOK, "room.html", data)
			return
		}
		// An old bookmark may point at an expired in-memory room session. Offer
		// the join form instead of a dead end.
		s.renderJoinPage(w, r, summary, "Deine Sitzung ist abgelaufen. Du kannst erneut beitreten.")
		return
	}
	s.renderJoinPage(w, r, summary, "")
}

func (s *Server) handleRoomJoin(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	if !s.parseForm(w, r) {
		return
	}
	name := r.PostFormValue("name")
	room, token, _, err := s.manager.Join(code, name)
	if err != nil {
		summary, ok := s.manager.Summary(code)
		if !ok {
			s.renderNotFound(w, r, err.Error())
			return
		}
		if isHTMX(r) {
			s.renderFragment(w, http.StatusOK, "form-error", map[string]string{"Message": err.Error()})
			return
		}
		s.renderJoinPageStatus(w, r, summary, err.Error(), http.StatusBadRequest)
		return
	}
	s.redirect(w, r, roomURL(room.Code(), token))
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.handlePhaseAction(w, r, false)
}

func (s *Server) handleRematch(w http.ResponseWriter, r *http.Request) {
	s.handlePhaseAction(w, r, true)
}

// handleBots adds or removes server-side bots for the lobby host.
func (s *Server) handleBots(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	room, ok := s.manager.Get(code)
	if !ok {
		s.renderNotFound(w, r, "Dieser Raum existiert nicht (mehr).")
		return
	}
	token := r.URL.Query().Get("p")
	if token == "" {
		http.Error(w, "missing player token", http.StatusUnauthorized)
		return
	}
	var err error
	if r.URL.Query().Get("op") == "remove" {
		err = room.RemoveBot(token)
	} else {
		err = room.AddBot(token)
	}
	if err != nil {
		if isHTMX(r) {
			s.renderFragment(w, http.StatusOK, "toast", map[string]string{"Message": err.Error()})
			return
		}
		s.redirect(w, r, roomURL(code, token))
		return
	}
	if isHTMX(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.redirect(w, r, roomURL(code, token))
}

func (s *Server) handlePhaseAction(w http.ResponseWriter, r *http.Request, rematch bool) {
	code := normalizeCode(r.PathValue("code"))
	room, ok := s.manager.Get(code)
	if !ok {
		s.renderNotFound(w, r, "Dieser Raum existiert nicht (mehr).")
		return
	}
	token := r.URL.Query().Get("p")
	if token == "" {
		http.Error(w, "missing player token", http.StatusUnauthorized)
		return
	}
	var err error
	if rematch {
		err = room.Rematch(token)
	} else {
		err = room.Start(token)
	}
	if err != nil {
		if isHTMX(r) {
			s.renderFragment(w, http.StatusOK, "toast", map[string]string{"Message": err.Error()})
			return
		}
		s.redirect(w, r, roomURL(code, token))
		return
	}
	if isHTMX(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.redirect(w, r, roomURL(code, token))
}

func (s *Server) renderJoinPage(w http.ResponseWriter, r *http.Request, summary lobby.Summary, message string) {
	s.renderJoinPageStatus(w, r, summary, message, http.StatusOK)
}

func (s *Server) renderJoinPageStatus(w http.ResponseWriter, r *http.Request, summary lobby.Summary, message string, status int) {
	data := joinData{
		Title: "Raum " + summary.Code + " · Bomber Grid",
		Room:  summary,
		Error: message,
	}
	s.renderPage(w, r, status, "join.html", data)
}

func (s *Server) renderNotFound(w http.ResponseWriter, r *http.Request, message string) {
	s.renderPage(w, r, http.StatusNotFound, "notfound.html", errorPageData{
		Title: "Nicht gefunden · Bomber Grid", Code: http.StatusNotFound, Message: message,
	})
}

func (s *Server) formFailure(w http.ResponseWriter, r *http.Request, status int, page string, data indexData, err error) {
	if isHTMX(r) {
		s.renderFragment(w, http.StatusOK, "form-error", map[string]string{"Message": err.Error()})
		return
	}
	s.renderPage(w, r, status, page, data)
}

func (s *Server) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, formBodyLimit)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("render page", "template", name, "error", err, "path", r.URL.Path)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) renderFragment(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("render fragment", "template", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		s.static.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self'; "+
				"connect-src 'self' ws: wss:; "+
				"base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Hijack is required so the WebSocket upgrader can take over the connection
// even though HTTP logging wraps the ResponseWriter.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return h.Hijack()
}

// Unwrap lets http.ResponseController reach optional interfaces.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		s.logger.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start))
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				s.logger.Error("panic in handler", "path", r.URL.Path, "panic", rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, location string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func isHTMX(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("HX-Request"), "true")
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func roomURL(code, token string) string {
	return "/room/" + url.PathEscape(code) + "?p=" + url.QueryEscape(token)
}

func absoluteShareURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" || proto == "http" {
		scheme = proto
	}
	return scheme + "://" + r.Host + path
}
