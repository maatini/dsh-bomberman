package game

import (
	"math/rand"
)

// spawnZoneRadius is how many cells around a spawn are kept free of bricks.
// Solid pillars inside the square remain solid, so the spawn always has exits.
const spawnZoneRadius = 1

type board struct {
	tiles [][]Tile
}

func newBoard(cfg Config, rng *rand.Rand, spawns []Position) board {
	tiles := make([][]Tile, cfg.Height)
	for y := range tiles {
		tiles[y] = make([]Tile, cfg.Width)
		for x := range tiles[y] {
			switch {
			case x == 0 || y == 0 || x == cfg.Width-1 || y == cfg.Height-1:
				tiles[y][x] = TileSolid
			case x%2 == 0 && y%2 == 0:
				tiles[y][x] = TileSolid
			default:
				tiles[y][x] = TileFloor
			}
		}
	}

	clear := make(map[Position]bool)
	for _, sp := range spawns {
		for dy := -spawnZoneRadius; dy <= spawnZoneRadius; dy++ {
			for dx := -spawnZoneRadius; dx <= spawnZoneRadius; dx++ {
				p := Position{X: sp.X + dx, Y: sp.Y + dy}
				if p.X > 0 && p.Y > 0 && p.X < cfg.Width-1 && p.Y < cfg.Height-1 {
					clear[p] = true
				}
			}
		}
	}

	for y := 1; y < cfg.Height-1; y++ {
		for x := 1; x < cfg.Width-1; x++ {
			p := Position{X: x, Y: y}
			if clear[p] || tiles[y][x] != TileFloor || x%2 == 0 && y%2 == 0 {
				continue
			}
			if rng.Float64() < cfg.BrickDensity {
				tiles[y][x] = TileBrick
			}
		}
	}
	return board{tiles: tiles}
}

func (b board) at(p Position) Tile {
	if p.Y < 0 || p.Y >= len(b.tiles) || p.X < 0 || p.X >= len(b.tiles[p.Y]) {
		return TileSolid
	}
	return b.tiles[p.Y][p.X]
}

func (b board) set(p Position, t Tile) {
	b.tiles[p.Y][p.X] = t
}

func (b board) walkable(p Position) bool {
	return b.at(p) == TileFloor
}

// spawnPositions returns the four classic corner spawn points in a stable
// order. Slot 0 is top-left, then top-right, bottom-left and bottom-right.
func spawnPositions(cfg Config) []Position {
	return []Position{
		{X: 1, Y: 1},
		{X: cfg.Width - 2, Y: 1},
		{X: 1, Y: cfg.Height - 2},
		{X: cfg.Width - 2, Y: cfg.Height - 2},
	}
}
