// Package chess is the first game service, wrapping corentings/chess for rules.
// Sync is both-sides-validate: every client applies every move through the
// same engine and checks a position hash — there is no authoritative server,
// because the relay can't be one (it only ever sees ciphertext).
package chess

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	chesslib "github.com/corentings/chess/v2"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/game"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

const ID = "chess"

const (
	kindMove           uint8 = 1
	kindResign         uint8 = 2
	kindOfferDraw      uint8 = 3
	kindAgreeDraw      uint8 = 4
	kindNewGame        uint8 = 5
	kindStartReq       uint8 = 6 // player → host: please start/rematch
	kindTakebackOffer  uint8 = 7
	kindTakebackAccept uint8 = 8
)

type msg struct {
	Kind uint8 `cbor:"1,keyasint"`
	// UCI move (e2e4, e7e8q) for kindMove.
	UCI string `cbor:"2,keyasint,omitempty"`
	// StateHash is SHA-256(FEN)[:8] AFTER the move — the desync tripwire.
	StateHash []byte `cbor:"3,keyasint,omitempty"`
	// WhiteID for kindNewGame.
	WhiteID uint32 `cbor:"4,keyasint,omitempty"`
	BlackID uint32 `cbor:"5,keyasint,omitempty"`
}

type snapshot struct {
	PGN       string `cbor:"1,keyasint"`
	WhiteID   uint32 `cbor:"2,keyasint"`
	BlackID   uint32 `cbor:"3,keyasint"`
	LastUCI   string `cbor:"4,keyasint,omitempty"`
	DrawnFrom uint32 `cbor:"5,keyasint,omitempty"`
	// Prev holds the pre-move state so a late joiner inherits the takeback stash.
	Prev []byte `cbor:"6,keyasint,omitempty"`
}

// State is emitted after every game change; the UI renders it directly.
type State struct {
	FEN     string
	WhiteID wire.ParticipantID
	BlackID wire.ParticipantID
	TurnID  wire.ParticipantID // 0 when the game is over or not started
	Outcome string             // "*", "1-0", "0-1", "1/2-1/2"
	Method  string             // "Checkmate", "Resignation", …
	LastUCI string
	Playing bool     // a game exists (start conditions met)
	History []string // SAN move list, derived from the engine's move tree
	PGN     string   // full PGN, for one-click export
	// CanTakeback: this end made the last move and may offer to undo it.
	// TakebackBy: participant who has offered a takeback (0 = none).
	CanTakeback bool
	TakebackBy  wire.ParticipantID
}

// DrawOffered is emitted when the opponent offers a draw.
type DrawOffered struct{ From wire.ParticipantID }

// Desync is emitted when a peer's move or state hash disagrees with the
// local engine — the game cannot safely continue.
type Desync struct {
	From   wire.ParticipantID
	Reason string
}

var (
	ErrNotPlayer = errors.New("chess: you are not a player in this game")
	ErrNotTurn   = errors.New("chess: not your turn")
	ErrNoGame    = errors.New("chess: no game in progress")
)

// Service implements service.Service. HandleFrame/Snapshot/Restore run on
// the mux goroutine; TryMove/Resign/OfferDraw/LegalTargets/Start come from
// the UI layer — the mutex covers game state.
type Service struct {
	service.Base

	mu        sync.Mutex
	table     game.Table
	game      *chesslib.Game
	whiteID   wire.ParticipantID
	blackID   wire.ParticipantID
	lastUCI   string
	drawnFrom wire.ParticipantID // pending draw offer

	prevSnap []byte             // serialized pre-move state (1-level takeback)
	offerBy  wire.ParticipantID // who offered a takeback (0 = none)
}

func New() *Service { return &Service{} }

func (s *Service) ID() string   { return ID }
func (s *Service) Version() int { return 1 }

func (s *Service) Attach(ctx service.Context) { s.SetContext(ctx) }

// OnPromote resets host-only seat bookkeeping when this end is promoted to host
// (migration); the next joiner re-seeds the opponent via NoteKeyed.
func (s *Service) OnPromote() {
	s.mu.Lock()
	s.table.OnPromote()
	s.mu.Unlock()
}

// MemberKeyed (host side) records the seated player; games start on demand
// via Start().
func (s *Service) MemberKeyed(id wire.ParticipantID, role session.Role) {
	if !s.Ctx().Host {
		return
	}
	s.mu.Lock()
	s.table.NoteKeyed(id, role)
	s.mu.Unlock()
}

// Start launches a game (or a rematch, once the previous game is over).
// On the host it seats players — white alternates each game — and
// broadcasts newGame; on a player it asks the host via startReq.
func (s *Service) Start() error {
	if !s.Ctx().Host {
		body, err := wire.Marshal(msg{Kind: kindStartReq})
		if err != nil {
			return err
		}
		return s.Ctx().Send.SendTo(s.Ctx().HostID, ID, body)
	}
	return s.hostStart(s.Ctx().Self)
}

// hostStart validates and launches; from is who asked (host or seated player).
func (s *Service) hostStart(from wire.ParticipantID) error {
	s.mu.Lock()
	if err := s.table.AuthorizeStart(s.Ctx().Host, from, s.Ctx().Self, s.phaseLocked()); err != nil {
		s.mu.Unlock()
		return err
	}
	seats := s.table.NextSeats(s.Ctx().Self)
	s.game = chesslib.NewGame()
	s.whiteID = seats.P1 // P1 = white (moves first)
	s.blackID = seats.P2
	s.lastUCI = ""
	s.drawnFrom = 0
	s.prevSnap = nil
	s.offerBy = 0
	s.applyDepartedLocked()
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindNewGame, WhiteID: uint32(seats.P1), BlackID: uint32(seats.P2)})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// phaseLocked maps chess state onto the shared lifecycle.
func (s *Service) phaseLocked() game.Phase {
	switch {
	case s.game == nil:
		return game.Idle
	case s.game.Outcome() == chesslib.NoOutcome:
		return game.Playing
	default:
		return game.Over
	}
}

// forfeitLocked ends the live game by resigning the departed side's color —
// the opponent walked away mid-game, so they forfeit.
func (s *Service) forfeitLocked(winner game.Side) {
	if winner == game.P2 { // white (P1) left
		s.game.Resign(chesslib.White)
	} else {
		s.game.Resign(chesslib.Black)
	}
}

// applyDepartedLocked ends a just-installed live game whose seat pair still
// names a player that already left the session — the leave can overtake the
// newGame or snapshot that carried the seats (the relay orders frames per
// sender only; see Table.Departed).
func (s *Service) applyDepartedLocked() {
	if s.phaseLocked() != game.Playing {
		return
	}
	if winner, forfeit := s.table.ApplyDeparted(); forfeit {
		s.forfeitLocked(winner)
	}
}

func (s *Service) MemberLeft(id wire.ParticipantID) {
	s.mu.Lock()
	winner, forfeit := s.table.NoteLeft(id, s.phaseLocked())
	if forfeit {
		s.forfeitLocked(winner)
	}
	s.mu.Unlock()
	if forfeit {
		s.emitState()
	}
}

// TryMove validates and broadcasts the local player's move (UCI: e2e4, e7e8q).
func (s *Service) TryMove(uci string) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if err := s.checkTurnLocked(s.Ctx().Self); err != nil {
		s.mu.Unlock()
		return err
	}
	prev, _ := s.snapshotBlobLocked(nil) // pre-move state; committed only on a valid move
	move, err := chesslib.UCINotation{}.Decode(s.game.Position(), uci)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("chess: bad move %q: %w", uci, err)
	}
	if err := s.game.Move(move, nil); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("chess: illegal move %q: %w", uci, err)
	}
	s.prevSnap = prev
	s.offerBy = 0
	s.lastUCI = uci
	s.drawnFrom = 0
	hash := positionHash(s.game)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindMove, UCI: uci, StateHash: hash})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// Resign concedes the local player's game.
func (s *Service) Resign() error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	color, err := s.colorOfLocked(s.Ctx().Self)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.game.Resign(color)
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindResign})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// OfferDraw proposes a draw; AgreeDraw accepts a pending offer.
func (s *Service) OfferDraw() error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if _, err := s.colorOfLocked(s.Ctx().Self); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindOfferDraw})
	if err != nil {
		return err
	}
	return s.Ctx().Send.Broadcast(ID, body)
}

func (s *Service) AgreeDraw() error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if s.drawnFrom == 0 {
		s.mu.Unlock()
		return errors.New("chess: no draw offer pending")
	}
	if _, err := s.colorOfLocked(s.Ctx().Self); err != nil {
		s.mu.Unlock()
		return err
	}
	_ = s.game.Draw(chesslib.DrawOffer)
	s.drawnFrom = 0
	s.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindAgreeDraw})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// LegalTargets returns destination squares for the piece on from ("e2") —
// the UI's move-highlighting query.
func (s *Service) LegalTargets(from string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.game == nil {
		return nil
	}
	var out []string
	for _, m := range s.game.ValidMoves() {
		if m.S1().String() == from {
			out = append(out, m.S2().String())
		}
	}
	return out
}

// LegalMoves returns every legal move in UCI (e.g. "e2e4", "e7e8q") — the solo
// bot picks one to reply with. Encoded the same way TryMove decodes it.
func (s *Service) LegalMoves() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.game == nil {
		return nil
	}
	moves := s.game.ValidMoves()
	enc := chesslib.UCINotation{}
	out := make([]string, 0, len(moves))
	for i := range moves {
		out = append(out, enc.Encode(nil, &moves[i]))
	}
	return out
}

func (s *Service) HandleFrame(from wire.ParticipantID, body []byte) error {
	m, err := wire.Body[msg](body)
	if err != nil {
		return fmt.Errorf("chess: %w", err)
	}
	switch m.Kind {
	case kindNewGame:
		return s.handleNewGame(from, m)
	case kindStartReq:
		if !s.Ctx().Host {
			return nil // only the host seats players
		}
		return s.hostStart(from)
	case kindMove:
		return s.handleMove(from, m)
	case kindResign:
		return s.handleResign(from)
	case kindOfferDraw:
		return s.handleOfferDraw(from)
	case kindAgreeDraw:
		return s.handleAgreeDraw(from)
	case kindTakebackOffer:
		return s.handleTakebackOffer(from)
	case kindTakebackAccept:
		return s.handleTakebackAccept(from)
	}
	return fmt.Errorf("chess: unknown message kind %d", m.Kind)
}

func (s *Service) handleNewGame(from wire.ParticipantID, m msg) error {
	if from != s.Ctx().HostID {
		return fmt.Errorf("chess: new game from non-host %d", from)
	}
	s.mu.Lock()
	s.game = chesslib.NewGame()
	s.whiteID = wire.ParticipantID(m.WhiteID)
	s.blackID = wire.ParticipantID(m.BlackID)
	// Seats mirror on every client so forfeit detection works off-host too.
	s.table.Seats = game.Seats{P1: s.whiteID, P2: s.blackID}
	s.lastUCI = ""
	s.drawnFrom = 0
	s.prevSnap = nil
	s.offerBy = 0
	s.applyDepartedLocked()
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleMove(from wire.ParticipantID, m msg) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if err := s.checkTurnLocked(from); err != nil {
		s.mu.Unlock()
		s.Ctx().Emit(Desync{From: from, Reason: "move out of turn"})
		return err
	}
	prev, _ := s.snapshotBlobLocked(nil) // pre-move state; committed only on a valid move
	move, err := chesslib.UCINotation{}.Decode(s.game.Position(), m.UCI)
	if err == nil {
		err = s.game.Move(move, nil)
	}
	if err != nil {
		s.mu.Unlock()
		s.Ctx().Emit(Desync{From: from, Reason: fmt.Sprintf("illegal move %s", m.UCI)})
		return fmt.Errorf("chess: peer sent illegal move %q: %w", m.UCI, err)
	}
	s.prevSnap = prev
	s.offerBy = 0
	s.lastUCI = m.UCI
	s.drawnFrom = 0
	hash := positionHash(s.game)
	s.mu.Unlock()

	if !bytes.Equal(hash, m.StateHash) {
		s.Ctx().Emit(Desync{From: from, Reason: "position hash mismatch"})
		return errors.New("chess: position hash mismatch")
	}
	s.emitState()
	return nil
}

func (s *Service) handleResign(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	color, err := s.colorOfLocked(from)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.game.Resign(color)
	s.mu.Unlock()
	s.emitState()
	return nil
}

func (s *Service) handleOfferDraw(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if _, err := s.colorOfLocked(from); err != nil {
		s.mu.Unlock()
		return err
	}
	s.drawnFrom = from
	s.mu.Unlock()
	s.Ctx().Emit(DrawOffered{From: from})
	return nil
}

func (s *Service) handleAgreeDraw(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.game == nil {
		s.mu.Unlock()
		return ErrNoGame
	}
	if _, err := s.colorOfLocked(from); err != nil {
		s.mu.Unlock()
		return err
	}
	_ = s.game.Draw(chesslib.DrawOffer)
	s.drawnFrom = 0
	s.mu.Unlock()
	s.emitState()
	return nil
}

// OfferTakeback asks the opponent to undo this end's last move. Valid only for
// the last mover (it's the opponent's turn now) while a stashed move exists.
func (s *Service) OfferTakeback() error {
	s.mu.Lock()
	if !s.canOfferLocked(s.Ctx().Self) {
		s.mu.Unlock()
		return errors.New("chess: no takeback available")
	}
	s.offerBy = s.Ctx().Self
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindTakebackOffer})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

// canOfferLocked reports whether who may offer a takeback: they made the last
// move (so it's the opponent's turn), the game is still in progress ("*"), and
// a pre-move stash exists.
func (s *Service) canOfferLocked(who wire.ParticipantID) bool {
	if s.game == nil || s.game.Outcome() != chesslib.NoOutcome || s.prevSnap == nil {
		return false
	}
	color, err := s.colorOfLocked(who)
	if err != nil {
		return false
	}
	return s.game.Position().Turn() != color
}

func (s *Service) handleTakebackOffer(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.canOfferLocked(from) {
		s.offerBy = from
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// AcceptTakeback accepts a pending offer from the opponent and reverts one move.
func (s *Service) AcceptTakeback() error {
	s.mu.Lock()
	if s.offerBy == 0 || s.offerBy == s.Ctx().Self || s.phaseLocked() != game.Playing {
		s.mu.Unlock()
		return errors.New("chess: no takeback to accept")
	}
	s.revertLocked()
	s.mu.Unlock()
	body, err := wire.Marshal(msg{Kind: kindTakebackAccept})
	if err != nil {
		return err
	}
	if err := s.Ctx().Send.Broadcast(ID, body); err != nil {
		return err
	}
	s.emitState()
	return nil
}

func (s *Service) handleTakebackAccept(from wire.ParticipantID) error {
	s.mu.Lock()
	if s.offerBy != 0 && from != s.offerBy {
		s.revertLocked()
	}
	s.mu.Unlock()
	s.emitState()
	return nil
}

// revertLocked restores the pre-move state stashed in prevSnap (1-level undo):
// the corentings game is rebuilt from the pre-move PGN — rolling the move
// history, position, turn and outcome back by one — and the draw-offer/lastUCI
// are restored too. Every end reverts from its own identical stash.
func (s *Service) revertLocked() {
	if s.prevSnap == nil {
		return
	}
	snap, err := wire.Body[snapshot](s.prevSnap)
	if err != nil {
		return
	}
	g := chesslib.NewGame()
	// The stash taken before the very first move holds the start position, whose
	// PGN is just the result marker ("*" — empty games render as "*", not "").
	// UnmarshalText has no game to replay there and fails, which used to make a
	// first-move takeback a silent no-op: the offer was consumed, the move stayed
	// on the board and the turn never came back.
	if pgn := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(snap.PGN), "*")); pgn != "" {
		if err := g.UnmarshalText([]byte(snap.PGN)); err != nil {
			return
		}
	}
	s.game = g
	s.whiteID = wire.ParticipantID(snap.WhiteID)
	s.blackID = wire.ParticipantID(snap.BlackID)
	s.table.Seats = game.Seats{P1: s.whiteID, P2: s.blackID}
	s.lastUCI = snap.LastUCI
	s.drawnFrom = wire.ParticipantID(snap.DrawnFrom)
	s.prevSnap = snap.Prev
	s.offerBy = 0
}

func (s *Service) Snapshot() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.game == nil {
		return nil, nil
	}
	return s.snapshotBlobLocked(s.prevSnap)
}

// snapshotBlobLocked serializes the current game state. prev is embedded so a
// late joiner inherits the pre-move stash and can take part in a takeback; it is
// nil when stashing prevSnap itself, to avoid unbounded nesting. Requires game != nil.
func (s *Service) snapshotBlobLocked(prev []byte) ([]byte, error) {
	return wire.Marshal(snapshot{
		PGN:       s.game.String(),
		WhiteID:   uint32(s.whiteID),
		BlackID:   uint32(s.blackID),
		LastUCI:   s.lastUCI,
		DrawnFrom: uint32(s.drawnFrom),
		Prev:      prev,
	})
}

func (s *Service) Restore(blob []byte) error {
	snap, err := wire.Body[snapshot](blob)
	if err != nil {
		return fmt.Errorf("chess: restore: %w", err)
	}
	g := chesslib.NewGame()
	if err := g.UnmarshalText([]byte(snap.PGN)); err != nil {
		return fmt.Errorf("chess: restore PGN: %w", err)
	}
	s.mu.Lock()
	// Late-joiner catch-up only: a client with a live game saw everything
	// in the snapshot already (and may have moved since the host built it).
	if s.game != nil {
		s.mu.Unlock()
		return nil
	}
	s.game = g
	s.whiteID = wire.ParticipantID(snap.WhiteID)
	s.blackID = wire.ParticipantID(snap.BlackID)
	// Seats mirror on every client so forfeit detection works off-host too.
	s.table.Seats = game.Seats{P1: s.whiteID, P2: s.blackID}
	s.lastUCI = snap.LastUCI
	s.drawnFrom = wire.ParticipantID(snap.DrawnFrom)
	s.prevSnap = snap.Prev
	s.applyDepartedLocked()
	s.mu.Unlock()
	s.emitState()
	return nil
}

// State returns the current game state (for UI pulls; pushes come via Emit).
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *Service) emitState() {
	s.mu.Lock()
	st := s.stateLocked()
	s.mu.Unlock()
	s.Ctx().Emit(st)
}

// sanHistory renders the game's main line as a SAN list (e.g. "e4","Nf3","O-O")
// straight from the engine's move tree, so it's correct after a Restore too.
func sanHistory(g *chesslib.Game) []string {
	hist := g.MoveHistory()
	out := make([]string, 0, len(hist))
	for _, h := range hist {
		out = append(out, chesslib.AlgebraicNotation{}.Encode(h.PrePosition, h.Move))
	}
	return out
}

func (s *Service) stateLocked() State {
	if s.game == nil {
		return State{}
	}
	st := State{
		FEN:     s.game.FEN(),
		WhiteID: s.whiteID,
		BlackID: s.blackID,
		Outcome: s.game.Outcome().String(),
		Method:  s.game.Method().String(),
		LastUCI: s.lastUCI,
		Playing: true,
		History: sanHistory(s.game),
		PGN:     s.game.String(),
	}
	if s.game.Outcome() == chesslib.NoOutcome {
		if s.game.Position().Turn() == chesslib.White {
			st.TurnID = s.whiteID
		} else {
			st.TurnID = s.blackID
		}
	}
	st.CanTakeback = s.offerBy == 0 && s.canOfferLocked(s.Ctx().Self)
	st.TakebackBy = s.offerBy
	return st
}

func (s *Service) checkTurnLocked(who wire.ParticipantID) error {
	if s.game.Outcome() != chesslib.NoOutcome {
		return errors.New("chess: game is over")
	}
	color, err := s.colorOfLocked(who)
	if err != nil {
		return err
	}
	if s.game.Position().Turn() != color {
		return ErrNotTurn
	}
	return nil
}

func (s *Service) colorOfLocked(who wire.ParticipantID) (chesslib.Color, error) {
	switch who {
	case s.whiteID:
		return chesslib.White, nil
	case s.blackID:
		return chesslib.Black, nil
	default:
		return chesslib.NoColor, ErrNotPlayer
	}
}

func positionHash(g *chesslib.Game) []byte {
	sum := sha256.Sum256([]byte(g.FEN()))
	return sum[:8]
}
