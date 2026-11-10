// The Gomoku Party service — five-in-a-row for 2–4 players on a shared 19×19
// board with rotating turns. Same both-sides-validate shape as the two-player
// gomoku/connect4 services (a move is a free (row,col) placement; a position
// hash is computed identically on send and receive), but backed by game.Ring
// (N ordered seats + a rotating turn) instead of the two-seat game.Table.
//
// Seating goes through a take-a-seat / watch LOBBY: the host opens a table, any
// participant (player or spectator) claims or releases a seat (up to MaxSeats),
// and the host begins once ≥2 are seated. Seating is host-authoritative (the
// host owns the claimed list and broadcasts it; every end adopts it verbatim).
// Play is peer-to-peer both-sides-validate; a mover's color is derived from its
// seat, never trusted from the wire.
package gomokup

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

const ID = "gomokup"

// MaxSeats is the player cap for a single game.
const MaxSeats = 4

const (
	kindNewGame  uint8 = 1 // host→all: begin play with a seat list + opening turn
	kindStartReq uint8 = 2 // non-host→host: please open a table
	kindPlace    uint8 = 3
	kindResign   uint8 = 4
	kindOpen     uint8 = 5 // host→all: a table is open for seating (carries claimed)
	kindSeats    uint8 = 6 // host→all: authoritative claimed-seat list
	kindSeatReq  uint8 = 7 // participant→host: take a seat
	kindLeaveReq uint8 = 8 // participant→host: leave my seat
)

type msg struct {
	Kind      uint8    `cbor:"1,keyasint"`
	Seats     []uint32 `cbor:"2,keyasint,omitempty"` // seat/claimed list
	Turn      uint8    `cbor:"3,keyasint,omitempty"` // opening seat index (begin)
	Row       int8     `cbor:"4,keyasint,omitempty"`
	Col       int8     `cbor:"5,keyasint,omitempty"`
	StateHash []byte   `cbor:"6,keyasint,omitempty"`
}

type snapshot struct {
	Board   Board    `cbor:"1,keyasint"`
	Seats   []uint32 `cbor:"2,keyasint,omitempty"`
	Turn    uint8    `cbor:"3,keyasint"`
	Phase   uint8    `cbor:"4,keyasint"`
	Winner  int8     `cbor:"5,keyasint"` // 0 none/abandoned, else winning seat+1
	Draw    bool     `cbor:"6,keyasint,omitempty"`
	Last    int16    `cbor:"7,keyasint"`
	History []string `cbor:"8,keyasint,omitempty"`
	Gone    []bool   `cbor:"9,keyasint,omitempty"`
	Lobby   bool     `cbor:"10,keyasint,omitempty"`
	Claimed []uint32 `cbor:"11,keyasint,omitempty"`
}

// State is emitted after every change; the UI renders it directly.
type State struct {
	Playing  bool
	Lobby    bool // in the take-a-seat lobby (not yet playing)
	Board    Board
	Seats    []wire.ParticipantID // lobby: claimed seats; playing: the seats (index = color-1)
	Gone     []bool               // per-seat: left/resigned (playing)
	TurnID   wire.ParticipantID   // 0 when over/idle/lobby
	WinnerID wire.ParticipantID   // 0 = none/abandoned/draw
	Draw     bool
	Outcome  string // "", "seat N wins", "abandoned", "draw" (UI shows names)
	WinCells []int16
	Last     int16    // last placed stone's index, -1 when none
	History  []string // ordered notation, "🔴 h8"
	MaxSeats int      // seat cap
	CanBegin bool     // this end is the host and ≥2 are seated
}

// stoneGlyph is the notation marker per seat color (index 0..MaxSeats-1).
var stoneGlyph = [MaxSeats]string{"🔴", "🔵", "🟢", "🟡"}

var (
	ErrNotTurn  = errors.New("gomokup: not your turn")
	ErrNoGame   = errors.New("gomokup: no game in progress")
	ErrNotSeat  = errors.New("gomokup: not a player in this game")
	ErrNoResign = errors.New("gomokup: no game to resign")

	errNotHost    = errors.New("gomokup: only the host can do that")
	errNotLobby   = errors.New("gomokup: no open table")
	errTooFew     = errors.New("gomokup: need at least two seated players")
	errInProgress = errors.New("gomokup: a game is in progress")
)

// Service implements service.Service; the mutex covers game state between the
// mux goroutine and UI calls.
type Service struct {
	service.Base

	mu      sync.Mutex
	ring    game.Ring
	lobby   bool                 // an open table awaiting seats
	claimed []wire.ParticipantID // host-authoritative claimed seats (mirrored on all ends)
	board   Board
	ph      game.Phase
	winner  int8 // 0 none/abandoned, else winning seat+1
	draw    bool
	last    int16
	history []string

	// gen counts state mutations. A local action mutates before it broadcasts,
	// so a failed broadcast would leave this end ahead of every peer and the
	// both-sides-validate hash check would then reject every later move
	// (unrecoverable without a rejoin). Callers therefore snapshot the state,
	// capture gen after mutating, and roll back on send failure — but only if
	// gen is unchanged, i.e. nothing else landed in between and moved the state
	// on legitimately.
	//
	// THE INVARIANT: gen advances exactly once for every write to the state that
	// rollback can rewind — which is every write, from any goroutine, in any
	// direction. That includes the local user actions (hostOpen, hostClaim,
	// hostRelease, Begin, Place, Resign) AND every inbound path that used to be
	// invisible here: the frame handlers (handleNewGame, handleOpen, handleSeats,
	// handlePlace, handleResign), MemberLeft, Restore and OnPromote.
	//
	// Getting this wrong is expensive in one direction only, so the rule is
	// deliberately "bump whenever state actually changed":
	//
	//   - A missed bump is a correctness bug. The peer's write is real, gen still
	//     matches, and rollback clobbers it — resurrecting a departed player or
	//     discarding a peer's new game.
	//   - A spurious bump is a lesser bug but still a bug: it cancels a rollback
	//     that was genuinely needed, leaving this end ahead of every peer. So
	//     paths that must not change anything (a repeated claim, a duplicate seat
	//     list, a leave from a participant this end never seated) return without
	//     bumping — see hostClaim/hostRelease, handleSeats and MemberLeft.
	//
	// gen is monotonic by construction: it lives here, outside stateSnapshot, so
	// a rollback or a Restore can never rewind it. It only ever increases.
	gen uint64
}

func New() *Service { return &Service{ring: game.NewRing(MaxSeats)} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.SetContext(ctx) }

// OnPromote resets rematch bookkeeping when this end is promoted to host.
//
// This is a state write like any other — it changes ring.Games, which decides
// the opening turn of the next Begin — so it must be visible to a broadcast that
// is in flight on another goroutine. Missing it here let a rollback started
// before the promotion restore the pre-promotion Games counter.
func (s *Service) OnPromote() {
	s.mu.Lock()
	prev := s.saveLocked()
	s.ring.OnPromote()
	s.writeLocked(prev)
	s.mu.Unlock()
}

// MemberKeyed is a no-op: seating is by explicit lobby claim, not by keying.
// (Kept to satisfy the MemberObserver interface alongside MemberLeft.)
func (s *Service) MemberKeyed(wire.ParticipantID, session.Role) {}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	host := s.Ctx().Host
	// Prune the departed participant from the lobby list on EVERY end, not only
	// on the current host.
	//
	// parley dispatches session.MemberLeft to every service BEFORE it promotes a
	// successor (mux.dispatch calls observeMembers, and only afterwards
	// maybeMigrate). So on the survivor that is about to become host, this runs
	// while Ctx().Host is still false. Gating the prune on `host` therefore left
	// the DEPARTED HOST in `claimed`; the promoted host then began a game whose
	// seats still named a participant that had already left, which
	// applyDepartedLocked immediately resolved as abandoned (or, with two
	// seats, as a phantom walkover win) — and because OnPromote also zeroes
	// ring.Games, the opening turn pointed at that dead seat. The promoted host
	// could never start a real game again.
	//
	// Pruning unconditionally is safe: `claimed` is a mirror of the host's
	// authoritative list, and every end receives the same MemberLeft for the
	// same participant, so all ends converge without needing a broadcast.
	prev := s.saveLocked()
	s.claimed = removeID(s.claimed, id)
	winnerSeat, over := s.ring.NoteLeft(id, s.ph)
	if over {
		s.endLocked(winnerSeat)
	}
	// NoteLeft has recorded the departure; drop everyone known-departed, so a
	// participant whose leave this end already saw can never be listed as
	// seated again by a stale list that arrived in between.
	s.claimed = removeDeparted(s.claimed, s.ring.Departed)
	// A leave is a state write: it prunes claimed, marks a seat Gone and can end
	// the game. Bump gen so a broadcast that is in flight on another goroutine
	// cannot roll any of that away (and see restoreLocked, which additionally
	// refuses to forget the departure itself).
	s.writeLocked(prev)
	inLobby := s.lobby
	seats := idsToU32(s.claimed)
	s.mu.Unlock()

	switch {
	case over:
		s.emitState()
	case host && inLobby:
		_ = s.broadcastSeats(seats) // a claimant left the lobby → free the seat everywhere
	}
}

// --- broadcast rollback ------------------------------------------------------
//
// Every mutating local action follows the same shape:
//
//	s.mu.Lock(); prev := s.saveLocked(); ...mutate...; gen := s.writeLocked(prev); s.mu.Unlock()
//	body, err := wire.Marshal(...)             // pre-lock, cheap
//	if err := s.Ctx().Send.Broadcast(...); err != nil { s.rollback(prev, gen); ... }
//
// The mutex is deliberately NOT held across Broadcast: a WebSocket write can
// block (parley writes with context.Background(), so there is no send timeout),
// and holding s.mu there would wedge the mux goroutine and every peer frame
// behind it.
//
// gen guards the rollback: if ANYTHING else wrote state while we were
// broadcasting — a peer frame, a session leave, a late-joiner snapshot, a host
// promotion — that write has already moved the state forward legitimately, and
// restoring an older snapshot would clobber it. In that case we keep the newer
// state and report the send error only.
//
// writeLocked is what keeps that guarantee honest across the whole package: it
// bumps gen iff the state really changed, and EVERY write path calls it — the
// local actions, the inbound frame handlers, MemberLeft, Restore and OnPromote.
// A missed call is a correctness bug (a real peer write becomes invisible to
// rollback); a spurious bump is also a bug (it cancels a rollback that was
// needed), which is why no-op paths return before mutating anything at all.

// stateSnapshot is a value copy of everything a rollback can rewind. Board is a
// fixed-size array so this allocation is cheap and cannot alias.
type stateSnapshot struct {
	ring    game.Ring
	lobby   bool
	claimed []wire.ParticipantID
	board   Board
	ph      game.Phase
	winner  int8
	draw    bool
	last    int16
	history []string
}

// saveLocked copies the rewindable state. Caller holds s.mu.
//
// ring.Departed is deliberately NOT in the copy: it is a monotone fact set, not
// rewindable state. A departure is final (participant IDs are never reused
// within a session), so rolling one back would leave this end permanently
// convinced that a player who is gone is still here — and Departed is the only
// replay source that could ever correct it. See restoreLocked.
func (s *Service) saveLocked() stateSnapshot {
	return stateSnapshot{
		// Ring's slices are replaced (never mutated in place) by SetSeats/
		// ApplyDeparted, except Gone which IS mutated in place by markGone, so
		// clone the mutable parts explicitly.
		ring:    cloneRing(s.ring),
		lobby:   s.lobby,
		claimed: append([]wire.ParticipantID(nil), s.claimed...),
		board:   s.board,
		ph:      s.ph,
		winner:  s.winner,
		draw:    s.draw,
		last:    s.last,
		history: cloneHistory(s.history),
	}
}

// restoreLocked puts a snapshot back, then re-derives what happened while the
// failed broadcast was in flight and must not be thrown away:
//
//   - ring.Departed is carried across from the LIVE ring, never from prev. A
//     leave observed during the broadcast is a fact about the session, and the
//     restore is not entitled to forget it.
//   - the effects of those departures are replayed against the restored state,
//     because they were originally computed against a state no peer ever saw.
//     Replaying is the right thing to do rather than copying them: the failed
//     move is gone, so the turn it advanced is back where every peer still has
//     it, and a leave that looked like "the turn holder left" at broadcast time
//     may well be an ordinary off-turn leave from their point of view.
func (s *Service) restoreLocked(prev stateSnapshot) {
	departed := s.ring.Departed // live, monotone: never rewound
	s.ring = cloneRing(prev.ring)
	s.ring.Departed = departed
	s.lobby = prev.lobby
	s.claimed = append([]wire.ParticipantID(nil), prev.claimed...)
	s.board = prev.board
	s.ph = prev.ph
	s.winner = prev.winner
	s.draw = prev.draw
	s.last = prev.last
	s.history = cloneHistory(prev.history)
	// The snapshot's lobby list may predate the leave: drop anyone we now know has
	// gone, so a rollback cannot put a departed player back in the seating list.
	s.claimed = removeDeparted(s.claimed, s.ring.Departed)
	s.applyDepartedLocked()
}

// writeLocked reports a state write to the rollback protocol and returns gen.
//
// Callers call it while still holding s.mu, immediately after mutating, with the
// snapshot taken just before the mutation. It bumps gen only when the state
// actually differs, so an idempotent frame (a repeated seat list, a duplicate
// leave) does not masquerade as a write and cancel somebody else's rollback.
func (s *Service) writeLocked(prev stateSnapshot) uint64 {
	if !s.stateEqualLocked(prev) {
		s.gen++
	}
	return s.gen
}

// stateEqualLocked reports whether the live state matches a snapshot. gen is not
// part of the state, and ring.Departed is not rewindable, so neither is compared.
func (s *Service) stateEqualLocked(prev stateSnapshot) bool {
	return ringEqual(s.ring, prev.ring) &&
		s.lobby == prev.lobby &&
		slices.Equal(s.claimed, prev.claimed) &&
		s.board == prev.board &&
		s.ph == prev.ph &&
		s.winner == prev.winner &&
		s.draw == prev.draw &&
		s.last == prev.last &&
		slices.Equal(s.history, prev.history)
}

func ringEqual(a, b game.Ring) bool {
	if a.Turn != b.Turn || a.Max != b.Max || a.Games != b.Games ||
		!slices.Equal(a.Seats, b.Seats) || !slices.Equal(a.Gone, b.Gone) {
		return false
	}
	return true
}

// rollback undoes a local mutation whose broadcast failed, unless something else
// wrote state meanwhile (gen changed) — in which case the newer state wins.
func (s *Service) rollback(prev stateSnapshot, gen uint64) {
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return // a peer frame, leave, snapshot or promotion already wrote state
	}
	s.restoreLocked(prev)
	s.mu.Unlock()
	s.emitState() // the UI must not keep showing a move that never left
}

func cloneRing(r game.Ring) game.Ring {
	r.Seats = append([]wire.ParticipantID(nil), r.Seats...)
	r.Gone = append([]bool(nil), r.Gone...)
	r.Departed = nil // monotone fact set: snapshotted and restored by nobody
	return r
}

func cloneHistory(h []string) []string {
	if h == nil {
		return nil
	}
	return append([]string(nil), h...)
}

// --- lobby: open / seat / begin ---------------------------------------------

// Start opens a table (host) or asks the host to open one (non-host).
func (s *Service) Start() error {
	if !s.Ctx().Host {
		return s.sendToHost(kindStartReq)
	}
	return s.hostOpen()
}

// hostOpen enters the seating lobby. It carries the claimed list across a
// rematch (leavers already dropped); a fresh table seeds the host into seat 0.
func (s *Service) hostOpen() error {
	s.mu.Lock()
	if s.ph == game.Playing {
		s.mu.Unlock()
		return errInProgress
	}
	prev := s.saveLocked()
	s.clearBoardLocked()
	s.ph = game.Idle
	s.lobby = true
	if len(s.claimed) == 0 {
		s.claimed = []wire.ParticipantID{s.Ctx().Self}
	}
	s.claimed = removeDeparted(s.claimed, s.ring.Departed)
	seats := idsToU32(s.claimed)
	gen := s.writeLocked(prev)
	s.mu.Unlock()
	return s.broadcastWithRollback(kindOpen, seats, prev, gen)
}

// TakeSeat / LeaveSeat claim or release this end's seat in the open lobby.
func (s *Service) TakeSeat() error {
	if !s.Ctx().Host {
		return s.sendToHost(kindSeatReq)
	}
	return s.hostClaim(s.Ctx().Self)
}

func (s *Service) LeaveSeat() error {
	if !s.Ctx().Host {
		return s.sendToHost(kindLeaveReq)
	}
	return s.hostRelease(s.Ctx().Self)
}

func (s *Service) hostClaim(id wire.ParticipantID) error {
	s.mu.Lock()
	if !s.lobby {
		s.mu.Unlock()
		return errNotLobby
	}
	// A claim that changes nothing must be a complete no-op: no snapshot, no gen
	// bump, no broadcast. Otherwise a redundant claim (a double-clicked seat, a
	// retried request, or this end already being seated) bumps gen while another
	// goroutine's broadcast is in flight, and that broadcast's rollback then
	// declines to run because it thinks a peer wrote state. A spurious gen bump
	// suppresses a rollback that was needed, which is just as bad as the missing
	// bump this whole protocol exists to prevent.
	if len(s.claimed) >= MaxSeats || containsID(s.claimed, id) {
		s.mu.Unlock()
		return nil
	}
	prev := s.saveLocked()
	s.claimed = append(s.claimed, id)
	seats := idsToU32(s.claimed)
	gen := s.writeLocked(prev)
	s.mu.Unlock()
	return s.broadcastWithRollback(kindSeats, seats, prev, gen)
}

func (s *Service) hostRelease(id wire.ParticipantID) error {
	s.mu.Lock()
	if !s.lobby {
		s.mu.Unlock()
		return errNotLobby
	}
	// Same rule as hostClaim: releasing a seat this end does not hold changes
	// nothing, so it must not bump gen or put a redundant frame on the wire.
	if !containsID(s.claimed, id) {
		s.mu.Unlock()
		return nil
	}
	prev := s.saveLocked()
	s.claimed = removeID(s.claimed, id)
	seats := idsToU32(s.claimed)
	gen := s.writeLocked(prev)
	s.mu.Unlock()
	return s.broadcastWithRollback(kindSeats, seats, prev, gen)
}

// Begin starts play from the claimed seats (host only, ≥2 seated).
//
// Seats are installed locally then broadcast, so a failed broadcast is rolled
// back — otherwise this end would sit in a game no peer ever learned about.
func (s *Service) Begin() error {
	s.mu.Lock()
	if !s.Ctx().Host {
		s.mu.Unlock()
		return errNotHost
	}
	if !s.lobby || len(s.claimed) < 2 {
		s.mu.Unlock()
		return errTooFew
	}
	prev := s.saveLocked()
	// Nobody who has left the session may be installed as a live seat. The lobby
	// list is pruned on every leave, so this only matters for a leave that raced
	// the list itself — exactly the case that used to hand the new host a seat
	// list naming the departed host, with the opening turn pointed at it.
	seats := removeDeparted(s.claimed, s.ring.Departed)
	if len(seats) < 2 {
		s.mu.Unlock()
		return errTooFew
	}
	// Keep the lobby list identical to what is being installed and broadcast, so
	// this end cannot keep naming a departed claimant the peers never learned
	// about.
	s.claimed = append([]wire.ParticipantID(nil), seats...)
	turn := s.ring.Games % len(seats)
	s.ring.SetSeats(seats, turn)
	s.ring.Games++
	s.resetLocked()
	s.applyDepartedLocked()
	gen := s.writeLocked(prev)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindNewGame, Seats: idsToU32(seats), Turn: uint8(turn)})
	if err != nil {
		s.rollback(prev, gen)
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		s.rollback(prev, gen)
		return err
	}
	s.emitState()
	return nil
}

// --- play -------------------------------------------------------------------

func (s *Service) notePlaceLocked(seat int, row, col int8) {
	glyph := "⚫"
	if seat >= 0 && seat < MaxSeats {
		glyph = stoneGlyph[seat]
	}
	s.history = append(s.history, fmt.Sprintf("%s %c%d", glyph, 'a'+col, row+1))
}

// Place plays the local player's stone at (row, col).
//
// The move is applied locally BEFORE broadcasting (peers only advance when the
// frame arrives), so a failed broadcast is rolled back rather than left to
// desync the game — see the broadcast rollback block.
func (s *Service) Place(row, col int8) error {
	s.mu.Lock()
	seat, err := s.checkTurnLocked(s.Ctx().Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	prev := s.saveLocked()
	if _, err := s.placeLocked(seat, row, col); err != nil {
		s.mu.Unlock()
		return err
	}
	hash := s.applyAndHashLocked()
	gen := s.writeLocked(prev)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindPlace, Row: row, Col: col, StateHash: hash})
	if err != nil {
		s.rollback(prev, gen)
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		s.rollback(prev, gen)
		return err
	}
	s.emitState()
	return nil
}

// placeLocked writes the stone and records notation; shared by send/receive.
func (s *Service) placeLocked(seat int, row, col int8) (int16, error) {
	idx, err := s.board.Place(row, col, int8(seat)+1)
	if err != nil {
		return 0, err
	}
	s.last = idx
	s.notePlaceLocked(seat, row, col)
	return idx, nil
}

// Resign forfeits the local player's seat; the game ends (sole survivor wins,
// otherwise abandoned). Rolled back if the broadcast fails.
func (s *Service) Resign() error {
	s.mu.Lock()
	if _, seated := s.ring.SideOf(s.Ctx().Self); !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return ErrNoResign
	}
	prev := s.saveLocked()
	winnerSeat, over := s.ring.Concede(s.Ctx().Self)
	if over {
		s.endLocked(winnerSeat)
	}
	gen := s.writeLocked(prev)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindResign})
	if err != nil {
		s.rollback(prev, gen)
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		s.rollback(prev, gen)
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("gomokup: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		return s.handleNewGame(from, m)
	case kindStartReq:
		if s.Ctx().Host {
			return s.hostOpen()
		}
		return nil
	case kindSeatReq:
		if s.Ctx().Host {
			return s.hostClaim(from)
		}
		return nil
	case kindLeaveReq:
		if s.Ctx().Host {
			return s.hostRelease(from)
		}
		return nil
	case kindOpen:
		return s.handleOpen(from, m)
	case kindSeats:
		return s.handleSeats(from, m)
	case kindPlace:
		return s.handlePlace(from, m)
	case kindResign:
		return s.handleResign(from)
	}
	return fmt.Errorf("gomokup: unknown message kind %d", m.Kind)
}

// --- inbound frames ----------------------------------------------------------
//
// Every handler below writes state from the mux goroutine, interleaved with the
// UI goroutine's local actions. They share one rollback protocol, so each one
// takes a snapshot, mutates, and calls writeLocked before releasing the lock —
// otherwise a local broadcast that is in flight on the other goroutine could
// roll the peer's write away. Handlers that change nothing still emit state
// (the UI may be waiting on it) but do not bump gen.

func (s *Service) handleNewGame(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return fmt.Errorf("gomokup: new game from non-host %d", from)
	}
	s.mu.Lock()
	prev := s.saveLocked()
	// The host's seat list can still name a player whose leave reached this end
	// first (the relay orders frames per sender only). The seats are installed
	// verbatim — seat index IS the colour and decides whose turn it is, so
	// dropping an entry here would renumber every seat after it and diverge from
	// the host. Instead applyDepartedLocked marks the departed seats Gone right
	// after, which is what makes a raced newGame unable to put someone who has
	// left back into play.
	s.ring.SetSeats(u32ToIDs(m.Seats), int(m.Turn))
	s.resetLocked()
	s.applyDepartedLocked()
	s.writeLocked(prev)
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleOpen(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return nil
	}
	s.mu.Lock()
	prev := s.saveLocked()
	s.clearBoardLocked()
	s.ph = game.Idle
	s.lobby = true
	s.claimed = removeDeparted(u32ToIDs(m.Seats), s.ring.Departed)
	s.writeLocked(prev)
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleSeats(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return nil
	}
	s.mu.Lock()
	prev := s.saveLocked()
	s.claimed = removeDeparted(u32ToIDs(m.Seats), s.ring.Departed)
	s.writeLocked(prev)
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handlePlace(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	seat, err := s.checkTurnLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	prev := s.saveLocked()
	if _, err := s.placeLocked(seat, m.Row, m.Col); err != nil {
		s.mu.Unlock()
		return err
	}
	hash := s.applyAndHashLocked()
	ok := bytes.Equal(hash, m.StateHash)
	if !ok {
		s.ph = game.Over
	}
	s.writeLocked(prev)
	s.mu.Unlock()
	if !ok {
		return errors.New("gomokup: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	if _, seated := s.ring.SideOf(from); !seated || s.ph != game.Playing {
		s.mu.Unlock()
		return nil
	}
	prev := s.saveLocked()
	winnerSeat, over := s.ring.Concede(from)
	if over {
		s.endLocked(winnerSeat)
	}
	s.writeLocked(prev)
	s.mu.Unlock()
	s.emitState()
	return nil
}

// endLocked marks the game over with the given winning seat (-1 = abandoned).
func (s *Service) endLocked(winnerSeat int) {
	s.ph = game.Over
	s.lobby = false
	if winnerSeat >= 0 {
		s.winner = int8(winnerSeat) + 1
	} else {
		s.winner = 0
	}
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ph == game.Idle && !s.lobby {
		return nil, nil
	}
	return wire.Marshal(snapshot{
		Board: s.board, Seats: idsToU32(s.ring.Seats), Turn: uint8(s.ring.Turn),
		Phase: uint8(s.ph), Winner: s.winner, Draw: s.draw, Last: s.last,
		History: s.history, Gone: s.ring.Gone, Lobby: s.lobby, Claimed: idsToU32(s.claimed),
	})
}

// Restore installs a late joiner's catch-up snapshot.
//
// The snapshot is a peer's view, captured at some earlier instant, and a session
// leave this end already saw has no ordering relationship with it — the leave can
// arrive first. So the entire restore is filtered through ring.Departed: a
// departed player is never written back into the lobby list, never installed as a
// live seat, and never left holding the turn (see applyDepartedLocked).
func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("gomokup: restore: %w", err)
	}
	s.mu.Lock()
	if s.ph != game.Idle || s.lobby { // late-joiner catch-up only
		s.mu.Unlock()
		return nil
	}
	prev := s.saveLocked()
	s.claimed = removeDeparted(u32ToIDs(snap.Claimed), s.ring.Departed)
	if snap.Lobby {
		s.lobby = true
		s.ph = game.Idle
		s.writeLocked(prev)
		s.mu.Unlock()
		s.emitState()
		return nil
	}
	s.board = snap.Board
	// Seats are installed verbatim for the same reason as handleNewGame (seat
	// index is the colour and the turn); applyDepartedLocked below marks any
	// departed seat Gone instead of renumbering the ring.
	s.ring.SetSeats(u32ToIDs(snap.Seats), int(snap.Turn))
	if len(snap.Gone) == len(s.ring.Gone) {
		copy(s.ring.Gone, snap.Gone)
	}
	s.ph = game.Phase(snap.Phase)
	s.winner = snap.Winner
	s.draw = snap.Draw
	s.last = snap.Last
	s.history = snap.History
	s.applyDepartedLocked()
	s.writeLocked(prev)
	s.mu.Unlock()
	s.emitState()
	return nil
}

// State returns the current game state for UI pulls.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

// --- internals ---------------------------------------------------------------

// clearBoardLocked resets the board (not the phase or lobby flags).
func (s *Service) clearBoardLocked() {
	s.board = Board{}
	s.winner = 0
	s.draw = false
	s.last = -1
	s.history = nil
}

// resetLocked begins a fresh game: board cleared, playing, lobby closed. The
// ring's Seats/Turn/Gone were set just before (Begin host / handleNewGame joiner).
func (s *Service) resetLocked() {
	s.clearBoardLocked()
	s.ph = game.Playing
	s.lobby = false
}

// applyDepartedLocked ends a just-installed live game whose seat list still
// names players that already left the session — their leave has no cross-
// sender ordering against the newGame/snapshot that carried the seats, so it
// can arrive first and find no ring to mark (see Ring.Departed).
func (s *Service) applyDepartedLocked() {
	if s.ph != game.Playing {
		return
	}
	if winnerSeat, over := s.ring.ApplyDeparted(); over {
		s.endLocked(winnerSeat)
	}
}

func (s *Service) checkTurnLocked(who wire.ParticipantID) (int, error) {
	if s.ph != game.Playing {
		return 0, ErrNoGame
	}
	seat, seated := s.ring.SideOf(who)
	if !seated {
		return 0, ErrNotSeat
	}
	if seat != s.ring.Turn {
		return 0, ErrNotTurn
	}
	return seat, nil
}

// applyAndHashLocked resolves the outcome after a placement, then hashes the
// post-move state — computed identically on send and receive.
func (s *Service) applyAndHashLocked() []byte {
	if w, _ := s.board.Winner(); w != 0 {
		s.winner = w
		s.ph = game.Over
	} else if s.board.Full() {
		s.draw = true
		s.ph = game.Over
	} else {
		s.ring.NextTurn()
	}
	b, err := wire.Marshal(struct {
		Board Board `cbor:"1,keyasint"`
		Turn  uint8 `cbor:"2,keyasint"`
		Phase uint8 `cbor:"3,keyasint"`
	}{s.board, uint8(s.ring.Turn), uint8(s.ph)})
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	return sum[:8]
}

func (s *Service) emitState() {
	s.mu.Lock()
	st := s.stateLocked()
	s.mu.Unlock()
	s.Ctx().Emit(st)
}

func (s *Service) stateLocked() State {
	if s.ph == game.Idle && !s.lobby {
		return State{Last: -1, MaxSeats: MaxSeats}
	}
	st := State{MaxSeats: MaxSeats, Last: s.last, History: append([]string(nil), s.history...)}
	if s.lobby {
		st.Lobby = true
		st.Seats = append([]wire.ParticipantID(nil), s.claimed...)
		st.CanBegin = s.Ctx().Host && len(s.claimed) >= 2
		return st
	}
	st.Playing = true
	st.Board = s.board
	st.Seats = append([]wire.ParticipantID(nil), s.ring.Seats...)
	st.Gone = append([]bool(nil), s.ring.Gone...)
	switch {
	case s.ph == game.Over && s.draw:
		st.Outcome = "draw"
	case s.ph == game.Over && s.winner > 0:
		st.WinnerID = s.ring.IDOf(int(s.winner) - 1)
		st.Outcome = fmt.Sprintf("seat %d wins", s.winner)
	case s.ph == game.Over:
		st.Outcome = "abandoned"
	default:
		st.TurnID = s.ring.IDOf(s.ring.Turn)
	}
	if _, cells := s.board.Winner(); cells != nil {
		st.WinCells = cells
	}
	return st
}

// --- message + slice helpers -------------------------------------------------

func (s *Service) sendToHost(kind uint8) error {
	body, err := wire.Marshal(msg{Kind: kind})
	if err != nil {
		return err
	}
	return s.Ctx().Send.SendTo(s.Ctx().HostID, ID, body)
}

func (s *Service) broadcast(kind uint8, seats []uint32) error {
	body, err := wire.Marshal(msg{Kind: kind, Seats: seats})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// broadcastWithRollback sends a lobby seat list and restores `prev` if the send
// fails, so the host's claimed list cannot drift ahead of what joiners were told.
func (s *Service) broadcastWithRollback(kind uint8, seats []uint32, prev stateSnapshot, gen uint64) error {
	body, err := wire.Marshal(msg{Kind: kind, Seats: seats})
	if err != nil {
		s.rollback(prev, gen)
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		s.rollback(prev, gen)
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) broadcastSeats(seats []uint32) error { return s.broadcast(kindSeats, seats) }

func idsToU32(ids []wire.ParticipantID) []uint32 {
	out := make([]uint32, len(ids))
	for i, id := range ids {
		out[i] = uint32(id)
	}
	return out
}

func u32ToIDs(us []uint32) []wire.ParticipantID {
	out := make([]wire.ParticipantID, len(us))
	for i, u := range us {
		out[i] = wire.ParticipantID(u)
	}
	return out
}

func containsID(ids []wire.ParticipantID, id wire.ParticipantID) bool {
	return slices.Contains(ids, id)
}

func removeID(ids []wire.ParticipantID, id wire.ParticipantID) []wire.ParticipantID {
	for i, x := range ids {
		if x == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// removeDeparted drops every participant already known to have left the session.
// It always returns a fresh slice: the input is frequently s.claimed itself, and
// removeID's in-place compaction would otherwise mutate the caller's list (and,
// through a shared backing array, an outstanding saveLocked snapshot of it).
func removeDeparted(ids []wire.ParticipantID, departed map[wire.ParticipantID]bool) []wire.ParticipantID {
	out := make([]wire.ParticipantID, 0, len(ids))
	for _, id := range ids {
		if !departed[id] {
			out = append(out, id)
		}
	}
	return out
}
