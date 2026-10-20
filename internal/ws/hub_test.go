package ws_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"bomberman/internal/lobby"
	webapp "bomberman/internal/web"
	"bomberman/internal/ws"
)

func TestWebSocketLobbyGameAndChat(t *testing.T) {
	manager := lobby.NewManager(nil)
	t.Cleanup(manager.Close)
	app, err := webapp.New(manager, nil)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	hub := ws.NewHub(manager, app.RenderLive, nil)
	t.Cleanup(hub.Close)
	server := httptest.NewServer(app.Handler(hub))
	t.Cleanup(server.Close)

	room, hostToken, err := manager.Create("Ada", "Testarena")
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	_, secondToken, _, err := manager.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatalf("join room: %v", err)
	}

	hostConn := dial(t, server.URL, room.Code(), hostToken)
	t.Cleanup(func() { _ = hostConn.Close() })
	secondConn := dial(t, server.URL, room.Code(), secondToken)
	t.Cleanup(func() { _ = secondConn.Close() })

	waitUntil(t, 2*time.Second, func() bool {
		v, err := room.View(hostToken)
		return err == nil && v.ConnectedPlayers == 2
	}, "both websockets connected")

	frame := readUntil(t, hostConn, "2 bereit", 2*time.Second)
	if !strings.Contains(frame, "hx-swap-oob=\"morph:#screen\"") {
		t.Fatalf("missing OOB morph frame: %s", frame[:200])
	}
	if !strings.Contains(frame, "lobby-screen") {
		t.Fatalf("lobby fragment missing")
	}

	// Start through the real HTTP route, as a browser using HTMX would.
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/room/"+room.Code()+"/start?p="+hostToken, nil)
	req.Header.Set("HX-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("start request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start status = %d", resp.StatusCode)
	}

	frame = readUntil(t, hostConn, "id=\"game-board\"", 3*time.Second)
	if !strings.Contains(frame, "countdown-overlay") {
		t.Fatalf("first game frame should show countdown")
	}
	if !strings.Contains(frame, "Ada") || !strings.Contains(frame, "Byte") {
		t.Fatalf("game frame misses players")
	}

	// Chat travels over the same socket and is rendered into a separate OOB
	// target without replacing the chat form.
	chat, _ := json.Marshal(map[string]string{"type": "chat", "message": "Hallo aus dem Test"})
	if err := hostConn.WriteMessage(websocket.TextMessage, chat); err != nil {
		t.Fatalf("write chat: %v", err)
	}
	frame = readUntil(t, hostConn, "Hallo aus dem Test", 2*time.Second)
	if !strings.Contains(frame, "hx-swap-oob=\"morph:#chat-log\"") {
		t.Fatal("chat OOB target missing")
	}

	// Movement input is accepted. Wait out the 3 s countdown and verify a
	// position change in the rendered board.
	time.Sleep(3200 * time.Millisecond)
	move, _ := json.Marshal(map[string]string{"type": "input", "action": "move", "dir": "down"})
	if err := hostConn.WriteMessage(websocket.TextMessage, move); err != nil {
		t.Fatalf("write move: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		v, err := room.View(hostToken)
		if err != nil || v.Game == nil {
			return false
		}
		for _, p := range v.Game.Players {
			if p.IsYou && p.Y > 1 {
				return true
			}
		}
		return false
	}, "player moved down")

	// A raw invalid message must not tear the socket down.
	if err := hostConn.WriteMessage(websocket.TextMessage, []byte("not json")); err != nil {
		t.Fatalf("write invalid: %v", err)
	}
	_ = hostConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := hostConn.ReadMessage(); err != nil {
			t.Fatalf("socket closed after invalid message: %v", err)
		}
		break
	}
}

func TestWebSocketRejectsUnknownTokens(t *testing.T) {
	manager := lobby.NewManager(nil)
	t.Cleanup(manager.Close)
	app, err := webapp.New(manager, nil)
	if err != nil {
		t.Fatal(err)
	}
	hub := ws.NewHub(manager, app.RenderLive, nil)
	t.Cleanup(hub.Close)
	server := httptest.NewServer(app.Handler(hub))
	t.Cleanup(server.Close)

	room, _, err := manager.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/room/" + room.Code() + "?p=wrong"
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected handshake error")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want 401", resp)
	}
}

func dial(t *testing.T, serverURL, code, token string) *websocket.Conn {
	t.Helper()
	url := fmt.Sprintf("ws%s/ws/room/%s?p=%s", strings.TrimPrefix(serverURL, "http"), code, token)
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial ws (status %d): %v", status, err)
	}
	return conn
}

func readUntil(t *testing.T, conn *websocket.Conn, needle string, timeout time.Duration) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read websocket while waiting for %q: %v", needle, err)
		}
		if strings.Contains(string(payload), needle) {
			return string(payload)
		}
	}
}

func waitUntil(t *testing.T, timeout time.Duration, fn func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", description)
}
