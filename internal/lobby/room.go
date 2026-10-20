package lobby

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bomberman/internal/game"
)

// Room-related errors are intentionally user-facing (German) because the HTTP
// handlers surface them directly in the UI.
var (
	ErrRoomNotFound     = errors.New("Dieser Raum existiert nicht (mehr).")
	ErrInvalidToken     = errors.New("Deine Sitzung ist ungültig. Bitte tritt dem Raum erneut bei.")
	ErrRoomClosed       = errors.New("Dieser Raum wurde geschlossen.")
	ErrNotHost          = errors.New("Nur der Host kann diese Aktion ausführen.")
	ErrNotEnoughPlayers = errors.New("Mindestens zwei verbundene Spieler:innen werden benötigt.")
	ErrWrongPhase       = errors.New("Diese Aktion ist in der aktuellen Phase nicht möglich.")
	ErrRateLimited      = errors.New("Bitte warte einen Moment, bevor du wieder schreibst.")
	ErrChatEmpty        = errors.New("Die Nachricht ist leer.")
)

// MaxPlayers is the number of active player slots in one room.
const MaxPlayers = 4

// MinPlayers is required to start a round.
const MinPlayers = 2

// maxChatRunes limits chat messages.
const maxChatRunes = 240

var playerColors = []string{
	"#ff7a3d", // ember
	"#42c9ff", // ice
	"#9be564", // lime
	"#f18bff", // bubblegum
}

type member struct {
	ID        string
	Name      string
	Color     string
	Slot      int
	Spectator bool
	Bot       bool

	connections  int
	offlineSince time.Time
	joinedAt     time.Time
	lastChat     time.Time
}

const (
	// reconnectGrace is how long a disconnected player keeps their slot before
	// the simulation eliminates them.
	reconnectGrace = 45 * time.Second
	// hostGrace keeps the crown during a short browser reload or network blip.
	hostGrace = 15 * time.Second
)

// Room owns one lobby and at most one running round. All mutable state is
// protected by mu; the game loop, websocket inputs and HTTP handlers all
// serialize through it.
type Room struct {
	mu sync.Mutex

	code   string
	name   string
	phase  Phase
	hostID string

	members map[string]*member
	order   []string

	game    *game.Engine
	gameCfg game.Config
	chat    []ChatMessage
	chatSeq int
	botSeq  int

	subs map[chan struct{}]struct{}

	createdAt    time.Time
	lastActivity time.Time
	closed       bool
	closeOnce    sync.Once
	done         chan struct{}
	logger       *slog.Logger
}

func newRoom(code, name, hostName string, logger *slog.Logger, gameCfg game.Config) (*Room, string, error) {
	r := &Room{
		code:         code,
		name:         name,
		phase:        PhaseLobby,
		gameCfg:      gameCfg,
		members:      make(map[string]*member, MaxPlayers),
		subs:         make(map[chan struct{}]struct{}),
		createdAt:    time.Now(),
		lastActivity: time.Now(),
		done:         make(chan struct{}),
		logger:       logger,
	}
	hostToken, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	r.members[hostToken] = &member{
		ID:       hostToken,
		Name:     hostName,
		Color:    playerColors[0],
		Slot:     0,
		joinedAt: time.Now(),
	}
	r.order = append(r.order, hostToken)
	r.hostID = hostToken
	r.addSystemLocked("Raum erstellt. Teile den Einladungslink mit deinen Freund:innen.")
	go r.loop()
	return r, hostToken, nil
}

// Code returns the immutable room code.
func (r *Room) Code() string { return r.code }

// loop drives the authoritative simulation at a fixed 20 Hz. HTML rendering is
// intentionally throttled separately in the websocket writer.
func (r *Room) loop() {
	ticker := time.NewTicker(game.TickDuration)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			r.mu.Lock()
			if r.closed {
				r.mu.Unlock()
				return
			}
			now := time.Now()
			r.maybeTransferHostLocked(now)
			if r.phase == PhasePlaying && r.game != nil {
				r.game.Tick()
				r.forfeitAwayLocked(now)
				r.lastActivity = now
				if r.game.Finished() {
					r.phase = PhaseFinished
					name, draw := r.game.Winner()
					if draw {
						r.addSystemLocked("Unentschieden – alle Spieler:innen sind ausgeschieden.")
					} else {
						r.addSystemLocked(name + " gewinnt die Runde! 🏆")
					}
				}
				r.notifyLocked()
			}
			r.mu.Unlock()
		}
	}
}

// Close stops the room loop. It is safe to call more than once.
func (r *Room) Close() {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		close(r.done)
	})
}

// Subscribe returns a coalescing update channel. The cancel function must be
// called when the websocket closes.
func (r *Room) Subscribe() (<-chan struct{}, func(), error) {
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, ErrRoomClosed
	}
	r.subs[ch] = struct{}{}
	r.mu.Unlock()

	cancel := func() {
		r.mu.Lock()
		if _, ok := r.subs[ch]; ok {
			delete(r.subs, ch)
			close(ch)
		}
		r.mu.Unlock()
	}
	return ch, cancel, nil
}

func (r *Room) notifyLocked() {
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// join adds a participant. During a running or finished round, new arrivals
// become spectators. A lobby that is only full because of bots makes room for
// the human instead of turning them away.
func (r *Room) join(name string) (token string, spectator bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", false, ErrRoomClosed
	}
	var replaced *member
	switch {
	case r.phase != PhaseLobby:
		spectator = true
	case r.playerCountLocked() >= MaxPlayers:
		replaced = r.removeBotLocked()
		if replaced == nil {
			spectator = true
		}
	}
	token, err = randomToken()
	if err != nil {
		return "", false, err
	}
	name = r.uniqueNameLocked(name)
	slot := -1
	color := "#9aa4b2"
	if !spectator {
		slot = r.nextSlotLocked()
		color = playerColors[slot]
	}
	r.members[token] = &member{
		ID:        token,
		Name:      name,
		Color:     color,
		Slot:      slot,
		Spectator: spectator,
		joinedAt:  time.Now(),
	}
	r.order = append(r.order, token)
	r.lastActivity = time.Now()
	if replaced != nil {
		r.addSystemLocked(replaced.Name + " macht Platz für " + name + ".")
	}
	if !spectator {
		r.addSystemLocked(name + " ist der Lobby beigetreten.")
	} else {
		r.addSystemLocked(name + " schaut als Zuschauer:in zu.")
	}
	r.notifyLocked()
	return token, spectator, nil
}

// removeBotLocked drops the most recently added bot and reports it.
func (r *Room) removeBotLocked() *member {
	var last *member
	for _, m := range r.members {
		if !m.Bot {
			continue
		}
		if last == nil || m.Slot > last.Slot {
			last = m
		}
	}
	if last == nil {
		return nil
	}
	delete(r.members, last.ID)
	for i, id := range r.order {
		if id == last.ID {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return last
}

func (r *Room) playerCountLocked() int {
	n := 0
	for _, m := range r.members {
		if !m.Spectator {
			n++
		}
	}
	return n
}

func (r *Room) nextSlotLocked() int {
	used := [MaxPlayers]bool{}
	for _, m := range r.members {
		if !m.Spectator && m.Slot >= 0 && m.Slot < MaxPlayers {
			used[m.Slot] = true
		}
	}
	for i := range used {
		if !used[i] {
			return i
		}
	}
	return 0
}

func (r *Room) uniqueNameLocked(name string) string {
	exists := func(candidate string) bool {
		for _, m := range r.members {
			if strings.EqualFold(m.Name, candidate) {
				return true
			}
		}
		return false
	}
	if !exists(name) {
		return name
	}
	base := []rune(name)
	for n := 2; n < 100; n++ {
		suffix := fmt.Sprintf(" %d", n)
		maxBase := 18 - utf8.RuneCountInString(suffix)
		if maxBase < 1 {
			maxBase = 1
		}
		if len(base) > maxBase {
			candidate := string(base[:maxBase]) + suffix
			if !exists(candidate) {
				return candidate
			}
			continue
		}
		candidate := string(base) + suffix
		if !exists(candidate) {
			return candidate
		}
	}
	return name
}

// Connect marks a websocket connection as alive, transferring host duties if
// the previous host is offline.
func (r *Room) Connect(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	m, ok := r.members[token]
	if !ok {
		return ErrInvalidToken
	}
	m.connections++
	if m.connections == 1 {
		m.offlineSince = time.Time{}
	}
	r.lastActivity = time.Now()
	r.notifyLocked()
	return nil
}

// Disconnect marks a websocket connection as gone and transfers the host role
// to another connected player when necessary.
func (r *Room) Disconnect(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[token]
	if !ok {
		return
	}
	if m.connections > 0 {
		m.connections--
	}
	if m.connections > 0 {
		r.notifyLocked()
		return
	}
	if m.offlineSince.IsZero() {
		m.offlineSince = time.Now()
	}
	r.lastActivity = time.Now()
	r.notifyLocked()
}

// SetDirection forwards a held movement direction to the simulation.
func (r *Room) SetDirection(token, dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.members[token]
	if m == nil || m.Spectator || r.phase != PhasePlaying || r.game == nil {
		return
	}
	r.game.SetDirection(token, game.ParseDirection(dir))
}

// PlaceBomb forwards a bomb request to the simulation.
func (r *Room) PlaceBomb(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.members[token]
	if m == nil || m.Spectator || r.phase != PhasePlaying || r.game == nil {
		return
	}
	r.game.PlaceBomb(token)
}

// Detonate triggers a player's primed bombs when they own the remote power-up.
func (r *Room) Detonate(token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.members[token]
	if m == nil || m.Spectator || r.phase != PhasePlaying || r.game == nil {
		return
	}
	r.game.Detonate(token)
}

// botNames are cycled when the host fills the lobby with bots.
var botNames = []string{
	"Bot Nova",
	"Bot Rex",
	"Bot Pixel",
	"Bot Volt",
	"Bot Kira",
	"Bot Zen",
}

// AddBot fills the next free player slot with a server-side bot. Bots exist
// only in the lobby: they are seeded into the round like any other player.
func (r *Room) AddBot(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if _, ok := r.members[token]; !ok {
		return ErrInvalidToken
	}
	if r.hostID != token {
		return ErrNotHost
	}
	if r.phase == PhasePlaying {
		return ErrWrongPhase
	}
	if r.playerCountLocked() >= MaxPlayers {
		return errors.New("Der Raum ist voll.")
	}
	r.botSeq++
	id := fmt.Sprintf("bot-%d", r.botSeq)
	slot := r.nextSlotLocked()
	name := r.uniqueNameLocked(botNames[(r.botSeq-1)%len(botNames)])
	r.members[id] = &member{
		ID:       id,
		Name:     name,
		Color:    playerColors[slot],
		Slot:     slot,
		Bot:      true,
		joinedAt: time.Now(),
	}
	r.order = append(r.order, id)
	r.addSystemLocked(name + " (Bot) verstärkt die Runde.")
	r.lastActivity = time.Now()
	r.notifyLocked()
	return nil
}

// RemoveBot removes the most recently added bot.
func (r *Room) RemoveBot(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if _, ok := r.members[token]; !ok {
		return ErrInvalidToken
	}
	if r.hostID != token {
		return ErrNotHost
	}
	if r.phase == PhasePlaying {
		return ErrWrongPhase
	}
	last := r.removeBotLocked()
	if last == nil {
		return errors.New("Es ist kein Bot im Raum.")
	}
	r.addSystemLocked(last.Name + " hat den Raum verlassen.")
	r.lastActivity = time.Now()
	r.notifyLocked()
	return nil
}

// isOnlineLocked reports whether a member can take part right now. Bots are
// always available because they live inside the server.
func (r *Room) isOnlineLocked(m *member) bool {
	return m != nil && (m.Bot || m.connections > 0)
}

// Say appends a validated chat message.
func (r *Room) Say(token, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[token]
	if !ok {
		return ErrInvalidToken
	}
	if time.Since(m.lastChat) < 250*time.Millisecond {
		return ErrRateLimited
	}
	text = sanitizeChat(text)
	if text == "" {
		return ErrChatEmpty
	}
	m.lastChat = time.Now()
	r.addChatLocked(ChatMessage{
		Author: m.Name,
		Text:   text,
		Color:  m.Color,
		Clock:  time.Now().Format("15:04"),
	})
	r.lastActivity = time.Now()
	r.notifyLocked()
	return nil
}

func sanitizeChat(text string) string {
	text = strings.TrimSpace(text)
	var b strings.Builder
	count := 0
	space := false
	for _, runeValue := range text {
		if runeValue < 32 || runeValue == 127 {
			runeValue = ' '
		}
		if runeValue == ' ' {
			if count == 0 || space {
				continue
			}
			space = true
			b.WriteRune(' ')
			count++
			continue
		}
		space = false
		b.WriteRune(runeValue)
		count++
		if count >= maxChatRunes {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func (r *Room) addChatLocked(msg ChatMessage) {
	r.chatSeq++
	msg.Seq = r.chatSeq
	if msg.Clock == "" {
		msg.Clock = time.Now().Format("15:04")
	}
	r.chat = append(r.chat, msg)
	if len(r.chat) > 50 {
		r.chat = r.chat[len(r.chat)-50:]
	}
}

func (r *Room) addSystemLocked(text string) {
	r.addChatLocked(ChatMessage{Text: text, System: true})
}

// Start begins a round when called by the host. Only connected players are
// included in the new simulation.
func (r *Room) Start(token string) error {
	return r.startGame(token, PhaseLobby)
}

// Rematch starts a fresh round after one has finished.
func (r *Room) Rematch(token string) error {
	return r.startGame(token, PhaseFinished)
}

func (r *Room) startGame(token string, requiredPhase Phase) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRoomClosed
	}
	if _, ok := r.members[token]; !ok {
		return ErrInvalidToken
	}
	if r.hostID != token {
		return ErrNotHost
	}
	if r.phase != requiredPhase {
		return ErrWrongPhase
	}
	seeds := make([]game.PlayerSeed, 0, MaxPlayers)
	for _, id := range r.onlinePlayersLocked() {
		m := r.members[id]
		seeds = append(seeds, game.PlayerSeed{ID: m.ID, Name: m.Name, Color: m.Color, IsBot: m.Bot})
	}
	if len(seeds) < MinPlayers {
		return ErrNotEnoughPlayers
	}
	seed, err := randomSeed()
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	g, err := game.New(seeds, r.gameCfg, seed)
	if err != nil {
		return fmt.Errorf("new game: %w", err)
	}
	r.game = g
	r.phase = PhasePlaying
	r.addSystemLocked("Runde gestartet – viel Glück!")
	r.lastActivity = time.Now()
	r.notifyLocked()
	return nil
}

// maybeTransferHostLocked hands the crown to another connected player if the
// host has been offline for longer than hostGrace. A quick page reload keeps the
// original host.
func (r *Room) maybeTransferHostLocked(now time.Time) {
	host := r.members[r.hostID]
	if host == nil || host.connections > 0 || host.offlineSince.IsZero() {
		return
	}
	if now.Sub(host.offlineSince) < hostGrace {
		return
	}
	for _, id := range r.order {
		candidate := r.members[id]
		if candidate == nil || candidate.ID == r.hostID || candidate.Spectator || candidate.Bot || candidate.connections == 0 {
			continue
		}
		r.hostID = candidate.ID
		r.addSystemLocked(candidate.Name + " übernimmt die Host-Rolle.")
		r.notifyLocked()
		return
	}
}

// forfeitAwayLocked removes players whose disconnect grace has expired. It is
// called from the simulation loop under the room lock.
func (r *Room) forfeitAwayLocked(now time.Time) {
	if r.game == nil || r.phase != PhasePlaying {
		return
	}
	for _, id := range r.order {
		m := r.members[id]
		if m == nil || m.Spectator || m.Bot || m.connections > 0 || m.offlineSince.IsZero() {
			continue
		}
		if now.Sub(m.offlineSince) < reconnectGrace {
			continue
		}
		if r.game.Forfeit(id) {
			r.addSystemLocked(m.Name + " wurde nach Verbindungsverlust aus der Runde entfernt.")
		}
	}
}

func (r *Room) onlinePlayersLocked() []string {
	players := make([]*member, 0, MaxPlayers)
	for _, m := range r.members {
		if !m.Spectator && r.isOnlineLocked(m) {
			players = append(players, m)
		}
	}
	sort.Slice(players, func(i, j int) bool { return players[i].Slot < players[j].Slot })
	ids := make([]string, len(players))
	for i, m := range players {
		ids[i] = m.ID
	}
	return ids
}

// View builds a snapshot for one token.
func (r *Room) View(token string) (View, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[token]
	if !ok {
		return View{}, ErrInvalidToken
	}

	v := View{
		Code:        r.code,
		Name:        r.name,
		Phase:       string(r.phase),
		PhaseText:   r.phase.Label(),
		Token:       token,
		SharePath:   "/room/" + r.code,
		MaxPlayers:  MaxPlayers,
		MinPlayers:  MinPlayers,
		IsHost:      r.hostID == token,
		IsSpectator: m.Spectator,
		Connected:   r.isOnlineLocked(m),
		Chat:        append([]ChatMessage(nil), r.chat...),
	}
	v.You = newMemberView(m, token, r.hostID)

	for _, id := range r.order {
		mm := r.members[id]
		mv := newMemberView(mm, token, r.hostID)
		if mm.Spectator {
			v.Spectators = append(v.Spectators, mv)
		} else {
			v.Players = append(v.Players, mv)
		}
		if !mm.Spectator {
			v.PlayerCount++
			if mm.Bot {
				v.BotCount++
			}
			if r.isOnlineLocked(mm) {
				v.ConnectedPlayers++
			}
		}
	}
	sort.SliceStable(v.Players, func(i, j int) bool {
		if v.Players[i].Slot == v.Players[j].Slot {
			return v.Players[i].Name < v.Players[j].Name
		}
		return v.Players[i].Slot < v.Players[j].Slot
	})
	v.SpectatorCount = len(v.Spectators)

	if r.game != nil {
		snap := r.game.Snapshot()
		found := false
		for i := range snap.Players {
			if snap.Players[i].ID == token {
				found = true
				snap.Players[i].IsYou = true
			}
		}
		// A member who was offline when the round started (or joined from the
		// lobby later) watches this round without owning a figure.
		if r.phase != PhaseLobby && !found {
			v.IsSpectator = true
			v.You.Spectator = true
		}
		v.Game = &snap
	}

	v.CanStart = v.IsHost && r.phase == PhaseLobby && v.ConnectedPlayers >= MinPlayers
	v.CanRematch = v.IsHost && r.phase == PhaseFinished && v.ConnectedPlayers >= MinPlayers
	v.CanAddBot = v.IsHost && r.phase != PhasePlaying && v.PlayerCount < MaxPlayers
	v.CanRemoveBot = v.IsHost && r.phase != PhasePlaying && v.BotCount > 0
	switch {
	case !v.IsHost:
		v.StartHint = "Nur der Host kann die Runde starten."
	case v.ConnectedPlayers < MinPlayers:
		v.StartHint = "Mindestens zwei Spieler:innen werden benötigt – ein Bot reicht dafür schon."
	default:
		v.StartHint = "Alle bereit? Dann kann es losgehen."
	}
	return v, nil
}

// Summary returns token-free public room information.
func (r *Room) Summary() Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Summary{
		Code:       r.code,
		Name:       r.name,
		Phase:      string(r.phase),
		PhaseText:  r.phase.Label(),
		MaxPlayers: MaxPlayers,
		JoinAs:     "player",
	}
	for _, m := range r.members {
		if m.Spectator {
			s.Spectators++
			continue
		}
		s.Players++
		if m.Bot {
			s.Bots++
		}
		if r.isOnlineLocked(m) {
			s.Connected++
		}
	}
	// A full lobby still accepts humans when a bot can be replaced.
	if r.phase != PhaseLobby || (s.Players >= MaxPlayers && s.Bots == 0) {
		s.JoinAs = "spectator"
	}
	return s
}

// HasConnections reports whether any member currently has an open socket.
func (r *Room) HasConnections() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.members {
		if m.connections > 0 {
			return true
		}
	}
	return false
}

func randomToken() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomSeed() (int64, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return 0, err
	}
	var seed int64
	for _, x := range b {
		seed = seed<<8 | int64(x)
	}
	if seed < 0 {
		seed = -seed
	}
	return seed, nil
}
