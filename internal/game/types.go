// Package game contains the authoritative, renderer-agnostic Bomber Grid
// simulation. It has no knowledge of HTTP, WebSockets or HTML: callers feed it
// player input and call Tick at a fixed rate, then read snapshots for rendering.
package game

import (
	"errors"
	"fmt"
	"time"
)

// TickRate is the number of simulation steps per second. The room layer calls
// Tick at this fixed rate and throttles HTML rendering separately.
const TickRate = 20

// TickDuration is the wall-clock duration of one simulation tick.
const TickDuration = time.Second / TickRate

// Tile is the static terrain value of one board cell.
type Tile uint8

const (
	// TileFloor can be walked on.
	TileFloor Tile = iota
	// TileSolid is an indestructible wall.
	TileSolid
	// TileBrick is a destructible block.
	TileBrick
)

// String returns a short, stable name for tests and snapshots.
func (t Tile) String() string {
	switch t {
	case TileFloor:
		return "floor"
	case TileSolid:
		return "solid"
	case TileBrick:
		return "brick"
	default:
		return "unknown"
	}
}

// Direction is a cardinal movement direction.
type Direction int8

const (
	DirNone Direction = iota
	DirUp
	DirDown
	DirLeft
	DirRight
)

// Delta returns the board delta for a direction.
func (d Direction) Delta() (dx, dy int) {
	switch d {
	case DirUp:
		return 0, -1
	case DirDown:
		return 0, 1
	case DirLeft:
		return -1, 0
	case DirRight:
		return 1, 0
	default:
		return 0, 0
	}
}

// String returns the protocol name of the direction.
func (d Direction) String() string {
	switch d {
	case DirUp:
		return "up"
	case DirDown:
		return "down"
	case DirLeft:
		return "left"
	case DirRight:
		return "right"
	default:
		return "none"
	}
}

// ParseDirection parses a protocol direction name. An empty or unknown value
// is treated as "none" so stray key-up events can never move a player.
func ParseDirection(s string) Direction {
	switch s {
	case "up":
		return DirUp
	case "down":
		return DirDown
	case "left":
		return DirLeft
	case "right":
		return DirRight
	default:
		return DirNone
	}
}

// Position is an integer board coordinate.
type Position struct {
	X int
	Y int
}

// PowerUpKind identifies a collectible placed under a destroyed brick.
type PowerUpKind uint8

const (
	PowerUpNone PowerUpKind = iota
	PowerUpRange
	PowerUpBomb
	PowerUpSpeed
	PowerUpKick
	PowerUpRemote
	PowerUpShield
)

// String returns the protocol/CSS name of a power-up.
func (p PowerUpKind) String() string {
	switch p {
	case PowerUpRange:
		return "range"
	case PowerUpBomb:
		return "bomb"
	case PowerUpSpeed:
		return "speed"
	case PowerUpKick:
		return "kick"
	case PowerUpRemote:
		return "remote"
	case PowerUpShield:
		return "shield"
	default:
		return "none"
	}
}

// Label returns the human-readable German label used by the UI.
func (p PowerUpKind) Label() string {
	switch p {
	case PowerUpRange:
		return "Reichweite"
	case PowerUpBomb:
		return "Extrabombe"
	case PowerUpSpeed:
		return "Tempo"
	case PowerUpKick:
		return "Kick"
	case PowerUpRemote:
		return "Fernzünder"
	case PowerUpShield:
		return "Schild"
	default:
		return ""
	}
}

// Symbol returns a compact glyph used inside a board cell.
func (p PowerUpKind) Symbol() string {
	switch p {
	case PowerUpRange:
		return "↔"
	case PowerUpBomb:
		return "+"
	case PowerUpSpeed:
		return "»"
	case PowerUpKick:
		return "⊣"
	case PowerUpRemote:
		return "✹"
	case PowerUpShield:
		return "⛨"
	default:
		return ""
	}
}

// Config controls board generation and game feel. Zero values are replaced by
// DefaultConfig in New.
type Config struct {
	Width  int
	Height int

	Lives      int
	MaxBombs   int
	BombRange  int
	MoveEvery  int // ticks between steps at normal speed
	SpeedEvery int // ticks between steps after a speed power-up

	BrickDensity  float64
	PowerUpChance float64

	CountdownTicks    int
	BombFuseTicks     int
	ExplosionTicks    int
	RespawnTicks      int
	InvulnerableTicks int
	KickEvery         int // ticks a kicked bomb needs per tile

	// MatchTicks ends the stalling phase: when the round reaches this many
	// ticks without a winner, sudden death starts (border walls crumble and
	// bombs rain down). SuddenBombEvery is the interval between drops before
	// the escalation ramps up.
	MatchTicks      int
	SuddenBombEvery int
	SuddenFuseTicks int

	MaxBombsCap int
	MaxRangeCap int
}

// DefaultConfig returns the tuned arcade defaults for a 15x13 board at 20 Hz.
func DefaultConfig() Config {
	return Config{
		Width:             15,
		Height:            13,
		Lives:             3,
		MaxBombs:          1,
		BombRange:         2,
		MoveEvery:         3,
		SpeedEvery:        2,
		BrickDensity:      0.44,
		PowerUpChance:     0.30,
		CountdownTicks:    60,
		BombFuseTicks:     60,
		ExplosionTicks:    9,
		RespawnTicks:      30,
		InvulnerableTicks: 30,
		KickEvery:         4,
		MatchTicks:        120 * TickRate,
		SuddenBombEvery:   TickRate,
		SuddenFuseTicks:   30,
		MaxBombsCap:       5,
		MaxRangeCap:       8,
	}
}

func (c Config) normalized() Config {
	def := DefaultConfig()
	if c.Width <= 0 {
		c.Width = def.Width
	}
	if c.Height <= 0 {
		c.Height = def.Height
	}
	if c.Lives <= 0 {
		c.Lives = def.Lives
	}
	if c.MaxBombs <= 0 {
		c.MaxBombs = def.MaxBombs
	}
	if c.BombRange <= 0 {
		c.BombRange = def.BombRange
	}
	if c.MoveEvery <= 0 {
		c.MoveEvery = def.MoveEvery
	}
	if c.SpeedEvery <= 0 {
		c.SpeedEvery = def.SpeedEvery
	}
	if c.BrickDensity < 0 || c.BrickDensity > 0.85 {
		c.BrickDensity = def.BrickDensity
	}
	if c.PowerUpChance < 0 || c.PowerUpChance > 1 {
		c.PowerUpChance = def.PowerUpChance
	}
	if c.CountdownTicks < 0 {
		c.CountdownTicks = 0
	}
	if c.BombFuseTicks <= 0 {
		c.BombFuseTicks = def.BombFuseTicks
	}
	if c.ExplosionTicks <= 0 {
		c.ExplosionTicks = def.ExplosionTicks
	}
	if c.RespawnTicks <= 0 {
		c.RespawnTicks = def.RespawnTicks
	}
	if c.InvulnerableTicks < 0 {
		c.InvulnerableTicks = def.InvulnerableTicks
	}
	if c.KickEvery <= 0 {
		c.KickEvery = def.KickEvery
	}
	if c.MatchTicks <= 0 {
		c.MatchTicks = def.MatchTicks
	}
	if c.SuddenBombEvery <= 0 {
		c.SuddenBombEvery = def.SuddenBombEvery
	}
	if c.SuddenFuseTicks <= 0 {
		c.SuddenFuseTicks = def.SuddenFuseTicks
	}
	if c.MaxBombsCap < c.MaxBombs {
		c.MaxBombsCap = def.MaxBombsCap
	}
	if c.MaxRangeCap < c.BombRange {
		c.MaxRangeCap = def.MaxRangeCap
	}
	return c
}

// PlayerSeed describes one participant at the moment a round starts.
type PlayerSeed struct {
	ID    string
	Name  string
	Color string
	IsBot bool
}

// Snapshot is an immutable copy of the visible game state. It is safe to hand
// to a template renderer after the room lock has been released.
type Snapshot struct {
	Width            int
	Height           int
	Tick             int
	ElapsedSeconds   int
	Countdown        int
	CountdownSeconds int
	Started          bool
	Finished         bool
	Draw             bool
	WinnerID         string
	WinnerName       string

	// SuddenDeath is true once the stalling phase ended. SuddenDeathIn counts
	// the seconds left until it starts (0 while active).
	SuddenDeath   bool
	SuddenDeathIn int
	MatchSeconds  int

	Cells   [][]Cell
	Bombs   []BombSnapshot
	Players []PlayerSnapshot
}

// Cell is the render-ready state of one board position.
type Cell struct {
	Tile    Tile
	Class   string
	PowerUp PowerUpKind
	Boom    bool
}

// BombSnapshot is the render state of an active bomb.
type BombSnapshot struct {
	ID    string
	X     int
	Y     int
	Range int
}

// PlayerSnapshot is the render state of one player.
type PlayerSnapshot struct {
	ID           string
	Name         string
	Color        string
	X            int
	Y            int
	Lives        int
	MaxBombs     int
	BombRange    int
	ActiveBombs  int
	SpeedLevel   int
	Alive        bool
	Eliminated   bool
	Respawning   bool
	Invulnerable bool
	IsYou        bool
	IsBot        bool
	Kick         bool
	Remote       bool
	Shield       bool
}

// ValidName validates and normalizes a display name. It is exported because
// the lobby and the input layer use the same rules.
func ValidName(name string) (string, error) {
	runes := []rune(name)
	out := make([]rune, 0, len(runes))
	space := false
	for _, r := range runes {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if len(out) > 0 {
				space = true
			}
		case r < 32 || r == 127:
			continue
		default:
			if space {
				out = append(out, ' ')
				space = false
			}
			out = append(out, r)
		}
		if len(out) >= 18 {
			break
		}
	}
	if len(out) == 0 {
		return "", errors.New("Bitte gib einen Namen ein.")
	}
	if len(out) < 2 {
		return "", errors.New("Der Name muss mindestens 2 Zeichen lang sein.")
	}
	return string(out), nil
}

// Validate checks the seed collection and board dimensions.
func (c Config) validate(seeds []PlayerSeed) error {
	if c.Width < 9 || c.Height < 9 {
		return fmt.Errorf("board must be at least 9x9, got %dx%d", c.Width, c.Height)
	}
	if c.Width%2 == 0 || c.Height%2 == 0 {
		return errors.New("board dimensions must be odd")
	}
	if len(seeds) < 2 || len(seeds) > 4 {
		return fmt.Errorf("need 2 to 4 players, got %d", len(seeds))
	}
	seen := make(map[string]struct{}, len(seeds))
	for _, s := range seeds {
		if s.ID == "" {
			return errors.New("player id must not be empty")
		}
		if _, ok := seen[s.ID]; ok {
			return fmt.Errorf("duplicate player id %q", s.ID)
		}
		seen[s.ID] = struct{}{}
	}
	return nil
}
