package web

import (
	"bytes"
	"strings"
	"testing"

	"bomberman/internal/game"
	"bomberman/internal/lobby"
)

// TestGameTemplateRendersPowerUpsAndBlast guards methods and CSS hook names
// that are only reachable once a power-up has been collected or a brick broke.
func TestGameTemplateRendersPowerUpsAndBlast(t *testing.T) {
	manager := lobby.NewManager(nil)
	t.Cleanup(manager.Close)
	server, err := New(manager, nil)
	if err != nil {
		t.Fatal(err)
	}

	cells := make([][]game.Cell, 3)
	for y := range cells {
		cells[y] = make([]game.Cell, 3)
		for x := range cells[y] {
			cells[y][x] = game.Cell{Tile: game.TileFloor, Class: "floor"}
		}
	}
	cells[0][1] = game.Cell{Tile: game.TileFloor, Class: "floor boom", Boom: true}
	cells[1][1] = game.Cell{Tile: game.TileFloor, Class: "floor", PowerUp: game.PowerUpRange}
	cells[2][2] = game.Cell{Tile: game.TileBrick, Class: "brick", PowerUp: game.PowerUpBomb}

	data := roomPageData{
		View: lobby.View{
			Code:      "ABCDE",
			Name:      "Template Test",
			Phase:     "playing",
			PhaseText: "Runde läuft",
			Game: &game.Snapshot{
				Width:     3,
				Height:    3,
				Countdown: 0,
				Cells:     cells,
				Players: []game.PlayerSnapshot{{
					ID: "p1", Name: "Ada", Color: "#ff7a3d", X: 1, Y: 1,
					Lives: 3, MaxBombs: 2, BombRange: 3, Alive: true, IsYou: true,
				}},
			},
		},
	}
	var buf bytes.Buffer
	if err := server.tmpl.ExecuteTemplate(&buf, "live", data); err != nil {
		t.Fatalf("execute live template: %v", err)
	}
	html := buf.String()
	for _, needle := range []string{
		`hx-swap-oob="morph:#screen"`,
		`powerup powerup-range`,
		`powerup powerup-bomb`,
		`↔`,
		`class="cell floor boom"`,
		`left: 33.333`,
		`id="player-p1"`,
		`data-tick="0"`,
		`data-finished="false"`,
		`data-player-id="p1"`,
		`data-lives="3"`,
		`data-max-bombs="2"`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("rendered game template missing %q", needle)
		}
	}

	// Sound hooks for the result jingle must survive into the finished state.
	data.Game.Finished = true
	data.Game.WinnerID = "p1"
	data.Game.WinnerName = "Ada"
	buf.Reset()
	if err := server.tmpl.ExecuteTemplate(&buf, "live", data); err != nil {
		t.Fatalf("execute finished template: %v", err)
	}
	finished := buf.String()
	for _, needle := range []string{`data-finished="true"`, `data-winner-id="p1"`} {
		if !strings.Contains(finished, needle) {
			t.Fatalf("finished game template missing %q", needle)
		}
	}
}

// TestGameTemplateRendersBotsSuddenDeathAndNewPowerUps covers the arena hooks
// for the bot badge, the sudden-death banner and the kick/remote/shield set.
func TestGameTemplateRendersBotsSuddenDeathAndNewPowerUps(t *testing.T) {
	manager := lobby.NewManager(nil)
	t.Cleanup(manager.Close)
	server, err := New(manager, nil)
	if err != nil {
		t.Fatal(err)
	}

	cells := make([][]game.Cell, 3)
	for y := range cells {
		cells[y] = make([]game.Cell, 3)
		for x := range cells[y] {
			cells[y][x] = game.Cell{Tile: game.TileFloor, Class: "floor"}
		}
	}
	cells[0][0] = game.Cell{Tile: game.TileFloor, Class: "floor", PowerUp: game.PowerUpKick}
	cells[0][1] = game.Cell{Tile: game.TileFloor, Class: "floor", PowerUp: game.PowerUpRemote}
	cells[0][2] = game.Cell{Tile: game.TileFloor, Class: "floor", PowerUp: game.PowerUpShield}

	data := roomPageData{
		View: lobby.View{
			Code:        "ABCDE",
			Name:        "Bot Test",
			Phase:       "playing",
			PhaseText:   "Runde läuft",
			PlayerCount: 2,
			Game: &game.Snapshot{
				Width:        3,
				Height:       3,
				Started:      true,
				SuddenDeath:  true,
				MatchSeconds: 120,
				Cells:        cells,
				Players: []game.PlayerSnapshot{
					{ID: "p1", Name: "Ada", Color: "#ff7a3d", X: 1, Y: 1, Lives: 3, Alive: true, IsYou: true, Remote: true},
					{ID: "bot-1", Name: "Bot Rex", Color: "#42c9ff", X: 2, Y: 1, Lives: 2, Alive: true, IsBot: true, Kick: true, Shield: true},
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := server.tmpl.ExecuteTemplate(&buf, "live", data); err != nil {
		t.Fatalf("execute live template: %v", err)
	}
	html := buf.String()
	for _, needle := range []string{
		`data-sudden="true"`,
		`sudden-strip`,
		`sudden-strip-title`,
		`sudden-badge`,
		`powerup powerup-kick`,
		`powerup powerup-remote`,
		`powerup powerup-shield`,
		`bot-chip`,
		`id="player-bot-1"`,
		`is-bot`,
		`data-kick="true"`,
		`data-remote="true"`,
		`data-shield="true"`,
		`⊣`,
		`✹`,
		`⛨`,
	} {
		if !strings.Contains(html, needle) {
			t.Fatalf("rendered bot/sudden-death template missing %q", needle)
		}
	}

	// The lobby exposes the bot controls to the host.
	data.View.Phase = "lobby"
	data.View.PhaseText = "Lobby"
	data.View.IsHost = true
	data.View.CanAddBot = true
	data.View.CanRemoveBot = true
	data.View.BotCount = 1
	data.View.ConnectedPlayers = 2
	data.View.Players = []lobby.MemberView{
		{ID: "p1", Name: "Ada", Color: "#ff7a3d", Slot: 0, IsHost: true, IsYou: true, Connected: true},
		{ID: "bot-1", Name: "Bot Rex", Color: "#42c9ff", Slot: 1, IsBot: true, Connected: true},
	}
	data.View.Game = nil
	buf.Reset()
	if err := server.tmpl.ExecuteTemplate(&buf, "live", data); err != nil {
		t.Fatalf("execute lobby template: %v", err)
	}
	lobbyHTML := buf.String()
	for _, needle := range []string{
		`/bots?p=`,
		`op=add`,
		`op=remove`,
		`+ Bot`,
		`bot-chip`,
		`meta-pill-bot`,
	} {
		if !strings.Contains(lobbyHTML, needle) {
			t.Fatalf("rendered lobby template missing %q", needle)
		}
	}
}
