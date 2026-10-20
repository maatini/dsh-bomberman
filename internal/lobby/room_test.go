package lobby

import (
	"errors"
	"strings"
	"testing"
	"time"

	"bomberman/internal/game"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(nil)
	t.Cleanup(m.Close)
	return m
}

func TestRoomLifecycleAndHostStart(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "Test Arena")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if room.Code() == "" || len(room.Code()) != roomCodeLength {
		t.Fatalf("unexpected code %q", room.Code())
	}
	if err := room.Connect(host); err != nil {
		t.Fatalf("connect host: %v", err)
	}

	_, joinToken, spectator, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if spectator {
		t.Fatal("Byte should join as player")
	}
	if err := room.Connect(joinToken); err != nil {
		t.Fatalf("connect second: %v", err)
	}

	hostView, err := room.View(host)
	if err != nil {
		t.Fatalf("host view: %v", err)
	}
	if !hostView.IsHost || !hostView.CanStart || hostView.ConnectedPlayers != 2 {
		t.Fatalf("unexpected host view: %+v", hostView)
	}
	if hostView.Phase != string(PhaseLobby) {
		t.Fatalf("phase = %s", hostView.Phase)
	}
	if err := room.Start(joinToken); !errors.Is(err, ErrNotHost) {
		t.Fatalf("non-host start error = %v", err)
	}
	if err := room.Start(host); err != nil {
		t.Fatalf("host start: %v", err)
	}
	view, err := room.View(host)
	if err != nil {
		t.Fatal(err)
	}
	if view.Phase != string(PhasePlaying) {
		t.Fatalf("phase after start = %s", view.Phase)
	}
	if view.Game == nil || len(view.Game.Players) != 2 {
		t.Fatalf("game not created: %+v", view.Game)
	}
	if view.Game.CountdownSeconds != 3 {
		t.Fatalf("countdown = %d", view.Game.CountdownSeconds)
	}

	// A late arrival becomes a spectator.
	_, spectatorToken, isSpectator, err := m.Join(room.Code(), "Late")
	if err != nil {
		t.Fatal(err)
	}
	if !isSpectator {
		t.Fatal("late join should be a spectator")
	}
	if err := room.Connect(spectatorToken); err != nil {
		t.Fatal(err)
	}
	lateView, _ := room.View(spectatorToken)
	if !lateView.IsSpectator || lateView.CanStart {
		t.Fatalf("unexpected spectator view: %+v", lateView)
	}
}

func TestStartRequiresTwoConnectedPlayers(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := room.Connect(host); err != nil {
		t.Fatal(err)
	}
	if err := room.Start(host); !errors.Is(err, ErrNotEnoughPlayers) {
		t.Fatalf("start error = %v, want ErrNotEnoughPlayers", err)
	}
	// An offline lobby member must not count.
	_, other, _, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	_ = other
	if err := room.Start(host); !errors.Is(err, ErrNotEnoughPlayers) {
		t.Fatalf("offline member counted: %v", err)
	}
}

func TestHostTransferOnDisconnect(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	_, second, _, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	_ = room.Connect(second)
	room.Disconnect(host)

	// A quick reload keeps the crown while inside the grace window.
	room.mu.Lock()
	room.maybeTransferHostLocked(time.Now())
	room.mu.Unlock()
	if v, _ := room.View(host); !v.IsHost {
		t.Fatal("host lost the role immediately on disconnect")
	}

	// After the grace period the connected player takes over.
	room.mu.Lock()
	room.members[host].offlineSince = time.Now().Add(-hostGrace - time.Second)
	room.maybeTransferHostLocked(time.Now())
	room.mu.Unlock()
	v, _ := room.View(second)
	if !v.IsHost {
		t.Fatal("second player should have been promoted to host")
	}
	// A reconnecting original host is a normal player now.
	_ = room.Connect(host)
	vHost, _ := room.View(host)
	if vHost.IsHost {
		t.Fatal("reconnected host should not steal the role back")
	}
}

func TestDisconnectedPlayerForfeitsAfterGrace(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	_, second, _, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	_ = room.Connect(second)
	if err := room.Start(host); err != nil {
		t.Fatal(err)
	}
	room.Disconnect(second)

	room.mu.Lock()
	// Fast-forward through the countdown so the match is active.
	for i := 0; i < 61 && room.game != nil; i++ {
		room.game.Tick()
	}
	room.members[second].offlineSince = time.Now().Add(-reconnectGrace - time.Second)
	room.forfeitAwayLocked(time.Now())
	room.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := room.View(host)
		if v.Phase == string(PhaseFinished) {
			if v.Game == nil || !v.Game.Finished || v.Game.WinnerName != "Ada" {
				t.Fatalf("unexpected finish state: %+v", v.Game)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("room did not finish after disconnect grace")
}

func TestOfflineLobbyMemberBecomesSpectatorWhenRoundStarts(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	_, second, _, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	_, late, _, err := m.Join(room.Code(), "Chip")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	_ = room.Connect(second)
	if err := room.Start(host); err != nil {
		t.Fatal(err)
	}
	// Chip was offline when the round started and connects mid-round.
	if err := room.Connect(late); err != nil {
		t.Fatal(err)
	}
	v, err := room.View(late)
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsSpectator || !v.You.Spectator {
		t.Fatal("late reconnecting lobby member should be a spectator")
	}
	for _, p := range v.Game.Players {
		if p.IsYou {
			t.Fatal("spectator must not own a game figure")
		}
	}
}

func TestChatValidationAndRateLimit(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := room.Say(host, "  Hallo   Welt  "); err != nil {
		t.Fatalf("Say: %v", err)
	}
	v, _ := room.View(host)
	if len(v.Chat) == 0 {
		t.Fatal("chat empty")
	}
	last := v.Chat[len(v.Chat)-1]
	if last.Text != "Hallo Welt" {
		t.Fatalf("chat text = %q", last.Text)
	}
	if err := room.Say(host, "zu schnell"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limit error = %v", err)
	}
	if _, err := room.View("unknown"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unknown token error = %v", err)
	}
}

func TestUniqueNamesAndSpectatorWhenFull(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	tokens := []string{host}
	for _, name := range []string{"Ada", "Byte", "Chip"} {
		_, token, _, err := m.Join(room.Code(), name)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, token)
	}
	v, _ := room.View(host)
	if v.PlayerCount != MaxPlayers {
		t.Fatalf("player count = %d", v.PlayerCount)
	}
	names := map[string]bool{}
	for _, p := range v.Players {
		if names[p.Name] {
			t.Fatalf("duplicate name %q", p.Name)
		}
		names[p.Name] = true
	}
	_, _, spectator, err := m.Join(room.Code(), "Overflow")
	if err != nil {
		t.Fatal(err)
	}
	if !spectator {
		t.Fatal("fifth participant should be spectator")
	}
}

func TestSummaryAndRoomNotFound(t *testing.T) {
	m := newTestManager(t)
	room, _, err := m.Create("Ada", "Meine Arena")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := m.Summary(strings.ToLower(room.Code()))
	if !ok {
		t.Fatal("summary not found")
	}
	if s.Name != "Meine Arena" || s.JoinAs != "player" {
		t.Fatalf("unexpected summary: %+v", s)
	}
	if _, ok := m.Summary("ZZZZZ"); ok {
		t.Fatal("unexpected room")
	}
	if _, _, _, err := m.Join("ZZZZZ", "Byte"); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("join missing room error = %v", err)
	}
}

func TestSubscribeReceivesUpdates(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	updates, cancel, err := room.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_ = room.Connect(host)
	select {
	case <-updates:
	default:
		t.Fatal("expected an update after connect")
	}
}

func TestNameValidation(t *testing.T) {
	m := newTestManager(t)
	if _, _, err := m.Create("A", ""); err == nil {
		t.Fatal("short host name accepted")
	}
	room, _, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.Join(room.Code(), " "); err == nil {
		t.Fatal("empty join name accepted")
	}
}

func TestHostAddsBotAndStartsSolo(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := room.Connect(host); err != nil {
		t.Fatal(err)
	}
	if err := room.AddBot(host); err != nil {
		t.Fatalf("AddBot: %v", err)
	}
	v, err := room.View(host)
	if err != nil {
		t.Fatal(err)
	}
	if v.PlayerCount != 2 || v.BotCount != 1 || v.ConnectedPlayers != 2 {
		t.Fatalf("unexpected counts: players=%d bots=%d connected=%d", v.PlayerCount, v.BotCount, v.ConnectedPlayers)
	}
	if !v.CanStart {
		t.Fatalf("solo round with a bot should be startable: %+v", v.StartHint)
	}
	if !v.CanAddBot || !v.CanRemoveBot {
		t.Fatal("host should be able to manage bots")
	}
	bots := 0
	for _, p := range v.Players {
		if p.IsBot {
			bots++
			if !p.Connected {
				t.Fatal("bots are always available")
			}
		}
	}
	if bots != 1 {
		t.Fatalf("bot players = %d", bots)
	}

	if err := room.Start(host); err != nil {
		t.Fatalf("start solo: %v", err)
	}
	started, _ := room.View(host)
	if started.Game == nil || len(started.Game.Players) != 2 {
		t.Fatalf("game players = %+v", started.Game)
	}
	if !started.Game.Players[1].IsBot {
		t.Fatal("bot was not seeded into the round")
	}
	if err := room.AddBot(host); !errors.Is(err, ErrWrongPhase) {
		t.Fatalf("AddBot during a round = %v, want ErrWrongPhase", err)
	}
}

func TestBotManagementRequiresHostAndLobby(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	_, other, _, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	if err := room.AddBot(other); !errors.Is(err, ErrNotHost) {
		t.Fatalf("non-host AddBot = %v, want ErrNotHost", err)
	}
	if err := room.RemoveBot(other); !errors.Is(err, ErrNotHost) {
		t.Fatalf("non-host RemoveBot = %v, want ErrNotHost", err)
	}
	if err := room.AddBot("unknown"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unknown token AddBot = %v, want ErrInvalidToken", err)
	}
	if err := room.RemoveBot(host); err == nil {
		t.Fatal("removing a missing bot should fail")
	}

	// Host, Byte and two bots fill the four player slots.
	for i := 0; i < 2; i++ {
		if err := room.AddBot(host); err != nil {
			t.Fatalf("AddBot %d: %v", i, err)
		}
	}
	if err := room.AddBot(host); err == nil {
		t.Fatal("a full room must reject additional bots")
	}
	if v, _ := room.View(host); v.PlayerCount != MaxPlayers || v.CanAddBot {
		t.Fatalf("full room view: players=%d canAdd=%v", v.PlayerCount, v.CanAddBot)
	}
	if err := room.RemoveBot(host); err != nil {
		t.Fatalf("RemoveBot: %v", err)
	}
	if v, _ := room.View(host); v.BotCount != 1 {
		t.Fatalf("bots after removal = %d", v.BotCount)
	}
}

func TestHostTransferSkipsBots(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	if err := room.AddBot(host); err != nil {
		t.Fatal(err)
	}
	room.Disconnect(host)

	room.mu.Lock()
	room.members[host].offlineSince = time.Now().Add(-hostGrace - time.Second)
	room.maybeTransferHostLocked(time.Now())
	room.mu.Unlock()
	if v, _ := room.View(host); !v.IsHost {
		t.Fatal("a bot must never inherit the host role")
	}

	_, second, _, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(second)
	room.mu.Lock()
	room.maybeTransferHostLocked(time.Now())
	room.mu.Unlock()
	if v, _ := room.View(second); !v.IsHost {
		t.Fatal("the human player should take over the host role")
	}
}

func TestManagerGameConfigAppliesToRooms(t *testing.T) {
	m := newTestManager(t)
	cfg := game.DefaultConfig()
	cfg.MatchTicks = 10 * game.TickRate
	m.SetGameConfig(cfg)

	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	room.mu.Lock()
	got := room.gameCfg.MatchTicks
	room.mu.Unlock()
	if got != cfg.MatchTicks {
		t.Fatalf("room match ticks = %d, want %d", got, cfg.MatchTicks)
	}
	_ = room.Connect(host)
	if err := room.AddBot(host); err != nil {
		t.Fatal(err)
	}
	if err := room.Start(host); err != nil {
		t.Fatal(err)
	}
	v, err := room.View(host)
	if err != nil {
		t.Fatal(err)
	}
	if v.Game == nil || v.Game.MatchSeconds != 10 {
		t.Fatalf("game match seconds = %+v", v.Game)
	}
}

func TestHumanReplacesBotInFullLobby(t *testing.T) {
	m := newTestManager(t)
	room, host, err := m.Create("Ada", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = room.Connect(host)
	for i := 0; i < MaxPlayers-1; i++ {
		if err := room.AddBot(host); err != nil {
			t.Fatalf("AddBot %d: %v", i, err)
		}
	}
	v, _ := room.View(host)
	if v.PlayerCount != MaxPlayers || v.BotCount != MaxPlayers-1 {
		t.Fatalf("full bot lobby: players=%d bots=%d", v.PlayerCount, v.BotCount)
	}

	_, token, spectator, err := m.Join(room.Code(), "Byte")
	if err != nil {
		t.Fatal(err)
	}
	if spectator {
		t.Fatal("a human should replace a bot instead of becoming a spectator")
	}
	_ = room.Connect(token)
	v, _ = room.View(host)
	if v.PlayerCount != MaxPlayers || v.BotCount != MaxPlayers-2 {
		t.Fatalf("after replacement: players=%d bots=%d", v.PlayerCount, v.BotCount)
	}
	if s, _ := m.Summary(room.Code()); s.JoinAs != "player" {
		t.Fatalf("summary JoinAs = %q, want player while bots remain", s.JoinAs)
	}
}
