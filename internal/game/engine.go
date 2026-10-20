package game

import (
	"fmt"
	"math/rand"
	"sort"
)

// Player is the mutable simulation state of one participant.
type Player struct {
	ID         string
	Name       string
	Color      string
	X, Y       int
	Spawn      Position
	Lives      int
	MaxBombs   int
	BombRange  int
	SpeedLevel int

	// Power-up flags. Kick pushes bombs, Remote detonates them on demand and
	// Shield absorbs exactly one hit.
	Kick   bool
	Remote bool
	Shield bool

	IsBot bool

	Desired  Direction
	NextMove int
	Alive    bool

	Eliminated  bool
	RespawnAt   int
	InvulnUntil int
}

// Bomb is one armed bomb on the board.
type Bomb struct {
	ID         string
	Position   Position
	OwnerID    string
	Range      int
	DetonateAt int

	// KickDir and KickAt implement a sliding (kicked) bomb. KickDir is
	// DirNone for a resting bomb.
	KickDir Direction
	KickAt  int
}

// Engine is the authoritative Bomber Grid simulation. It is deliberately not
// internally locked: a Room serializes all calls from its game loop and input
// handlers, which keeps the engine deterministic and easy to test.
type Engine struct {
	cfg Config
	brd board
	rng *rand.Rand

	order      []string
	players    map[string]*Player
	bombs      map[Position]*Bomb
	explosions map[Position]int // tick at which the blast disappears
	powerups   map[Position]PowerUpKind

	bombSeq int
	bots    map[string]*botBrain

	sudden      bool
	suddenSince int
	lastDrop    int

	tick      int
	countdown int
	started   bool
	finished  bool
	winnerID  string
	draw      bool
}

// New creates a round with generated terrain and four classic spawn corners.
func New(seeds []PlayerSeed, cfg Config, seed int64) (*Engine, error) {
	cfg = cfg.normalized()
	if err := cfg.validate(seeds); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(seed))
	spawns := spawnPositions(cfg)
	b := newBoard(cfg, rng, spawns[:len(seeds)])

	e := &Engine{
		cfg:        cfg,
		brd:        b,
		rng:        rng,
		order:      make([]string, 0, len(seeds)),
		players:    make(map[string]*Player, len(seeds)),
		bombs:      make(map[Position]*Bomb),
		explosions: make(map[Position]int),
		powerups:   make(map[Position]PowerUpKind),
		bots:       make(map[string]*botBrain),
		countdown:  cfg.CountdownTicks,
	}
	for i, seed := range seeds {
		color := seed.Color
		if color == "" {
			color = "#ffb347"
		}
		sp := spawns[i]
		p := &Player{
			ID:         seed.ID,
			Name:       seed.Name,
			Color:      color,
			X:          sp.X,
			Y:          sp.Y,
			Spawn:      sp,
			Lives:      cfg.Lives,
			MaxBombs:   cfg.MaxBombs,
			BombRange:  cfg.BombRange,
			SpeedLevel: 1,
			Alive:      true,
			IsBot:      seed.IsBot,
		}
		e.players[p.ID] = p
		e.order = append(e.order, p.ID)
		if p.IsBot {
			e.bots[p.ID] = &botBrain{}
		}
	}
	return e, nil
}

// Forfeit eliminates a player who is no longer reachable (for example after a
// reconnect grace period). The remaining players continue normally.
func (e *Engine) Forfeit(id string) bool {
	p := e.players[id]
	if p == nil || p.Eliminated || e.finished {
		return false
	}
	p.Lives = 0
	p.Alive = false
	p.Eliminated = true
	p.Desired = DirNone
	p.RespawnAt = 0
	p.InvulnUntil = 0
	e.checkWinner()
	return true
}

// Finished reports whether a winner has been decided.
func (e *Engine) Finished() bool { return e.finished }

// Winner returns the winning player's display name, or an empty string on a
// draw. It is only meaningful when Finished is true.
func (e *Engine) Winner() (name string, draw bool) {
	if !e.finished {
		return "", false
	}
	if e.draw {
		return "", true
	}
	if p := e.players[e.winnerID]; p != nil {
		return p.Name, false
	}
	return "", false
}

// SetDirection updates a player's held movement direction and performs an
// immediate step when the movement cooldown has elapsed. This makes a fresh
// key press feel responsive even between fixed ticks.
func (e *Engine) SetDirection(id string, d Direction) bool {
	p := e.players[id]
	if p == nil || p.Eliminated || e.finished {
		return false
	}
	p.Desired = d
	if d == DirNone || !p.Alive || !e.started || e.countdown > 0 {
		return true
	}
	if e.tick >= p.NextMove {
		e.step(p)
	}
	return true
}

// PlaceBomb attempts to arm a bomb under the player. With the remote power-up
// a second press detonates the player's primed bombs instead, which is the
// classic handheld behaviour.
func (e *Engine) PlaceBomb(id string) bool {
	p := e.players[id]
	if p == nil || p.Eliminated || !p.Alive || !e.started || e.countdown > 0 || e.finished {
		return false
	}
	pos := Position{X: p.X, Y: p.Y}
	if _, exists := e.bombs[pos]; exists || e.activeBombs(id) >= p.MaxBombs {
		if p.Remote && e.activeBombs(id) > 0 {
			return e.Detonate(id)
		}
		return false
	}
	e.bombSeq++
	e.bombs[pos] = &Bomb{
		ID:         fmt.Sprintf("bomb-%d", e.bombSeq),
		Position:   pos,
		OwnerID:    id,
		Range:      p.BombRange,
		DetonateAt: e.tick + e.cfg.BombFuseTicks,
	}
	return true
}

// Detonate triggers every primed bomb of a player with the remote power-up.
func (e *Engine) Detonate(id string) bool {
	p := e.players[id]
	if p == nil || p.Eliminated || !p.Alive || !p.Remote || !e.started || e.finished {
		return false
	}
	positions := make([]Position, 0, len(e.bombs))
	for pos, b := range e.bombs {
		if b.OwnerID == id {
			positions = append(positions, pos)
		}
	}
	if len(positions) == 0 {
		return false
	}
	sortPositions(positions)
	seen := make(map[Position]bool, len(positions))
	for _, pos := range positions {
		e.detonate(pos, seen)
	}
	return true
}

// Tick advances the simulation by one 50 ms step.
func (e *Engine) Tick() {
	if e.finished {
		return
	}
	e.tick++
	if e.countdown > 0 {
		e.countdown--
		if e.countdown == 0 {
			e.started = true
		}
		return
	}
	e.started = true

	e.maybeSuddenDeath()
	e.updateBots()
	e.moveBombs()
	e.movePlayers()
	e.detonateDueBombs()
	e.suddenDeathTick()
	e.damagePlayers()
	e.respawnPlayers()
	e.collectPowerUps()
	e.expireExplosions()
	e.checkWinner()
}

func (e *Engine) movePlayers() {
	for _, id := range e.order {
		p := e.players[id]
		if !p.Alive || p.Eliminated || p.Desired == DirNone || e.tick < p.NextMove {
			continue
		}
		e.step(p)
	}
}

func (e *Engine) step(p *Player) {
	dx, dy := p.Desired.Delta()
	target := Position{X: p.X + dx, Y: p.Y + dy}
	if e.canEnter(target) {
		p.X, p.Y = target.X, target.Y
		p.NextMove = e.tick + e.moveCooldown(p)
		return
	}
	// A player with the kick power-up shoves the bomb out of the way and
	// follows it.
	if p.Kick {
		if bomb, ok := e.bombs[target]; ok && e.kickBomb(bomb, p.Desired) && e.canEnter(target) {
			p.X, p.Y = target.X, target.Y
			p.NextMove = e.tick + e.moveCooldown(p)
			return
		}
	}
	// Retry on the next tick. This prevents a key held against a wall from
	// accumulating a burst of movement once the wall disappears.
	p.NextMove = e.tick + 1
}

// kickBomb slides a bomb one tile into dir. It reports whether the bomb moved.
func (e *Engine) kickBomb(b *Bomb, dir Direction) bool {
	dx, dy := dir.Delta()
	next := Position{X: b.Position.X + dx, Y: b.Position.Y + dy}
	if !e.bombCanSlide(next) {
		return false
	}
	delete(e.bombs, b.Position)
	b.Position = next
	e.bombs[next] = b
	b.KickDir = dir
	b.KickAt = e.tick + e.cfg.KickEvery
	return true
}

func (e *Engine) bombCanSlide(pos Position) bool {
	if !e.brd.walkable(pos) {
		return false
	}
	_, bomb := e.bombs[pos]
	return !bomb
}

// moveBombs advances already kicked bombs until they hit something.
func (e *Engine) moveBombs() {
	if len(e.bombs) == 0 {
		return
	}
	moving := make([]*Bomb, 0, len(e.bombs))
	for _, b := range e.bombs {
		if b.KickDir != DirNone && e.tick >= b.KickAt {
			moving = append(moving, b)
		}
	}
	if len(moving) == 0 {
		return
	}
	sort.Slice(moving, func(i, j int) bool {
		if moving[i].Position.Y == moving[j].Position.Y {
			return moving[i].Position.X < moving[j].Position.X
		}
		return moving[i].Position.Y < moving[j].Position.Y
	})
	for _, b := range moving {
		if !e.kickBomb(b, b.KickDir) {
			b.KickDir = DirNone
		}
	}
}

func (e *Engine) canEnter(p Position) bool {
	if !e.brd.walkable(p) {
		return false
	}
	_, bomb := e.bombs[p]
	return !bomb
}

// sortPositions orders positions deterministically (top-left to bottom-right).
func sortPositions(positions []Position) {
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Y == positions[j].Y {
			return positions[i].X < positions[j].X
		}
		return positions[i].Y < positions[j].Y
	})
}

func (e *Engine) moveCooldown(p *Player) int {
	if p.SpeedLevel > 1 {
		return max(1, e.cfg.SpeedEvery)
	}
	return max(1, e.cfg.MoveEvery)
}

func (e *Engine) activeBombs(owner string) int {
	n := 0
	for _, b := range e.bombs {
		if b.OwnerID == owner {
			n++
		}
	}
	return n
}

func (e *Engine) detonateDueBombs() {
	due := make([]Position, 0)
	for pos, b := range e.bombs {
		if e.tick >= b.DetonateAt {
			due = append(due, pos)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].Y == due[j].Y {
			return due[i].X < due[j].X
		}
		return due[i].Y < due[j].Y
	})
	for _, pos := range due {
		e.detonate(pos, make(map[Position]bool))
	}
}

// detonate removes one bomb and paints a cross-shaped blast. Chained bombs
// detonate recursively; the seen set protects against cycles.
func (e *Engine) detonate(pos Position, seen map[Position]bool) {
	if seen[pos] {
		return
	}
	seen[pos] = true
	bomb, ok := e.bombs[pos]
	if !ok {
		return
	}
	delete(e.bombs, pos)
	e.explosions[pos] = e.tick + e.cfg.ExplosionTicks

	for _, d := range []Direction{DirUp, DirDown, DirLeft, DirRight} {
		dx, dy := d.Delta()
		for r := 1; r <= bomb.Range; r++ {
			p := Position{X: pos.X + dx*r, Y: pos.Y + dy*r}
			switch e.brd.at(p) {
			case TileSolid:
				goto nextDirection
			case TileBrick:
				e.explosions[p] = e.tick + e.cfg.ExplosionTicks
				e.brd.set(p, TileFloor)
				e.maybeSpawnPowerUp(p)
				goto nextDirection
			}

			e.explosions[p] = e.tick + e.cfg.ExplosionTicks
			if _, chained := e.bombs[p]; chained {
				e.detonate(p, seen)
				goto nextDirection
			}
		}
	nextDirection:
	}
}

func (e *Engine) maybeSpawnPowerUp(pos Position) {
	if e.rng.Float64() >= e.cfg.PowerUpChance {
		return
	}
	if _, exists := e.powerups[pos]; exists {
		return
	}
	roll := e.rng.Intn(100)
	switch {
	case roll < 24:
		e.powerups[pos] = PowerUpBomb
	case roll < 46:
		e.powerups[pos] = PowerUpRange
	case roll < 60:
		e.powerups[pos] = PowerUpSpeed
	case roll < 76:
		e.powerups[pos] = PowerUpKick
	case roll < 88:
		e.powerups[pos] = PowerUpRemote
	default:
		e.powerups[pos] = PowerUpShield
	}
}

// blastCells returns every board cell a bomb at pos would cover, including the
// center and the first brick in each direction. It mirrors the propagation
// rules of detonate and is used by the bots and by the sudden-death logic.
func (e *Engine) blastCells(pos Position, radius int) []Position {
	cells := []Position{pos}
	for _, d := range []Direction{DirUp, DirDown, DirLeft, DirRight} {
		dx, dy := d.Delta()
		for r := 1; r <= radius; r++ {
			p := Position{X: pos.X + dx*r, Y: pos.Y + dy*r}
			t := e.brd.at(p)
			if t == TileSolid {
				break
			}
			cells = append(cells, p)
			if t == TileBrick {
				break
			}
		}
	}
	return cells
}

// maybeSuddenDeath starts the endgame once the match clock runs out: the outer
// wall ring crumbles so nobody can hide forever, and bomb rain begins.
func (e *Engine) maybeSuddenDeath() {
	if e.sudden || e.tick < e.cfg.MatchTicks {
		return
	}
	e.sudden = true
	e.suddenSince = e.tick
	e.lastDrop = e.tick
	for x := 0; x < e.cfg.Width; x++ {
		e.brd.set(Position{X: x, Y: 0}, TileFloor)
		e.brd.set(Position{X: x, Y: e.cfg.Height - 1}, TileFloor)
	}
	for y := 0; y < e.cfg.Height; y++ {
		e.brd.set(Position{X: 0, Y: y}, TileFloor)
		e.brd.set(Position{X: e.cfg.Width - 1, Y: y}, TileFloor)
	}
}

// suddenDeathTick drops bombs onto the arena at an accelerating rate. Bombs
// spawn on free tiles only, so the escalation is always visible and fair.
func (e *Engine) suddenDeathTick() {
	if !e.sudden || e.finished {
		return
	}
	escalation := (e.tick - e.suddenSince) / (10 * TickRate)
	interval := e.cfg.SuddenBombEvery - escalation*3
	if interval < 4 {
		interval = 4
	}
	if e.tick-e.lastDrop < interval {
		return
	}
	e.dropRainBomb()
	e.lastDrop = e.tick
}

// dropRainBomb arms one bomb on a random free tile.
func (e *Engine) dropRainBomb() {
	candidates := make([]Position, 0, e.cfg.Width*e.cfg.Height)
	for y := 0; y < e.cfg.Height; y++ {
		for x := 0; x < e.cfg.Width; x++ {
			pos := Position{X: x, Y: y}
			if !e.brd.walkable(pos) {
				continue
			}
			if _, bomb := e.bombs[pos]; bomb {
				continue
			}
			if _, boom := e.explosions[pos]; boom {
				continue
			}
			candidates = append(candidates, pos)
		}
	}
	if len(candidates) == 0 {
		return
	}
	pos := candidates[e.rng.Intn(len(candidates))]
	e.bombSeq++
	e.bombs[pos] = &Bomb{
		ID:         fmt.Sprintf("bomb-%d", e.bombSeq),
		Position:   pos,
		Range:      2,
		DetonateAt: e.tick + e.cfg.SuddenFuseTicks,
	}
}

func (e *Engine) damagePlayers() {
	for _, id := range e.order {
		p := e.players[id]
		if !p.Alive || p.Eliminated {
			continue
		}
		if e.tick < p.InvulnUntil {
			continue
		}
		if _, hit := e.explosions[Position{X: p.X, Y: p.Y}]; !hit {
			continue
		}
		if p.Shield {
			// Absorb exactly one hit and grant a short grace period so the
			// same blast cannot eat the shield and the life at once.
			p.Shield = false
			p.InvulnUntil = e.tick + 10
			continue
		}
		p.Lives--
		p.Alive = false
		p.Desired = DirNone
		p.NextMove = 0
		p.InvulnUntil = 0
		if p.Lives <= 0 {
			p.Eliminated = true
			p.RespawnAt = 0
		} else {
			p.RespawnAt = e.tick + e.cfg.RespawnTicks
		}
	}
}

func (e *Engine) respawnPlayers() {
	for _, id := range e.order {
		p := e.players[id]
		if p.Eliminated || p.Alive || e.tick < p.RespawnAt {
			continue
		}
		pos := p.Spawn
		if !e.brd.walkable(pos) {
			p.RespawnAt = e.tick + 2
			continue
		}
		if _, bomb := e.bombs[pos]; bomb {
			p.RespawnAt = e.tick + 2
			continue
		}
		if _, boom := e.explosions[pos]; boom {
			p.RespawnAt = e.tick + 2
			continue
		}
		p.X, p.Y = pos.X, pos.Y
		p.Alive = true
		p.Desired = DirNone
		p.NextMove = e.tick
		p.InvulnUntil = e.tick + e.cfg.InvulnerableTicks
	}
}

func (e *Engine) collectPowerUps() {
	for _, id := range e.order {
		p := e.players[id]
		if !p.Alive || p.Eliminated {
			continue
		}
		pos := Position{X: p.X, Y: p.Y}
		kind, ok := e.powerups[pos]
		if !ok {
			continue
		}
		delete(e.powerups, pos)
		switch kind {
		case PowerUpRange:
			if p.BombRange < e.cfg.MaxRangeCap {
				p.BombRange++
			}
		case PowerUpBomb:
			if p.MaxBombs < e.cfg.MaxBombsCap {
				p.MaxBombs++
			}
		case PowerUpSpeed:
			p.SpeedLevel = 2
		case PowerUpKick:
			p.Kick = true
		case PowerUpRemote:
			p.Remote = true
		case PowerUpShield:
			p.Shield = true
		}
	}
}

func (e *Engine) expireExplosions() {
	for pos, until := range e.explosions {
		if e.tick >= until {
			delete(e.explosions, pos)
		}
	}
}

func (e *Engine) checkWinner() {
	if !e.started || e.finished {
		return
	}
	remaining := 0
	last := ""
	for _, id := range e.order {
		if !e.players[id].Eliminated {
			remaining++
			last = id
		}
	}
	if remaining > 1 {
		return
	}
	e.finished = true
	if remaining == 0 {
		e.draw = true
		return
	}
	e.winnerID = last
}

// Snapshot returns a deep copy of the visible state.
func (e *Engine) Snapshot() Snapshot {
	cells := make([][]Cell, e.cfg.Height)
	for y := 0; y < e.cfg.Height; y++ {
		row := make([]Cell, e.cfg.Width)
		for x := 0; x < e.cfg.Width; x++ {
			t := e.brd.tiles[y][x]
			class := t.String()
			boom := false
			if until, ok := e.explosions[Position{X: x, Y: y}]; ok && e.tick < until {
				class += " boom"
				boom = true
			}
			row[x] = Cell{
				Tile:    t,
				Class:   class,
				PowerUp: e.powerups[Position{X: x, Y: y}],
				Boom:    boom,
			}
		}
		cells[y] = row
	}

	bombs := make([]BombSnapshot, 0, len(e.bombs))
	for _, b := range e.bombs {
		bombs = append(bombs, BombSnapshot{
			ID:    b.ID,
			X:     b.Position.X,
			Y:     b.Position.Y,
			Range: b.Range,
		})
	}
	sort.Slice(bombs, func(i, j int) bool {
		if bombs[i].Y == bombs[j].Y {
			return bombs[i].X < bombs[j].X
		}
		return bombs[i].Y < bombs[j].Y
	})

	players := make([]PlayerSnapshot, 0, len(e.order))
	for _, id := range e.order {
		p := e.players[id]
		players = append(players, PlayerSnapshot{
			ID:           p.ID,
			Name:         p.Name,
			Color:        p.Color,
			X:            p.X,
			Y:            p.Y,
			Lives:        p.Lives,
			MaxBombs:     p.MaxBombs,
			BombRange:    p.BombRange,
			ActiveBombs:  e.activeBombs(id),
			SpeedLevel:   p.SpeedLevel,
			Alive:        p.Alive,
			Eliminated:   p.Eliminated,
			Respawning:   !p.Alive && !p.Eliminated,
			Invulnerable: p.Alive && e.tick < p.InvulnUntil,
			IsBot:        p.IsBot,
			Kick:         p.Kick,
			Remote:       p.Remote,
			Shield:       p.Shield,
		})
	}

	countdownSeconds := 0
	if e.countdown > 0 {
		countdownSeconds = (e.countdown + TickRate - 1) / TickRate
	}
	suddenSeconds := 0
	if !e.sudden && e.cfg.MatchTicks > e.tick {
		suddenSeconds = (e.cfg.MatchTicks - e.tick + TickRate - 1) / TickRate
	}
	return Snapshot{
		Width:            e.cfg.Width,
		Height:           e.cfg.Height,
		Tick:             e.tick,
		ElapsedSeconds:   e.tick / TickRate,
		Countdown:        e.countdown,
		CountdownSeconds: countdownSeconds,
		Started:          e.started,
		Finished:         e.finished,
		Draw:             e.draw,
		WinnerID:         e.winnerID,
		WinnerName:       e.winnerName(),
		SuddenDeath:      e.sudden,
		SuddenDeathIn:    suddenSeconds,
		MatchSeconds:     e.cfg.MatchTicks / TickRate,
		Cells:            cells,
		Bombs:            bombs,
		Players:          players,
	}
}

func (e *Engine) winnerName() string {
	if p := e.players[e.winnerID]; p != nil {
		return p.Name
	}
	return ""
}
