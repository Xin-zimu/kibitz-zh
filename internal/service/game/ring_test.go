package game

import (
	"testing"

	"github.com/richardwooding/parley/wire"
)

func pid(n uint32) wire.ParticipantID { return wire.ParticipantID(n) }

func TestRingSetSeatsAndLookup(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 1)
	if len(r.Seats) != 3 || r.Turn != 1 {
		t.Fatalf("seats=%v turn=%d, want 3 seats, turn 1", r.Seats, r.Turn)
	}
	side, ok := r.SideOf(pid(3))
	if !ok || side != 2 {
		t.Fatalf("SideOf(3)=%d,%v want 2,true", side, ok)
	}
	if r.IDOf(0) != pid(1) {
		t.Fatalf("IDOf(0)=%d want 1", r.IDOf(0))
	}
	// Out-of-range turn is clamped to 0.
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 9)
	if r.Turn != 0 {
		t.Fatalf("turn=%d, want clamped 0", r.Turn)
	}
}

func TestRingNextTurnSkipsGone(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	r.Gone[1] = true // seat 1 left
	r.NextTurn()     // 0 -> skip 1 -> 2
	if r.Turn != 2 {
		t.Fatalf("turn=%d, want 2 (skipped gone seat 1)", r.Turn)
	}
	r.NextTurn() // 2 -> wrap to 0
	if r.Turn != 0 {
		t.Fatalf("turn=%d, want 0 (wrapped)", r.Turn)
	}
}

func TestRingNoteLeftForfeitWhenOneActive(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 0)
	winner, over := r.NoteLeft(pid(2), Playing)
	if !over || winner != 0 {
		t.Fatalf("2-player leave: winner=%d over=%v, want 0,true", winner, over)
	}
}

func TestRingNoteLeftEndsGameWithThree(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 1) // seat 1 on turn
	winner, over := r.NoteLeft(pid(2), Playing)                 // seat 1, the turn holder
	if !over || winner != -1 {
		t.Fatalf("3-player turn-holder leave: winner=%d over=%v, want -1,true", winner, over)
	}
	if !r.Gone[1] {
		t.Fatal("departed seat 1 should be marked Gone")
	}
	if r.ActiveCount() != 2 {
		t.Fatalf("active=%d, want 2", r.ActiveCount())
	}
}

// Upstream ended a 3-player game whenever anyone left. Gomoku Party now plays
// on when the leaver is not the turn holder: one player closing a tab must not
// kill the game for the people still connected.
func TestRingNoteLeftContinuesWhenOffTurn(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0) // seat 0 on turn
	winner, over := r.NoteLeft(pid(2), Playing)                 // seat 1 leaves
	if over || winner != -1 {
		t.Fatalf("off-turn leave with 3: %d,%v, want -1,false (play on)", winner, over)
	}
	if !r.Gone[1] {
		t.Fatalf("gone=%v, want seat 1 marked", r.Gone)
	}
	if r.Turn != 0 {
		t.Fatalf("turn=%d, want 0 (unchanged)", r.Turn)
	}
	if r.ActiveCount() != 2 {
		t.Fatalf("active=%d, want 2", r.ActiveCount())
	}
	// The remaining two must be able to finish the game normally.
	if winner, over := r.NoteLeft(pid(3), Playing); !over || winner != 0 {
		t.Fatalf("second leave: %d,%v, want sole survivor seat 0", winner, over)
	}
}

// The turn holder leaving has no order-independent resolution, so the game
// ends rather than hanging on a seat nobody occupies.
func TestRingNoteLeftEndsWhenTurnHolderLeaves(t *testing.T) {
	for _, tc := range []struct {
		name   string
		seats  []wire.ParticipantID
		turn   int
		leaver wire.ParticipantID
	}{
		{"3p turn-holder", []wire.ParticipantID{pid(1), pid(2), pid(3)}, 1, pid(2)},
		{"4p turn-holder", []wire.ParticipantID{pid(1), pid(2), pid(3), pid(4)}, 2, pid(3)},
		{"2p turn-holder", []wire.ParticipantID{pid(1), pid(2)}, 0, pid(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRing(4)
			r.SetSeats(tc.seats, tc.turn)
			winner, over := r.NoteLeft(tc.leaver, Playing)
			if !over {
				t.Fatalf("turn-holder leave must end the game; got over=false")
			}
			if r.ActiveCount() == 1 && winner != r.lastActive() {
				t.Fatalf("sole survivor: winner=%d, want seat %d", winner, r.lastActive())
			}
		})
	}
}

func TestRingNoteLeftIgnoredWhenNotPlaying(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 0)
	if winner, over := r.NoteLeft(pid(2), Idle); over || winner != -1 {
		t.Fatalf("leave while idle: %d,%v, want -1,false", winner, over)
	}
}

func TestRingConcede(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0) // seat 0 on turn
	// Seat 2 concedes: off-turn, so the other two play on.
	if winner, over := r.Concede(pid(3)); over || winner != -1 {
		t.Fatalf("off-turn concede: %d,%v, want -1,false (play on)", winner, over)
	}
	// Seat 0 (the turn holder) concedes: only seat 1 remains → it wins.
	if winner, over := r.Concede(pid(1)); !over || winner != 1 {
		t.Fatalf("concede down to one: %d,%v, want seat 1,true", winner, over)
	}
}

// A leave can overtake the newGame that seats the leaver (the relay orders
// frames per sender only). NoteLeft then sees no seat — ApplyDeparted must mark
// it Gone once the stale seat list lands.
//
// The departed player is seat 2 while seat 0 is on turn, so the game CONTINUES:
// the leaver was not on turn, could not have a legal move in flight, and the
// leave touches no turn state, so every end converges. (Upstream ended the game
// here unconditionally; see TestRingApplyDepartedEndsWhenTurnHolderLeft for the
// case that still ends.)
func TestRingApplyDepartedRacedNewGameContinues(t *testing.T) {
	r := NewRing(4)
	if winner, over := r.NoteLeft(pid(3), Idle); over || winner != -1 {
		t.Fatalf("pre-seat leave: %d,%v, want -1,false", winner, over)
	}
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	winner, over := r.ApplyDeparted()
	if over || winner != -1 {
		t.Fatalf("3 players, off-turn leaver: %d,%v, want -1,false (play on)", winner, over)
	}
	if !r.Gone[2] || r.Gone[0] || r.Gone[1] {
		t.Fatalf("gone=%v, want only seat 2 marked", r.Gone)
	}
	if r.Turn != 0 {
		t.Fatalf("turn=%d, want 0 unchanged (leaver was not on turn)", r.Turn)
	}
	if r.ActiveCount() != 2 {
		t.Fatalf("active=%d, want 2", r.ActiveCount())
	}
}

// Raced newGame where the DEPARTED seat is the opening turn holder: there is no
// order-independent answer, so the game must end rather than leave a live game
// whose turn sits on an empty seat.
func TestRingApplyDepartedEndsWhenTurnHolderLeft(t *testing.T) {
	r := NewRing(4)
	r.NoteLeft(pid(1), Idle) // seat 0, which will be the opening turn
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	winner, over := r.ApplyDeparted()
	if !over || winner != -1 {
		t.Fatalf("departed turn holder: %d,%v, want -1,true (abandoned)", winner, over)
	}
	if !r.Gone[0] {
		t.Fatalf("gone=%v, want seat 0 marked", r.Gone)
	}
}

func TestRingApplyDepartedSoleSurvivorWins(t *testing.T) {
	r := NewRing(4)
	r.NoteLeft(pid(2), Idle)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2)}, 0)
	if winner, over := r.ApplyDeparted(); !over || winner != 0 {
		t.Fatalf("2-player with departed seat: %d,%v, want 0,true", winner, over)
	}
}

func TestRingApplyDepartedNoopWithoutDepartures(t *testing.T) {
	r := NewRing(4)
	r.SetSeats([]wire.ParticipantID{pid(1), pid(2), pid(3)}, 0)
	if winner, over := r.ApplyDeparted(); over || winner != -1 {
		t.Fatalf("no departures: %d,%v, want -1,false", winner, over)
	}
	// Already-Gone seats are not re-marked: a second pass reports nothing new.
	r.NoteLeft(pid(3), Playing)
	if _, over := r.ApplyDeparted(); over {
		t.Fatal("second pass re-marked an already-gone seat")
	}
}

func TestRingOnPromoteResetsGames(t *testing.T) {
	r := NewRing(4)
	r.Games = 3
	r.OnPromote()
	if r.Games != 0 {
		t.Fatalf("Games=%d after promote, want 0", r.Games)
	}
}
