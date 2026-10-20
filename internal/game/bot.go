package game

import "sort"

// botBrain is the tiny amount of per-bot AI state. The behaviour itself is
// stateless: every think step derives a fresh plan from the board, which keeps
// bots deterministic and immune to stale plans.
type botBrain struct {
	nextThink int
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
)

var botDirs = [...]Direction{DirUp, DirDown, DirLeft, DirRight}

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
		if p == nil || !p.Alive || p.Eliminated || e.tick < brain.nextThink {
			continue
		}
		e.thinkBot(p)
		brain.nextThink = e.tick + botThinkEvery
	}
}

func (e *Engine) thinkBot(p *Player) {
	danger := e.dangerMap()
	here := Position{X: p.X, Y: p.Y}
	if e.botInDanger(here, danger) {
		if dir, ok := e.botFlee(p, danger); ok {
			p.Desired = dir
		} else {
			p.Desired = DirNone
		}
		return
	}
	if e.botPlaceBomb(p) {
		if dir, ok := e.botFlee(p, e.dangerMap()); ok {
			p.Desired = dir
		} else {
			p.Desired = DirNone
		}
		return
	}
	if dir, ok := e.botRoam(p, danger); ok {
		p.Desired = dir
		return
	}
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

// botBFS walks the board from the bot's tile and returns the absolute arrival
// tick plus the first step that leads to each reachable cell. Cells that would
// already be on fire when the bot arrives are not entered.
func (e *Engine) botBFS(p *Player, danger map[Position]int) (map[Position]int, map[Position]Direction) {
	start := Position{X: p.X, Y: p.Y}
	cost := max(1, e.moveCooldown(p))
	arrival := map[Position]int{start: e.tick}
	first := make(map[Position]Direction, 64)
	queue := []Position{start}
	for head := 0; head < len(queue); head++ {
		pos := queue[head]
		at := arrival[pos]
		for _, d := range botDirs {
			dx, dy := d.Delta()
			next := Position{X: pos.X + dx, Y: pos.Y + dy}
			if _, seen := arrival[next]; seen {
				continue
			}
			if !e.brd.walkable(next) {
				continue
			}
			if _, bomb := e.bombs[next]; bomb {
				continue
			}
			arrive := at + cost
			if dt, ok := danger[next]; ok && arrive+botSafetyMargin > dt {
				continue
			}
			arrival[next] = arrive
			if pos == start {
				first[next] = d
			} else {
				first[next] = first[pos]
			}
			queue = append(queue, next)
		}
	}
	return arrival, first
}

// botFlee returns the step towards the reachable tile with the largest lead
// over the coming blast.
func (e *Engine) botFlee(p *Player, danger map[Position]int) (Direction, bool) {
	start := Position{X: p.X, Y: p.Y}
	arrival, first := e.botBFS(p, danger)

	type escape struct {
		dir     Direction
		lead    int
		arrival int
		pos     Position
	}
	options := make([]escape, 0, len(arrival))
	for pos, at := range arrival {
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
		options = append(options, escape{dir: first[pos], lead: lead, arrival: at, pos: pos})
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
	arrival, _ := e.botBFS(p, after)
	safe := false
	for cell, at := range arrival {
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
// brick is in reach or an opponent stands in the blast pattern.
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

// botRoam walks towards the most attractive reachable goal: power-ups first,
// then tiles next to bricks.
func (e *Engine) botRoam(p *Player, danger map[Position]int) (Direction, bool) {
	start := Position{X: p.X, Y: p.Y}
	arrival, first := e.botBFS(p, danger)

	type goal struct {
		pos     Position
		score   int
		arrival int
	}
	goals := make([]goal, 0, len(arrival))
	for pos, at := range arrival {
		if pos == start {
			continue
		}
		if dt, ok := danger[pos]; ok && at+botSafetyMargin > dt {
			continue
		}
		score := at
		if _, ok := e.powerups[pos]; ok {
			score -= 24
		}
		if e.brickNear(pos) {
			score -= 8
		}
		goals = append(goals, goal{pos: pos, score: score, arrival: at})
	}
	if len(goals) == 0 {
		return DirNone, false
	}
	sort.Slice(goals, func(i, j int) bool {
		if goals[i].score != goals[j].score {
			return goals[i].score < goals[j].score
		}
		if goals[i].arrival != goals[j].arrival {
			return goals[i].arrival < goals[j].arrival
		}
		return lessPos(goals[i].pos, goals[j].pos)
	})
	return first[goals[0].pos], true
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

func lessPos(a, b Position) bool {
	if a.Y == b.Y {
		return a.X < b.X
	}
	return a.Y < b.Y
}
