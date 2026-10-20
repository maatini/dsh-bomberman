package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bomberman/internal/lobby"
	webapp "bomberman/internal/web"
)

func newTestApp(t *testing.T) (*lobby.Manager, *webapp.Server, *httptest.Server) {
	t.Helper()
	manager := lobby.NewManager(nil)
	t.Cleanup(manager.Close)
	app, err := webapp.New(manager, nil)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	server := httptest.NewServer(app.Handler(nil))
	t.Cleanup(server.Close)
	return manager, app, server
}

func TestLandingCreateAndJoinPages(t *testing.T) {
	_, _, server := newTestApp(t)

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "BOMBER") {
		t.Fatalf("landing status=%d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("missing CSP header")
	}

	form := url.Values{"name": {"Ada"}, "room_name": {"Testbunker"}}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/lobby/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	location := resp.Header.Get("HX-Redirect")
	if !strings.HasPrefix(location, "/room/") || !strings.Contains(location, "?p=") {
		t.Fatalf("unexpected redirect %q", location)
	}

	resp, err = http.Get(server.URL + location)
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("room status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, `ws-connect="/ws/room/`) || !strings.Contains(body, "Testbunker") {
		t.Fatal("room page misses WebSocket root or name")
	}
	if !strings.Contains(body, `id="sound-toggle"`) || !strings.Contains(body, "/static/js/sound.js") {
		t.Fatal("room page misses the arcade sound toggle")
	}

	code := strings.Split(strings.TrimPrefix(location, "/room/"), "?")[0]
	resp, err = http.Get(server.URL + "/room/" + code)
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Lobby betreten") {
		t.Fatalf("join page status=%d", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/room/ZZZZZ")
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "existiert nicht") {
		t.Fatalf("missing room status=%d", resp.StatusCode)
	}
}

func TestStartAndRematchRoutes(t *testing.T) {
	manager, app, server := newTestApp(t)
	room, host, err := manager.Create("Ada", "Arena")
	if err != nil {
		t.Fatal(err)
	}
	_, second, _, err := manager.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	_ = room.Connect(second)

	postHX := func(path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, server.URL+path, nil)
		req.Header.Set("HX-Request", "true")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	resp := postHX("/room/" + room.Code() + "/start?p=" + host)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start status = %d", resp.StatusCode)
	}
	live, err := app.RenderLive(room.Code(), host)
	if err != nil {
		t.Fatalf("RenderLive: %v", err)
	}
	html := string(live)
	for _, needle := range []string{`hx-swap-oob="morph:#screen"`, `id="game-board"`, "countdown-overlay"} {
		if !strings.Contains(html, needle) {
			t.Fatalf("live frame missing %q", needle)
		}
	}

	// Rematch is not allowed while the first round is running. HTMX receives
	// an OOB toast frame instead of a swapped page.
	resp = postHX("/room/" + room.Code() + "/rematch?p=" + host)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rematch error status = %d", resp.StatusCode)
	}
	toast := readBody(t, resp)
	if !strings.Contains(toast, `id="toast"`) || !strings.Contains(toast, "nicht möglich") {
		t.Fatalf("unexpected error fragment: %s", toast)
	}
}

func TestJoinValidationFragmentAndStatic(t *testing.T) {
	manager, _, server := newTestApp(t)
	room, _, err := manager.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{"name": {"A"}}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/room/"+room.Code()+"/join", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "form-error") {
		t.Fatalf("validation status=%d body=%s", resp.StatusCode, body)
	}

	resp, err = http.Get(server.URL + "/static/js/sound.js")
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "AudioContext") {
		t.Fatalf("sound asset status=%d len=%d", resp.StatusCode, len(body))
	}

	resp, err = http.Get(server.URL + "/static/vendor/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	body = readBody(t, resp)
	if resp.StatusCode != http.StatusOK || len(body) < 10000 {
		t.Fatalf("static asset status=%d len=%d", resp.StatusCode, len(body))
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-cache") {
		t.Fatal("static cache header missing")
	}

	resp, err = http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if body = readBody(t, resp); strings.TrimSpace(body) != "ok" {
		t.Fatalf("health = %q", body)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBotRouteAddsAndRemovesBots(t *testing.T) {
	manager, _, server := newTestApp(t)
	room, hostToken, err := manager.Create("Ada", "Bot Bunker")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(hostToken)

	botURL := server.URL + "/room/" + room.Code() + "/bots?p=" + hostToken + "&op=add"
	req, _ := http.NewRequest(http.MethodPost, botURL, nil)
	req.Header.Set("HX-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("add bot status = %d, want 204", resp.StatusCode)
	}

	resp, err = http.Get(server.URL + "/room/" + room.Code() + "?p=" + hostToken)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	for _, needle := range []string{"Bot Nova", "bot-chip", "op=remove"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("room page after adding a bot missing %q", needle)
		}
	}

	removeURL := server.URL + "/room/" + room.Code() + "/bots?p=" + hostToken + "&op=remove"
	req, _ = http.NewRequest(http.MethodPost, removeURL, nil)
	req.Header.Set("HX-Request", "true")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove bot status = %d, want 204", resp.StatusCode)
	}

	v, err := room.View(hostToken)
	if err != nil {
		t.Fatal(err)
	}
	if v.BotCount != 0 || v.PlayerCount != 1 {
		t.Fatalf("bots after removal: count=%d players=%d", v.BotCount, v.PlayerCount)
	}
}
