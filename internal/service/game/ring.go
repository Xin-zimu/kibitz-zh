package game

import "github.com/richardwooding/parley/wire"

// Ring is the N-player turn runtime — an ordered seat list with a rotating turn
// — for games seating 2..Max players. It is the multi-seat sibling of Table
// (which stays two-player); the twelve existing games keep using Table. Logic
// only — no Sender, no mutex, no I/O; callers hold their own lock.
//
// Seating (who sits, in what order) is decided by the owning service's lobby
// (host-authoritative) and installed here via SetSeats; the twelve two-player
// games keep using Table. Ring only tracks the live game: whose turn it is,
// which seats have left, and rotation.
type Ring struct {
	Seats []wire.ParticipantID // seated players this game; index = seat number
	Gone  []bool               // per-seat: left/resigned mid-game (turn skips it)
	Turn  int                  // index into Seats whose turn it is
	Max   int                  // seat cap
	Games int                  // completed games; rotates who opens each rematch
	// Departed remembers every participant whose session leave this end has
	// seen. The relay orders frames per sender only, so a leave can overtake a
	// newGame or snapshot already in flight from the host — the seat list then
	// arrives still naming the departed player. ApplyDeparted re-checks a
	// freshly installed seat list against this set. Participant IDs are never
	// reused within a session, so a recorded departure is final.
	Departed map[wire.ParticipantID]bool
}

// NewRing returns a Ring that seats up to max players.
func NewRing(max int) Ring { return Ring{Max: max} }

// SetSeats installs the seat list + opening turn for a new game (from the
// host's authoritative Begin). Gone is cleared to match a fresh game.
func (r *Ring) SetSeats(seats []wire.ParticipantID, turn int) {
	r.Seats = append([]wire.ParticipantID(nil), seats...)
	r.Gone = make([]bool, len(seats))
	if turn < 0 || turn >= len(seats) {
		turn = 0
	}
	r.Turn = turn
}

// SideOf reports which seat a participant holds this game.
func (r *Ring) SideOf(id wire.ParticipantID) (int, bool) {
	for i, s := range r.Seats {
		if s == id {
			return i, true
		}
	}
	return 0, false
}

// IDOf returns the participant in a seat (0 if out of range).
func (r *Ring) IDOf(seat int) wire.ParticipantID {
	if seat < 0 || seat >= len(r.Seats) {
		return 0
	}
	return r.Seats[seat]
}

// NextTurn advances the turn to the next still-active (not Gone) seat.
func (r *Ring) NextTurn() {
	for i := 0; i < len(r.Seats); i++ {
		r.Turn = (r.Turn + 1) % len(r.Seats)
		if !r.Gone[r.Turn] {
			return
		}
	}
}

// ActiveCount is how many seated players have not left.
func (r *Ring) ActiveCount() int {
	n := 0
	for _, g := range r.Gone {
		if !g {
			n++
		}
	}
	return n
}

func (r *Ring) lastActive() int {
	for i, g := range r.Gone {
		if !g {
			return i
		}
	}
	return 0
}

// NoteLeft handles a departure during a live game. A seated player's leave
// marks their seat Gone; the game CONTINUES with the remaining players as long
// as at least two of them are still active. It ends only when fewer than two
// active seats remain — the sole survivor wins, or the game is abandoned when
// nobody is left.
//
// Upstream kibitz ended the game on ANY leave, to dodge the ordering hazard
// below. That is unnecessarily harsh for 3- and 4-player games: one player
// closing a tab killed the game for everyone still connected.
//
// The ordering hazard, and why continuing is still sound:
//
// The relay orders frames per sender only, so a membership leave and an
// in-flight move can be observed in either order on different ends. The
// dangerous shape is a leave whose seat is ALSO the current turn holder: one
// end might apply the move first (board gains a stone, turn advances) while
// another processes the leave first and then has to reject the move as
// not-your-turn. Eagerly advancing the turn past a Gone seat is what makes that
// asymmetric.
//
// Continuing is therefore safe precisely when the leaver does NOT hold the turn
// — then the leave changes no turn state at all, and every end converges
// regardless of order. When the leaver DOES hold the turn, there is no
// order-independent answer, and degrading to a finished game is strictly better
// than leaving a live game whose turn points at a seat nobody occupies (which
// would hang every survivor forever). See markGone.
func (r *Ring) NoteLeft(id wire.ParticipantID, ph Phase) (int, bool) {
	if r.Departed == nil {
		r.Departed = map[wire.ParticipantID]bool{}
	}
	r.Departed[id] = true
	if ph != Playing {
		return -1, false
	}
	return r.markGone(id)
}

// ApplyDeparted marks Gone any seat whose player already left the session
// (see Departed) and applies the same continue-or-finish rule as NoteLeft.
// Returns (winnerSeat, true) when the game must end, (-1, false) when it
// continues or when no seat was newly marked. Call it after installing a seat
// list (SetSeats from a newGame or snapshot).
func (r *Ring) ApplyDeparted() (int, bool) {
	marked := false
	for i, id := range r.Seats {
		if !r.Gone[i] && r.Departed[id] {
			r.Gone[i] = true
			marked = true
		}
	}
	if !marked {
		return -1, false
	}
	return r.settle()
}

// Concede ends a live game for a seated player who voluntarily forfeits — same
// continue-or-finish semantics as a mid-game leave.
func (r *Ring) Concede(id wire.ParticipantID) (int, bool) {
	return r.markGone(id)
}

// markGone marks the seat Gone and reports whether the game must end.
//
// The decision is deliberately conservative, because the relay only orders
// frames per sender: a leave and an in-flight move can be observed in different
// orders on different ends, and any rule that makes the turn depend on that
// order corrupts the game.
//
//   - Fewer than two active seats left: end. A sole survivor wins; with nobody
//     left it is abandoned. This is order-independent — it depends only on the
//     seat list and the departed set, which every end shares.
//   - The leaver holds the turn: end as abandoned. Here there is NO
//     order-independent answer. The leaver may have just broadcast a move, so
//     one end can apply it (stone placed, turn advanced) while another handles
//     the leave first, advances the turn past the dead seat, and then rejects
//     that same move as not-your-turn. The two ends would disagree on the
//     board. Ending in both orders keeps every end agreed on the outcome; only
//     the cosmetic board of an already-finished game can differ.
//   - Otherwise: play on with the remaining players. This is the case worth
//     improving: the leaver was not on turn, so they could not legally have a
//     move in flight (a move from them is refused as not-your-turn in BOTH
//     orders), the leave touches no turn state, and every end converges. One
//     player closing a tab in a 3- or 4-player game no longer kills it for the
//     people still playing.
func (r *Ring) markGone(id wire.ParticipantID) (int, bool) {
	seat, seated := r.SideOf(id)
	if !seated || r.Gone[seat] {
		return -1, false
	}
	r.Gone[seat] = true
	return r.settle()
}

// settle decides the outcome after seats were marked Gone. It must be called
// with the Gone set already updated and must not itself mutate turn state.
func (r *Ring) settle() (int, bool) {
	switch r.ActiveCount() {
	case 0:
		return -1, true // nobody left: abandoned
	case 1:
		return r.lastActive(), true // sole survivor wins
	}
	if r.Turn >= 0 && r.Turn < len(r.Gone) && r.Gone[r.Turn] {
		return -1, true // turn holder left: end (see markGone)
	}
	return -1, false // two or more active, turn intact: play on
}

// OnPromote resets rematch bookkeeping when this end is promoted to host
// (migration).
func (r *Ring) OnPromote() { r.Games = 0 }
