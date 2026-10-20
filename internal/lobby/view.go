package lobby

import (
	"time"
	"unicode"

	"bomberman/internal/game"
)

// Phase is the lifecycle stage of a room.
type Phase string

const (
	PhaseLobby    Phase = "lobby"
	PhasePlaying  Phase = "playing"
	PhaseFinished Phase = "finished"
)

// Label returns the German UI label for a phase.
func (p Phase) Label() string {
	switch p {
	case PhaseLobby:
		return "Lobby"
	case PhasePlaying:
		return "Runde läuft"
	case PhaseFinished:
		return "Runde beendet"
	default:
		return "Unbekannt"
	}
}

// ChatMessage is one immutable chat line.
type ChatMessage struct {
	Seq    int
	Author string
	Text   string
	Color  string
	Clock  string
	System bool
}

// MemberView is the public projection of a room member.
type MemberView struct {
	ID        string
	Name      string
	Color     string
	Initial   string
	Slot      int
	IsHost    bool
	IsYou     bool
	IsBot     bool
	Connected bool
	Spectator bool
}

// View is everything the HTMX renderer needs for one specific participant.
type View struct {
	Code      string
	Name      string
	Phase     string
	PhaseText string
	Token     string
	SharePath string

	You              MemberView
	Players          []MemberView
	Spectators       []MemberView
	PlayerCount      int
	ConnectedPlayers int
	BotCount         int
	SpectatorCount   int
	MaxPlayers       int
	MinPlayers       int

	IsHost       bool
	CanStart     bool
	StartHint    string
	CanRematch   bool
	CanAddBot    bool
	CanRemoveBot bool

	IsSpectator bool
	Connected   bool

	Game *game.Snapshot
	Chat []ChatMessage
}

// Summary is the public room information used by the join page.
type Summary struct {
	Code       string
	Name       string
	Phase      string
	PhaseText  string
	Players    int
	Connected  int
	MaxPlayers int
	Bots       int
	Spectators int
	JoinAs     string // "player" or "spectator"
}

// newMemberView projects an internal member.
func newMemberView(m *member, youID string, hostID string) MemberView {
	initial := "?"
	for _, r := range m.Name {
		initial = string(unicode.ToUpper(r))
		break
	}
	return MemberView{
		ID:        m.ID,
		Name:      m.Name,
		Color:     m.Color,
		Initial:   initial,
		Slot:      m.Slot,
		IsHost:    m.ID == hostID,
		IsYou:     m.ID == youID,
		IsBot:     m.Bot,
		Connected: m.Bot || m.connections > 0,
		Spectator: m.Spectator,
	}
}

// idleSince is used by the janitor.
func (r *Room) idleSince() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastActivity
}
