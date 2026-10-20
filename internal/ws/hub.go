// Package ws implements the browser WebSocket transport. It authenticates a
// per-tab room token, forwards small JSON input messages into the lobby and
// streams render-ready HTML fragments back to HTMX.
package ws

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"bomberman/internal/lobby"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 25 * time.Second
	renderInterval = time.Second / 15 // 15 HTML updates per second
	maxInputBytes  = 2048
)

// Renderer turns the current room state into one HTML fragment containing OOB
// swap roots for HTMX. It is supplied by the web package.
type Renderer func(code, token string) ([]byte, error)

// Hub owns all live WebSocket clients.
type Hub struct {
	manager  *lobby.Manager
	render   Renderer
	logger   *slog.Logger
	upgrader websocket.Upgrader

	mu      sync.Mutex
	clients map[*client]struct{}
	closed  bool
}

// NewHub constructs a WebSocket hub.
func NewHub(manager *lobby.Manager, render Renderer, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		manager: manager,
		render:  render,
		logger:  logger,
		upgrader: websocket.Upgrader{
			HandshakeTimeout:  10 * time.Second,
			ReadBufferSize:    2048,
			WriteBufferSize:   4096,
			EnableCompression: false,
			CheckOrigin:       sameOrigin,
		},
		clients: make(map[*client]struct{}),
	}
}

// ServeHTTP upgrades /ws/room/{code}?p=<token>.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))
	token := r.URL.Query().Get("p")
	if code == "" || token == "" {
		http.Error(w, "room code and player token required", http.StatusUnauthorized)
		return
	}
	room, ok := h.manager.Get(code)
	if !ok {
		http.Error(w, "room not found", http.StatusNotFound)
		return
	}
	if _, err := room.View(token); err != nil {
		http.Error(w, "invalid player token", http.StatusUnauthorized)
		return
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	h.mu.Unlock()

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response.
		h.logger.Warn("websocket upgrade failed", "room", code, "error", err)
		return
	}

	c := &client{
		hub:   h,
		room:  room,
		token: token,
		conn:  conn,
		done:  make(chan struct{}),
	}
	h.add(c)
	if err := room.Connect(token); err != nil {
		h.remove(c)
		c.close()
		return
	}

	h.logger.Debug("websocket connected", "room", code, "remote", r.RemoteAddr)
	go c.readPump()
	c.writePump()
	h.remove(c)
	room.Disconnect(token)
	c.close()
	h.logger.Debug("websocket disconnected", "room", code, "remote", r.RemoteAddr)
}

// Close shuts down all active sockets. It is safe to call more than once.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

// ClientCount is useful for diagnostics and tests.
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) add(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		c.close()
		return
	}
	h.clients[c] = struct{}{}
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients and tests
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

type client struct {
	hub   *Hub
	room  *lobby.Room
	token string
	conn  *websocket.Conn

	done      chan struct{}
	closeOnce sync.Once
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

type inputMessage struct {
	Type    string `json:"type"`
	Action  string `json:"action"`
	Dir     string `json:"dir"`
	Message string `json:"message"`
}

func (c *client) readPump() {
	defer c.close()
	c.conn.SetReadLimit(maxInputBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		var msg inputMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			switch msg.Action {
			case "move":
				c.room.SetDirection(c.token, msg.Dir)
			case "bomb":
				c.room.PlaceBomb(c.token)
			case "detonate":
				c.room.Detonate(c.token)
			}
		case "chat":
			_ = c.room.Say(c.token, msg.Message)
		}
	}
}

func (c *client) writePump() {
	updates, cancel, err := c.room.Subscribe()
	if err != nil {
		return
	}
	defer cancel()

	renderTick := time.NewTicker(renderInterval)
	defer renderTick.Stop()
	pingTick := time.NewTicker(pingPeriod)
	defer pingTick.Stop()

	dirty := true
	if !c.sendRender() {
		return
	}
	dirty = false

	for {
		select {
		case <-c.done:
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			dirty = true
		case <-renderTick.C:
			if dirty {
				if !c.sendRender() {
					return
				}
				dirty = false
			}
		case <-pingTick.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				return
			}
		}
	}
}

func (c *client) sendRender() bool {
	payload, err := c.hub.render(c.room.Code(), c.token)
	if err != nil {
		c.hub.logger.Error("render live state", "room", c.room.Code(), "error", err)
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "render failed"),
			time.Now().Add(writeWait))
		return false
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return false
	}
	return true
}
