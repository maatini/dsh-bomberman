// Package lobby manages ephemeral rooms, membership, chat and the lifecycle
// of a server-authoritative round. Transport and HTML rendering stay outside.
package lobby

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

	"bomberman/internal/game"
)

const (
	roomCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	roomCodeLength   = 5
	maxRooms         = 512
	roomIdleTTL      = 45 * time.Minute
)

// Manager owns all in-memory rooms and a janitor that removes abandoned ones.
// Everything is intentionally ephemeral: restarting the server starts fresh.
type Manager struct {
	mu     sync.RWMutex
	rooms  map[string]*Room
	logger *slog.Logger
	done   chan struct{}
	once   sync.Once

	// gameCfg is copied into every new room. It is set once at startup and only
	// affects rooms created afterwards.
	gameCfg game.Config
}

// NewManager creates a manager and starts its cleanup loop.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		rooms:   make(map[string]*Room),
		logger:  logger,
		done:    make(chan struct{}),
		gameCfg: game.DefaultConfig(),
	}
	go m.janitor()
	return m
}

// SetGameConfig overrides the simulation settings used by rooms created from
// now on. It is intended for startup configuration (for example a shorter
// sudden-death timer) and is safe to call before the first room exists.
func (m *Manager) SetGameConfig(cfg game.Config) {
	m.mu.Lock()
	m.gameCfg = cfg
	m.mu.Unlock()
}

// Create makes a new room and returns it together with the host token.
func (m *Manager) Create(hostName, roomName string) (*Room, string, error) {
	name, err := game.ValidName(hostName)
	if err != nil {
		return nil, "", err
	}
	roomName = sanitizeRoomName(roomName, name)
	m.mu.Lock()
	if len(m.rooms) >= maxRooms {
		m.mu.Unlock()
		return nil, "", errors.New("Gerade sind zu viele Räume aktiv. Bitte versuche es gleich noch einmal.")
	}
	code := ""
	for {
		var err error
		code, err = randomRoomCode()
		if err != nil {
			m.mu.Unlock()
			return nil, "", err
		}
		if _, exists := m.rooms[code]; !exists {
			break
		}
	}
	room, hostToken, err := newRoom(code, roomName, name, m.logger, m.gameCfg)
	if err != nil {
		m.mu.Unlock()
		return nil, "", err
	}
	m.rooms[code] = room
	m.mu.Unlock()
	return room, hostToken, nil
}

// Join adds a participant to an existing room. It returns a per-tab token and
// whether the participant joined as a spectator.
func (m *Manager) Join(code, name string) (*Room, string, bool, error) {
	code = normalizeCode(code)
	name, err := game.ValidName(name)
	if err != nil {
		return nil, "", false, err
	}
	room, ok := m.Get(code)
	if !ok {
		return nil, "", false, ErrRoomNotFound
	}
	token, spectator, err := room.join(name)
	if err != nil {
		return nil, "", false, err
	}
	return room, token, spectator, nil
}

// Get resolves a room by code.
func (m *Manager) Get(code string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	room, ok := m.rooms[normalizeCode(code)]
	return room, ok
}

// View builds the participant-specific view for a room.
func (m *Manager) View(code, token string) (View, error) {
	room, ok := m.Get(code)
	if !ok {
		return View{}, ErrRoomNotFound
	}
	return room.View(token)
}

// Summary returns public room metadata for the join page.
func (m *Manager) Summary(code string) (Summary, bool) {
	room, ok := m.Get(code)
	if !ok {
		return Summary{}, false
	}
	return room.Summary(), true
}

// Close stops the janitor and all room loops.
func (m *Manager) Close() {
	m.once.Do(func() {
		close(m.done)
		m.mu.Lock()
		rooms := make([]*Room, 0, len(m.rooms))
		for _, room := range m.rooms {
			rooms = append(rooms, room)
		}
		m.rooms = make(map[string]*Room)
		m.mu.Unlock()
		for _, room := range rooms {
			room.Close()
		}
	})
}

// RoomCount is primarily useful in tests and diagnostics.
func (m *Manager) RoomCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms)
}

func (m *Manager) janitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

func (m *Manager) cleanup() {
	type candidate struct {
		code string
		room *Room
	}
	var stale []candidate
	now := time.Now()
	m.mu.RLock()
	for code, room := range m.rooms {
		if room.HasConnections() {
			continue
		}
		if now.Sub(room.idleSince()) > roomIdleTTL {
			stale = append(stale, candidate{code: code, room: room})
		}
	}
	m.mu.RUnlock()
	if len(stale) == 0 {
		return
	}
	m.mu.Lock()
	for _, c := range stale {
		if current, ok := m.rooms[c.code]; ok && current == c.room {
			delete(m.rooms, c.code)
			c.room.Close()
			m.logger.Info("removed idle room", "room", c.code)
		}
	}
	m.mu.Unlock()
}

func randomRoomCode() (string, error) {
	buf := make([]byte, roomCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, roomCodeLength)
	for i, b := range buf {
		out[i] = roomCodeAlphabet[int(b)%len(roomCodeAlphabet)]
	}
	return string(out), nil
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func sanitizeRoomName(name, hostName string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Bomber Grid · " + hostName
	}
	var b strings.Builder
	count := 0
	space := false
	for _, r := range name {
		if r < 32 || r == 127 {
			r = ' '
		}
		if unicode.IsSpace(r) {
			if count == 0 || space {
				continue
			}
			space = true
			b.WriteRune(' ')
			count++
		} else {
			space = false
			b.WriteRune(r)
			count++
		}
		if count >= 32 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "Bomber Grid"
	}
	return out
}
