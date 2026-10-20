package game

import "sort"

// botGoalKind describes what a bot currently wants to reach. Plans are kept
// across think steps so movement stays deliberate instead of jittering between
// equally good neighbours.
type botGoalKind uint8

const (
	botGoalPowerUp botGoalKind = iota
	botGoalBrick
	botGoalEnemy
)

// botBrain is the persistent planning state of one bot. The plan survives
// between think steps and is only discarded when it becomes invalid.
type botBrain struct {
	nextThink int

	hasPlan bool
	goal    botGoalKind
	target  Position
	path    []Position

	lastPos  Position
	lastDir  Direction
	lastMove int
}

const (
	// botThinkEvery throttles the search. Two ticks is fast enough to react to
	// a placed bomb and cheap enough for three bots at 20 Hz.
	botThinkEvery = 2
	// botFleeWindow is how far ahead a bot looks at coming blasts before it
	// starts running.
	botFleeWindow = 45
	// botSafetyMargin is the extra time a bot wants between arriving on a tile
	// and the tile catching fire.
	botSafetyMargin = 2
	// botSettleWindow keeps a bot from parking on a tile that is about to be
	// covered by a blast.
	botSettleWindow = 20
)

var botDirs = [...]Direction{DirUp, DirDown, DirLeft, DirRight}

// botSearch is one breadth-first sweep over the board.
type botSearch struct {
	arrival map[Position]int
	first   map[Position]Direction
	prev    map[Position]Position
}

// updateBots lets every bot decide its next move before human movement is
// processed for this tick.
func (e *Engine) updateBots() {
	if len(e.bots) == 0 || !e.started || e.finished {
		return
	}
	for _, id := range e.order {
		brain := e.bots[id]
		if brain == nil {
			continue
		}
		p := e.players[id]
		if p == nil || !p.Alive || p.Eliminated {
			continue
		}
		e.trackMove(brain, p)
		if e.tick < brain.nextThink {
			continue
		}
		e.thinkBot(brain, p)
		brain.nextThink = e.tick + botThinkEvery
	}
}

// trackMove records the last step so goal selection can avoid immediately
// walking back the way the bot came.
func (e *Engine) trackMove(brain *botBrain, p *Player) {
	pos := Position{X: p.X, Y: p.Y}
	if pos == brain.lastPos {
		return
	}
	if d := directionBetween(brain.lastPos, pos); d != DirNone {
		brain.lastDir = d
		brain.lastMove = e.tick
	}
	brain.lastPos = pos
}

// thinkBot is the decision ladder: survive, use the remote, strike, then
// advance the current plan or build a new one.
func (e *Engine) thinkBot(brain *botBrain, p *Player) {
	danger := e.dangerMap()
	here := Position{X: p.X, Y: p.Y}

	if e.botInDanger(here, danger) {
		brain.hasPlan = false
		brain.path = nil
		if dir, ok := e.botFlee(p, danger); ok {
			p.Desired = dir
		} else {
			p.Desired = DirNone
		}
		return
	}

	if e.botRemoteShot(p) {
		brain.hasPlan = false
		brain.path = nil
		p.Desired = DirNone
		return
	}

	if e.botPlaceBomb(p) {
		brain.hasPlan = false
		brain.path = nil
		if dir, ok := e.botFlee(p, e.dangerMap()); ok {
			p.Desired = dir
		} else {
			p.Desired = DirNone
		}
		return
	}

	if dir, ok := e.botFollowPlan(p, brain, danger); ok {
		p.Desired = dir
		return
	}
	brain.hasPlan = false
	brain.path = nil
	p.Desired = DirNone
}

func (e *Engine) botInDanger(pos Position, danger map[Position]int) bool {
	dt, ok := danger[pos]
	return ok && dt <= e.tick+botFleeWindow
}

// dangerMap computes the earliest tick at which every cell catches fire. Bomb
// chains are resolved by relaxing the detonation times a few times, which is
// enough for the handful of bombs that are on the board at once.
func (e *Engine) dangerMap() map[Position]int {
	det := make(map[Position]int, len(e.bombs))
	for pos, b := range e.bombs {
		det[pos] = b.DetonateAt
	}
	for pass := 0; pass < 4; pass++ {
		changed := false
		for pos, b := range e.bombs {
			t := det[pos]
			for _, cell := range e.blastCells(pos, b.Range) {
				if other, ok := det[cell]; ok && other > t {
					det[cell] = t
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}

	danger := make(map[Position]int, len(e.explosions)+len(det)*6)
	for pos, b := range e.bombs {
		t := det[pos]
		for _, cell := range e.blastCells(pos, b.Range) {
			if old, ok := danger[cell]; !ok || t < old {
				danger[cell] = t
			}
		}
	}
	for pos := range e.explosions {
		danger[pos] = e.tick
	}
	return danger
}

// searchFrom walks the board from the bot's tile. The first step is delayed
// until the bot's movement cooldown has expired, so arrival times match the
// simulation instead of being optimistic.
func (e *Engine) searchFrom(p *Player, danger map[Position]int, strict bool) botSearch {
	start := Position{X: p.X, Y: p.Y}
	cost := max(1, e.moveCooldown(p))
	startAt := e.tick
	if p.NextMove > startAt {
		startAt = p.NextMove
	}
	search := botSearch{
		arrival: map[Position]int{start: startAt},
		first:   make(map[Position]Direction, 64),
		prev:    make(map[Position]Position, 64),
	}
	queue := []Position{start}
	for head := 0; head < len(queue); head++ {
		pos := queue[head]
		at := search.arrival[pos]
		for _, d := range botDirs {
			dx, dy := d.Delta()
			next := Position{X: pos.X + dx, Y: pos.Y + dy}
			if _, seen := search.arrival[next]; seen {
				continue
			}
			if !e.brd.walkable(next) {
				continue
			}
			if _, bomb := e.bombs[next]; bomb {
				continue
			}
			arrive := at + cost
			if dt, ok := danger[next]; ok {
				// Strict searches (routes to goals) never enter a blast zone.
				// Escape searches only reject cells that are already burning on
				// arrival, because sometimes the only way out runs past a bomb.
				if strict || arrive+botSafetyMargin > dt {
					continue
				}
			}
			search.arrival[next] = arrive
			search.prev[next] = pos
			if pos == start {
				search.first[next] = d
			} else {
				search.first[next] = search.first[pos]
			}
			queue = append(queue, next)
		}
	}
	return search
}

// botFlee returns the step towards the reachable tile with the largest lead
// over the coming blast.
func (e *Engine) botFlee(p *Player, danger map[Position]int) (Direction, bool) {
	start := Position{X: p.X, Y: p.Y}
	search := e.searchFrom(p, danger, false)

	type escape struct {
		dir     Direction
		lead    int
		arrival int
		pos     Position
	}
	options := make([]escape, 0, len(search.arrival))
	for pos, at := range search.arrival {
		if pos == start {
			continue
		}
		lead := 100000
		if dt, ok := danger[pos]; ok {
			if at+botSafetyMargin > dt {
				continue
			}
			lead = dt - at
		}
		options = append(options, escape{dir: search.first[pos], lead: lead, arrival: at, pos: pos})
	}
	if len(options) == 0 {
		return DirNone, false
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].lead != options[j].lead {
			return options[i].lead > options[j].lead
		}
		if options[i].arrival != options[j].arrival {
			return options[i].arrival < options[j].arrival
		}
		return lessPos(options[i].pos, options[j].pos)
	})
	return options[0].dir, true
}

// botFollowPlan keeps the current plan while it remains safe and useful, and
// otherwise builds a fresh one.
func (e *Engine) botFollowPlan(p *Player, brain *botBrain, danger map[Position]int) (Direction, bool) {
	e.syncPath(p, brain)
	if brain.hasPlan && e.planValid(p, brain, danger) {
		return e.pathDirection(p, brain), true
	}
	if !e.buildPlan(p, brain, danger) {
		return DirNone, false
	}
	return e.pathDirection(p, brain), true
}

// syncPath drops the tiles the bot has already reached.
func (e *Engine) syncPath(p *Player, brain *botBrain) {
	for len(brain.path) > 0 && brain.path[0] == (Position{X: p.X, Y: p.Y}) {
		brain.path = brain.path[1:]
	}
}

func (e *Engine) pathDirection(p *Player, brain *botBrain) Direction {
	if len(brain.path) == 0 {
		return DirNone
	}
	next := brain.path[0]
	dx, dy := next.X-p.X, next.Y-p.Y
	for _, d := range botDirs {
		ddx, ddy := d.Delta()
		if ddx == dx && ddy == dy {
			return d
		}
	}
	return DirNone
}

// planValid checks that the goal still exists and that every remaining step is
// walkable, bomb-free and reached before the tile catches fire.
func (e *Engine) planValid(p *Player, brain *botBrain, danger map[Position]int) bool {
	if len(brain.path) == 0 || !e.goalValid(p, brain.goal, brain.target) {
		return false
	}
	cost := max(1, e.moveCooldown(p))
	at := e.tick
	if p.NextMove > at {
		at = p.NextMove
	}
	pos := Position{X: p.X, Y: p.Y}
	for _, cell := range brain.path {
		dx, dy := cell.X-pos.X, cell.Y-pos.Y
		if dx*dx+dy*dy != 1 {
			return false
		}
		if !e.brd.walkable(cell) {
			return false
		}
		if _, bomb := e.bombs[cell]; bomb {
			return false
		}
		at += cost
		if dt, ok := danger[cell]; ok && at+botSafetyMargin > dt {
			return false
		}
		pos = cell
	}
	return true
}

func (e *Engine) goalValid(p *Player, kind botGoalKind, target Position) bool {
	switch kind {
	case botGoalPowerUp:
		_, ok := e.powerups[target]
		return ok
	case botGoalBrick:
		return e.brickNear(target)
	case botGoalEnemy:
		return e.enemyNear(p.ID, target, 3)
	default:
		return false
	}
}

// buildPlan searches the board, scores every reachable goal and stores the
// shortest path to the best one.
func (e *Engine) buildPlan(p *Player, brain *botBrain, danger map[Position]int) bool {
	start := Position{X: p.X, Y: p.Y}
	search := e.searchFrom(p, danger, true)

	kind, target, ok := e.chooseGoal(p, brain, search, danger)
	if !ok {
		return false
	}
	path := pathFrom(search.prev, start, target)
	if len(path) == 0 {
		return false
	}
	brain.goal = kind
	brain.target = target
	brain.path = path
	brain.hasPlan = true
	return true
}

// chooseGoal scores reachable tiles. Power-ups beat attacking, attacking beats
// digging. A bot without any of those simply waits instead of pacing around.
func (e *Engine) chooseGoal(p *Player, brain *botBrain, search botSearch, danger map[Position]int) (botGoalKind, Position, bool) {
	start := Position{X: p.X, Y: p.Y}
	kind := botGoalBrick
	var best Position
	bestScore := 0
	found := false
	recentReverse := e.tick-brain.lastMove <= 12
	// A bomb on the board blocks the next one. While the bot waits for it to
	// explode, only collecting a power-up is useful - walking to bricks it
	// cannot blow up just looks restless.
	canBomb := e.activeBombs(p.ID) < p.MaxBombs
	for cell, at := range search.arrival {
		if cell == start {
			continue
		}
		// Do not stop on a tile that is about to be covered by a blast.
		if dt, ok := danger[cell]; ok && at+botSettleWindow > dt {
			continue
		}
		k := botGoalBrick
		score := 0
		switch {
		case e.powerupAt(cell):
			k, score = botGoalPowerUp, at
		case !canBomb:
			continue
		case e.enemyNear(p.ID, cell, 2):
			k, score = botGoalEnemy, at+6
		case e.brickNear(cell):
			k, score = botGoalBrick, at+12
		default:
			continue
		}
		// Never pace back and forth between two neighbouring tiles.
		if recentReverse && search.first[cell] == oppositeDirection(brain.lastDir) {
			score += 30
		}
		if !found || score < bestScore || (score == bestScore && lessPos(cell, best)) {
			kind, best, bestScore, found = k, cell, score, true
		}
	}
	return kind, best, found
}

// pathFrom reconstructs the steps from start to goal using the BFS parents.
func pathFrom(prev map[Position]Position, start, goal Position) []Position {
	reversed := make([]Position, 0, 16)
	for cur := goal; cur != start; {
		reversed = append(reversed, cur)
		parent, ok := prev[cur]
		if !ok || len(reversed) > 400 {
			return nil
		}
		cur = parent
	}
	path := make([]Position, len(reversed))
	for i := range reversed {
		path[len(reversed)-1-i] = reversed[i]
	}
	return path
}

// botPlaceBomb places a bomb when it is useful and provably escapable.
func (e *Engine) botPlaceBomb(p *Player) bool {
	if e.activeBombs(p.ID) >= p.MaxBombs {
		return false
	}
	pos := Position{X: p.X, Y: p.Y}
	if _, ok := e.bombs[pos]; ok {
		return false
	}
	if !e.botWantsBomb(p, pos) {
		return false
	}

	e.bombs[pos] = &Bomb{
		ID:         "bot-probe",
		Position:   pos,
		OwnerID:    p.ID,
		Range:      p.BombRange,
		DetonateAt: e.tick + e.cfg.BombFuseTicks,
	}

	after := e.dangerMap()
	search := e.searchFrom(p, after, false)
	safe := false
	for cell, at := range search.arrival {
		if cell == pos {
			continue
		}
		if dt, ok := after[cell]; !ok || at+botSafetyMargin < dt {
			safe = true
			break
		}
	}
	// Remove the probe before arming the real bomb so PlaceBomb sees a free
	// tile and uses a fresh ID.
	delete(e.bombs, pos)
	if !safe {
		return false
	}
	return e.PlaceBomb(p.ID)
}

// botWantsBomb reports whether a bomb under the bot is worth its fuse: either a
// brick is in reach or an opponent stands inside the blast pattern.
func (e *Engine) botWantsBomb(p *Player, pos Position) bool {
	for _, d := range botDirs {
		dx, dy := d.Delta()
		if e.brd.at(Position{X: pos.X + dx, Y: pos.Y + dy}) == TileBrick {
			return true
		}
	}
	for _, cell := range e.blastCells(pos, p.BombRange) {
		for _, other := range e.players {
			if other.ID == p.ID || !other.Alive || other.Eliminated {
				continue
			}
			if other.X == cell.X && other.Y == cell.Y {
				return true
			}
		}
	}
	return false
}

// botRemoteShot fires the bot's own bombs when an opponent is inside the blast
// and the bot itself is not.
func (e *Engine) botRemoteShot(p *Player) bool {
	if !p.Remote || e.activeBombs(p.ID) == 0 {
		return false
	}
	pos := Position{X: p.X, Y: p.Y}
	own := make([]Position, 0, len(e.bombs))
	for bpos, bomb := range e.bombs {
		if bomb.OwnerID == p.ID {
			own = append(own, bpos)
		}
	}
	sortPositions(own)
	for _, bpos := range own {
		bomb := e.bombs[bpos]
		blast := e.blastCells(bpos, bomb.Range)
		if positionsContain(blast, pos) {
			continue // never detonate a bomb the bot is standing in
		}
		if e.botBlastHitsEnemy(p, blast) {
			return e.Detonate(p.ID)
		}
	}
	return false
}

func (e *Engine) botBlastHitsEnemy(p *Player, blast []Position) bool {
	for _, other := range e.order {
		enemy := e.players[other]
		if enemy == nil || enemy.ID == p.ID || !enemy.Alive || enemy.Eliminated {
			continue
		}
		if positionsContain(blast, Position{X: enemy.X, Y: enemy.Y}) {
			return true
		}
	}
	return false
}

func positionsContain(cells []Position, target Position) bool {
	for _, cell := range cells {
		if cell == target {
			return true
		}
	}
	return false
}

func (e *Engine) powerupAt(pos Position) bool {
	_, ok := e.powerups[pos]
	return ok
}

// enemyNear reports whether an opposing player is within radius tiles.
func (e *Engine) enemyNear(self string, pos Position, radius int) bool {
	return e.enemyDistance(self, pos) <= radius
}

// enemyDistance is the Manhattan distance to the nearest living opponent. The
// bot itself is skipped; without opponents it returns a large value so patrols
// stay local.
func (e *Engine) enemyDistance(self string, pos Position) int {
	best := 1 << 20
	for _, other := range e.players {
		if other.ID == self || !other.Alive || other.Eliminated {
			continue
		}
		d := abs(other.X-pos.X) + abs(other.Y-pos.Y)
		if d < best {
			best = d
		}
	}
	return best
}

func (e *Engine) brickNear(pos Position) bool {
	for _, d := range botDirs {
		dx, dy := d.Delta()
		if e.brd.at(Position{X: pos.X + dx, Y: pos.Y + dy}) == TileBrick {
			return true
		}
	}
	return false
}

func directionBetween(from, to Position) Direction {
	dx, dy := to.X-from.X, to.Y-from.Y
	for _, d := range botDirs {
		ddx, ddy := d.Delta()
		if ddx == dx && ddy == dy {
			return d
		}
	}
	return DirNone
}

func oppositeDirection(d Direction) Direction {
	switch d {
	case DirUp:
		return DirDown
	case DirDown:
		return DirUp
	case DirLeft:
		return DirRight
	case DirRight:
		return DirLeft
	default:
		return DirNone
	}
}

func lessPos(a, b Position) bool {
	if a.Y == b.Y {
		return a.X < b.X
	}
	return a.Y < b.Y
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
