package game

import (
	"strings"
	"testing"
)

func testSeeds(n int) []PlayerSeed {
	seeds := []PlayerSeed{
		{ID: "p1", Name: "Ada", Color: "#ff6b35"},
		{ID: "p2", Name: "Byte", Color: "#39c5ff"},
		{ID: "p3", Name: "Chip", Color: "#9be564"},
		{ID: "p4", Name: "Dot", Color: "#f4a7ff"},
	}
	return seeds[:n]
}

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.BrickDensity = 0
	cfg.PowerUpChance = 0
	cfg.CountdownTicks = 0
	cfg.BombFuseTicks = 3
	cfg.ExplosionTicks = 2
	cfg.InvulnerableTicks = 0
	cfg.RespawnTicks = 2
	return cfg
}

func mustGame(t *testing.T, n int, cfg Config) *Engine {
	t.Helper()
	e, err := New(testSeeds(n), cfg, 42)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestBoardAndInitialSnapshot(t *testing.T) {
	cfg := DefaultConfig()
	e := mustGame(t, 2, cfg)
	snap := e.Snapshot()
	if snap.Width != 15 || snap.Height != 13 {
		t.Fatalf("unexpected dimensions %dx%d", snap.Width, snap.Height)
	}
	if len(snap.Players) != 2 {
		t.Fatalf("want 2 players, got %d", len(snap.Players))
	}
	if snap.CountdownSeconds != 3 {
		t.Fatalf("want 3 second countdown, got %d", snap.CountdownSeconds)
	}
	if snap.Players[0].Lives != cfg.Lives || !snap.Players[0].Alive {
		t.Fatalf("unexpected player state: %+v", snap.Players[0])
	}
	if e.brd.at(Position{X: 1, Y: 1}) != TileFloor {
		t.Fatal("spawn should be floor")
	}
	if e.brd.at(Position{X: 0, Y: 0}) != TileSolid {
		t.Fatal("border should be solid")
	}
	if e.brd.at(Position{X: 2, Y: 2}) != TileSolid {
		t.Fatal("even/even pillar should be solid")
	}
	// The 3x3 spawn zone is clear of bricks.
	for y := 0; y <= 2; y++ {
		for x := 0; x <= 2; x++ {
			if x == 0 || y == 0 {
				continue
			}
			if e.brd.at(Position{X: x, Y: y}) == TileBrick {
				t.Fatalf("brick inside spawn zone at %d,%d", x, y)
			}
		}
	}
}

func TestCountdownAndResponsiveMovement(t *testing.T) {
	cfg := testConfig()
	cfg.CountdownTicks = 2
	e := mustGame(t, 2, cfg)
	e.SetDirection("p1", DirRight)
	if e.players["p1"].X != 1 {
		t.Fatal("player must not move during countdown")
	}
	e.Tick()
	e.Tick()
	if !e.started {
		t.Fatal("game should have started")
	}
	e.SetDirection("p1", DirRight)
	if got := e.players["p1"].X; got != 2 {
		t.Fatalf("responsive movement failed: x=%d", got)
	}
	// The cooldown blocks another step immediately.
	e.SetDirection("p1", DirRight)
	if got := e.players["p1"].X; got != 2 {
		t.Fatalf("moved during cooldown: x=%d", got)
	}
}

func TestBombBlocksMovementAndCanBeLeft(t *testing.T) {
	cfg := testConfig()
	cfg.BombFuseTicks = 100
	e := mustGame(t, 2, cfg)
	e.Tick() // start
	if !e.PlaceBomb("p1") {
		t.Fatal("could not place bomb")
	}
	e.SetDirection("p1", DirDown)
	if e.players["p1"].Y != 2 {
		t.Fatalf("expected to move off bomb, y=%d", e.players["p1"].Y)
	}
	e.SetDirection("p1", DirNone)
	// Walk back towards the bomb. The bomb tile must be blocked.
	for i := 0; i < 10; i++ {
		e.Tick()
	}
	e.SetDirection("p1", DirUp)
	if e.players["p1"].Y != 2 {
		t.Fatalf("walked into bomb tile, y=%d", e.players["p1"].Y)
	}
}

func TestExplosionDamageAndRespawn(t *testing.T) {
	cfg := testConfig()
	cfg.BombFuseTicks = 2
	cfg.RespawnTicks = 2
	cfg.InvulnerableTicks = 3
	e := mustGame(t, 2, cfg)
	e.Tick()
	e.players["p2"].X, e.players["p2"].Y = 2, 1 // next to p1's spawn
	if !e.PlaceBomb("p1") {
		t.Fatal("place bomb")
	}
	e.SetDirection("p1", DirDown) // leave own blast
	p2 := e.players["p2"]
	for i := 0; i < 10 && p2.Alive; i++ {
		e.Tick()
	}
	if p2.Lives != cfg.Lives-1 {
		t.Fatalf("expected one lost life, got %d", p2.Lives)
	}
	if p2.Alive || p2.Eliminated {
		t.Fatalf("p2 should be waiting to respawn: %+v", p2)
	}
	for i := 0; i < 10 && !p2.Alive; i++ {
		e.Tick()
	}
	if !p2.Alive {
		t.Fatal("p2 should have respawned")
	}
	if p2.X != p2.Spawn.X || p2.Y != p2.Spawn.Y {
		t.Fatal("respawn should be at spawn")
	}
	if !e.Snapshot().Players[1].Invulnerable {
		t.Fatal("respawned player should be invulnerable briefly")
	}
}

func TestChainReaction(t *testing.T) {
	cfg := testConfig()
	cfg.BombFuseTicks = 10
	cfg.ExplosionTicks = 2
	e := mustGame(t, 2, cfg)
	e.Tick()
	// Manually place two bombs one tile apart to make the timing deterministic.
	e.bombs[Position{X: 1, Y: 1}] = &Bomb{ID: "b1", Position: Position{1, 1}, OwnerID: "p1", Range: 2, DetonateAt: e.tick + 1}
	e.bombs[Position{X: 2, Y: 1}] = &Bomb{ID: "b2", Position: Position{2, 1}, OwnerID: "p2", Range: 2, DetonateAt: e.tick + 99}
	e.players["p1"].X, e.players["p1"].Y = 1, 3
	e.players["p2"].X, e.players["p2"].Y = 3, 3
	e.Tick()
	if len(e.bombs) != 0 {
		t.Fatalf("chain reaction left %d bombs", len(e.bombs))
	}
	for _, pos := range []Position{{1, 1}, {2, 1}, {3, 1}} {
		if _, ok := e.explosions[pos]; !ok {
			t.Fatalf("missing chain blast at %+v", pos)
		}
	}
}

func TestExplosionDestroysBrickAndDropsPowerUp(t *testing.T) {
	cfg := testConfig()
	cfg.PowerUpChance = 1
	cfg.BombFuseTicks = 1
	e := mustGame(t, 2, cfg)
	e.Tick()
	pos := Position{X: 2, Y: 1}
	e.brd.set(pos, TileBrick)
	if !e.PlaceBomb("p1") {
		t.Fatal("place bomb")
	}
	e.Tick() // fuse one
	e.Tick() // detonate and expire old tick
	if e.brd.at(pos) != TileFloor {
		t.Fatalf("brick was not destroyed: %s", e.brd.at(pos))
	}
	if e.powerups[pos] == PowerUpNone {
		t.Fatal("expected a power-up under the brick")
	}
	snap := e.Snapshot()
	if snap.Cells[pos.Y][pos.X].PowerUp == PowerUpNone {
		t.Fatal("power-up missing from snapshot")
	}
}

func TestPowerUpsApplyAndAreCapped(t *testing.T) {
	cfg := testConfig()
	cfg.MaxBombsCap = 2
	cfg.MaxRangeCap = 3
	e := mustGame(t, 2, cfg)
	e.Tick()
	p := e.players["p1"]
	pos := Position{X: p.X, Y: p.Y}
	e.powerups[pos] = PowerUpBomb
	e.Tick()
	if p.MaxBombs != 2 {
		t.Fatalf("bomb power-up not applied: %d", p.MaxBombs)
	}
	e.powerups[pos] = PowerUpBomb // extra pickups respect cap
	e.Tick()
	if p.MaxBombs != 2 {
		t.Fatalf("bomb power-up exceeded cap: %d", p.MaxBombs)
	}
	e.powerups[pos] = PowerUpRange
	e.Tick()
	if p.BombRange != 3 {
		t.Fatalf("range power-up not applied: %d", p.BombRange)
	}
	e.powerups[pos] = PowerUpSpeed
	e.Tick()
	if p.SpeedLevel != 2 {
		t.Fatalf("speed power-up not applied: %d", p.SpeedLevel)
	}
	if e.moveCooldown(p) != cfg.SpeedEvery {
		t.Fatalf("speed cooldown = %d, want %d", e.moveCooldown(p), cfg.SpeedEvery)
	}
}

func TestWinnerWhenOnlyOnePlayerRemains(t *testing.T) {
	cfg := testConfig()
	cfg.Lives = 1
	cfg.BombFuseTicks = 1
	e := mustGame(t, 2, cfg)
	e.Tick()
	e.players["p2"].X, e.players["p2"].Y = 2, 1
	if !e.PlaceBomb("p1") {
		t.Fatal("place bomb")
	}
	// Move p1 out of its own blast. Test-only direct positioning is fine here;
	// movement itself is covered by the dedicated movement tests.
	e.players["p1"].X, e.players["p1"].Y = 5, 5
	for i := 0; i < 10 && !e.Finished(); i++ {
		e.Tick()
	}
	if !e.Finished() {
		t.Fatal("game should be finished")
	}
	name, draw := e.Winner()
	if draw || name != "Ada" {
		t.Fatalf("unexpected winner name=%q draw=%v", name, draw)
	}
	if snap := e.Snapshot(); !snap.Finished || snap.WinnerName != "Ada" {
		t.Fatalf("snapshot winner: %+v", snap)
	}
}

func TestBombLimit(t *testing.T) {
	cfg := testConfig()
	e := mustGame(t, 2, cfg)
	e.Tick()
	if !e.PlaceBomb("p1") {
		t.Fatal("first bomb should succeed")
	}
	// Move two cells away, then try to place a second bomb with only one slot.
	e.SetDirection("p1", DirDown)
	for i := 0; i < 5; i++ {
		e.Tick()
	}
	if e.PlaceBomb("p1") {
		t.Fatal("second bomb must respect MaxBombs=1")
	}
}

func TestForfeitEliminatesPlayer(t *testing.T) {
	e := mustGame(t, 2, testConfig())
	e.Tick()
	if !e.Forfeit("p2") {
		t.Fatal("forfeit should succeed")
	}
	if !e.Finished() {
		t.Fatal("game should finish when one player forfeits")
	}
	name, draw := e.Winner()
	if draw || name != "Ada" {
		t.Fatalf("winner = %q draw=%v", name, draw)
	}
	snap := e.Snapshot()
	if !snap.Players[1].Eliminated || snap.Players[1].Lives != 0 {
		t.Fatalf("forfeited player state: %+v", snap.Players[1])
	}
	if e.Forfeit("missing") {
		t.Fatal("unknown player cannot forfeit")
	}
}

func TestSnapshotIsDefensiveCopy(t *testing.T) {
	e := mustGame(t, 2, DefaultConfig())
	snap := e.Snapshot()
	snap.Cells[1][1] = Cell{Tile: TileBrick}
	again := e.Snapshot()
	if again.Cells[1][1].Tile == TileBrick {
		t.Fatal("snapshot cells alias engine state")
	}
}

func TestValidName(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"  Ada  Lovelace ", "Ada Lovelace", false},
		{"", "", true},
		{"A", "", true},
		{"\x00\x01", "", true},
		{"This name is much too long", "This name is much t", false},
	}
	for _, tt := range tests {
		got, err := ValidName(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidName(%q) error = %v", tt.in, err)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ValidName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseDirection(t *testing.T) {
	for _, d := range []Direction{DirUp, DirDown, DirLeft, DirRight} {
		if got := ParseDirection(d.String()); got != d {
			t.Fatalf("round trip %v = %v", d, got)
		}
	}
	if got := ParseDirection("diagonal"); got != DirNone {
		t.Fatalf("unknown direction = %v", got)
	}
	if !strings.Contains(DirUp.String(), "up") {
		t.Fatal("direction string")
	}
}

func TestSuddenDeathOpensWallsAndRainsBombs(t *testing.T) {
	cfg := testConfig()
	cfg.MatchTicks = 20
	cfg.SuddenBombEvery = 2
	cfg.SuddenFuseTicks = 2
	cfg.BombFuseTicks = 100
	e := mustGame(t, 2, cfg)

	if e.Snapshot().SuddenDeath {
		t.Fatal("sudden death must not start immediately")
	}
	for i := 0; i < 19; i++ {
		e.Tick()
	}
	if e.Snapshot().SuddenDeath {
		t.Fatal("sudden death started too early")
	}
	e.Tick()
	snap := e.Snapshot()
	if !snap.SuddenDeath {
		t.Fatal("sudden death should have started")
	}
	if snap.SuddenDeathIn != 0 {
		t.Fatalf("sudden death countdown = %d, want 0", snap.SuddenDeathIn)
	}
	if e.brd.at(Position{X: 0, Y: 0}) != TileFloor {
		t.Fatal("outer wall ring should have crumbled")
	}
	if e.brd.at(Position{X: 0, Y: 5}) != TileFloor {
		t.Fatal("left border should be walkable during sudden death")
	}
	for i := 0; i < 40 && len(e.bombs) == 0; i++ {
		e.Tick()
	}
	if len(e.bombs) == 0 {
		t.Fatal("sudden death should drop bombs")
	}
}

func TestSuddenDeathCountdownInSnapshot(t *testing.T) {
	cfg := testConfig()
	cfg.MatchTicks = 100
	e := mustGame(t, 2, cfg)
	e.Tick()
	if got := e.Snapshot().SuddenDeathIn; got != 5 {
		t.Fatalf("SuddenDeathIn = %d, want 5", got)
	}
}

func TestKickPowerUpSlidesBomb(t *testing.T) {
	cfg := testConfig()
	cfg.KickEvery = 2
	e := mustGame(t, 2, cfg)
	e.Tick()
	p := e.players["p1"]
	p.X, p.Y = 4, 1
	p.Kick = true
	e.bombs[Position{X: 3, Y: 1}] = &Bomb{
		ID:         "kick-test",
		Position:   Position{X: 3, Y: 1},
		OwnerID:    "p2",
		Range:      2,
		DetonateAt: e.tick + 200,
	}
	e.SetDirection("p1", DirLeft)
	if _, ok := e.bombs[Position{X: 3, Y: 1}]; ok {
		t.Fatal("bomb was not kicked out of the way")
	}
	for i := 0; i < 20; i++ {
		e.Tick()
	}
	if len(e.bombs) != 1 {
		t.Fatalf("expected exactly one bomb, got %d", len(e.bombs))
	}
	for pos := range e.bombs {
		if pos.Y != 1 || pos.X > 2 {
			t.Fatalf("bomb did not slide left: %+v", pos)
		}
	}
}

func TestKickRequiresPowerUp(t *testing.T) {
	e := mustGame(t, 2, testConfig())
	e.Tick()
	p := e.players["p1"]
	p.X, p.Y = 4, 1
	e.bombs[Position{X: 3, Y: 1}] = &Bomb{
		ID:         "block",
		Position:   Position{X: 3, Y: 1},
		Range:      2,
		DetonateAt: e.tick + 200,
	}
	e.SetDirection("p1", DirLeft)
	if _, ok := e.bombs[Position{X: 3, Y: 1}]; !ok {
		t.Fatal("bomb moved without the kick power-up")
	}
	if p.X != 4 {
		t.Fatalf("player walked into the bomb: x=%d", p.X)
	}
}

func TestRemoteDetonatesPrimedBombs(t *testing.T) {
	cfg := testConfig()
	cfg.BombFuseTicks = 100
	e := mustGame(t, 2, cfg)
	e.Tick()
	p := e.players["p1"]
	p.Remote = true
	if !e.PlaceBomb("p1") {
		t.Fatal("place bomb")
	}
	pos := Position{X: p.X, Y: p.Y}
	e.SetDirection("p1", DirDown)
	e.SetDirection("p1", DirNone)
	if len(e.bombs) != 1 {
		t.Fatalf("bombs = %d", len(e.bombs))
	}
	if !e.Detonate("p1") {
		t.Fatal("remote detonate should succeed")
	}
	if len(e.bombs) != 0 {
		t.Fatal("bomb should have detonated")
	}
	if _, ok := e.explosions[pos]; !ok {
		t.Fatal("remote detonation did not create a blast")
	}
	// Without the power-up the request is ignored.
	e.players["p2"].Remote = false
	if e.Detonate("p2") {
		t.Fatal("detonate must require the remote power-up")
	}
}

func TestRemoteSecondPressDetonates(t *testing.T) {
	cfg := testConfig()
	cfg.BombFuseTicks = 100
	e := mustGame(t, 2, cfg)
	e.Tick()
	p := e.players["p1"]
	p.Remote = true
	if !e.PlaceBomb("p1") {
		t.Fatal("place bomb")
	}
	// Standing on the armed bomb with a full bomb slot: pressing again fires it.
	if !e.PlaceBomb("p1") {
		t.Fatal("second press should detonate")
	}
	if len(e.bombs) != 0 {
		t.Fatal("remote press did not detonate the bomb")
	}
}

func TestShieldAbsorbsOneHit(t *testing.T) {
	cfg := testConfig()
	cfg.BombFuseTicks = 1
	cfg.InvulnerableTicks = 0
	e := mustGame(t, 2, cfg)
	e.Tick()
	p := e.players["p1"]
	p.Shield = true
	p.X, p.Y = 5, 1
	e.bombs[Position{X: 5, Y: 1}] = &Bomb{
		ID:         "shield-test",
		Position:   Position{X: 5, Y: 1},
		Range:      0,
		DetonateAt: e.tick + 1,
	}
	baseLives := p.Lives
	for i := 0; i < 6; i++ {
		e.Tick()
	}
	if p.Shield {
		t.Fatal("shield should have been consumed")
	}
	if p.Lives != baseLives {
		t.Fatalf("shield did not absorb the hit: lives %d", p.Lives)
	}
	// The next blast hits normally.
	p.Shield = false
	p.InvulnUntil = 0
	e.explosions[Position{X: p.X, Y: p.Y}] = e.tick + 2
	e.Tick()
	if p.Lives != baseLives-1 {
		t.Fatalf("second hit should cost a life, lives = %d", p.Lives)
	}
}

func TestNewPowerUpsApply(t *testing.T) {
	e := mustGame(t, 2, testConfig())
	e.Tick()
	p := e.players["p1"]
	pos := Position{X: p.X, Y: p.Y}
	for _, kind := range []PowerUpKind{PowerUpKick, PowerUpRemote, PowerUpShield} {
		e.powerups[pos] = kind
		e.Tick()
	}
	if !p.Kick || !p.Remote || !p.Shield {
		t.Fatalf("new power-ups not applied: %+v", p)
	}
	snap := e.Snapshot().Players[0]
	if !snap.Kick || !snap.Remote || !snap.Shield {
		t.Fatalf("snapshot flags missing: %+v", snap)
	}
}

func TestBotMovesAndPlacesBombs(t *testing.T) {
	cfg := testConfig()
	cfg.BrickDensity = 0.5
	cfg.BombFuseTicks = 60 // a 3-tick fuse would be an unfair trap
	cfg.MatchTicks = 100000
	e, err := New([]PlayerSeed{
		{ID: "p1", Name: "Ada"},
		{ID: "p2", Name: "Bot Rex", IsBot: true},
	}, cfg, 7)
	if err != nil {
		t.Fatal(err)
	}
	bot := e.players["p2"]
	start := Position{X: bot.X, Y: bot.Y}
	placed := false
	moved := false
	for i := 0; i < 300; i++ {
		e.Tick()
		if len(e.bombs) > 0 {
			placed = true
		}
		if bot.X != start.X || bot.Y != start.Y {
			moved = true
		}
	}
	if !moved {
		t.Fatal("bot never moved")
	}
	if !placed {
		t.Fatal("bot never placed a bomb")
	}
}

func TestBotDoesNotForfeit(t *testing.T) {
	cfg := testConfig()
	e, err := New([]PlayerSeed{
		{ID: "p1", Name: "Ada"},
		{ID: "p2", Name: "Bot Rex", IsBot: true},
	}, cfg, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !e.players["p2"].IsBot {
		t.Fatal("bot flag missing on player")
	}
	snap := e.Snapshot()
	if !snap.Players[1].IsBot {
		t.Fatal("bot flag missing in snapshot")
	}
}

func TestBotRoundReachesAWinner(t *testing.T) {
	cfg := testConfig()
	cfg.Lives = 2
	cfg.BrickDensity = 0.4
	cfg.BombFuseTicks = 60
	cfg.MatchTicks = 200
	cfg.SuddenBombEvery = 5
	cfg.SuddenFuseTicks = 6
	e, err := New([]PlayerSeed{
		{ID: "p1", Name: "Ada"},
		{ID: "p2", Name: "Bot Nova", IsBot: true},
		{ID: "p3", Name: "Bot Rex", IsBot: true},
	}, cfg, 11)
	if err != nil {
		t.Fatal(err)
	}
	// The human never moves: the bots and the sudden-death bomb rain must still
	// end the round instead of stalling forever.
	for i := 0; i < 8000 && !e.Finished(); i++ {
		e.Tick()
	}
	if !e.Finished() {
		t.Fatalf("round did not finish after %d ticks", e.tick)
	}
	if name, draw := e.Winner(); draw {
		t.Fatal("expected a winner, got a draw")
	} else if name == "" {
		t.Fatal("winner name is empty")
	}
}
