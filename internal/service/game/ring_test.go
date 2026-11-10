package game

import (
	"strconv"
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

// -----------------------------------------------------------------------------
// Leave-order pins
//
// The continue-or-finish decision is only PARTLY order-independent. The
// ActiveCount branch is a pure function of (seat list, departed set), but the
// turn-holder branch reads the CURRENT turn, which depends on whether an
// in-flight move has already been applied on this end. So two ends processing
// the same departures in different orders can reach different winners.
//
// The tests below pin the behaviour that ships TODAY, warts included. Changing
// the RULE (for example "a leaver who ever held the turn always ends the game")
// is a gameplay decision that needs an explicit ruling first — see
// docs/09-LEAVE-RULES-PROPOSAL.md — and these expectations must be updated
// deliberately as part of that decision, never "fixed" to make an unannounced
// rule change pass.
// -----------------------------------------------------------------------------

// goneString renders a Gone slice as "TFF" so a whole-seat-set expectation fits
// on one line in the tables below.
func goneString(gone []bool) string {
	b := make([]byte, len(gone))
	for i, g := range gone {
		if g {
			b[i] = 'T'
		} else {
			b[i] = 'F'
		}
	}
	return string(b)
}

// orderName names a subtest after a leave order, e.g. "leave-2-1".
func orderName(order []wire.ParticipantID) string {
	name := "leave"
	for _, id := range order {
		name += "-" + strconv.Itoa(int(id))
	}
	return name
}

// permute returns every ordering of xs (n! of them; n is at most 4 here).
func permute(xs []wire.ParticipantID) [][]wire.ParticipantID {
	if len(xs) <= 1 {
		return [][]wire.ParticipantID{append([]wire.ParticipantID(nil), xs...)}
	}
	var out [][]wire.ParticipantID
	for i := range xs {
		rest := append(append([]wire.ParticipantID(nil), xs[:i]...), xs[i+1:]...)
		for _, tail := range permute(rest) {
			out = append(out, append([]wire.ParticipantID{xs[i]}, tail...))
		}
	}
	return out
}

// nonEmptySubsets returns every non-empty subset of xs.
func nonEmptySubsets(xs []wire.ParticipantID) [][]wire.ParticipantID {
	var out [][]wire.ParticipantID
	for mask := 1; mask < 1<<len(xs); mask++ {
		var sub []wire.ParticipantID
		for i := range xs {
			if mask&(1<<i) != 0 {
				sub = append(sub, xs[i])
			}
		}
		out = append(out, sub)
	}
	return out
}

// leaveOutcome is the state an end is left in after applying a leave order.
type leaveOutcome struct {
	gone   string // T/F per seat, seat order
	turn   int
	winner int
	over   bool
}

// runLeaves replays one leave order exactly the way a live end experiences it:
// each leave goes to NoteLeft with that end's current phase, and a leave that
// ends the game flips the phase to Over, so every later leave is recorded
// (Departed) without touching the ring.
//
// That phase flip is not incidental — gomokup.Service.MemberLeft does precisely
// this (NoteLeft, then endLocked on over, which sets s.ph = game.Over), and it
// is why the {1, 2} case on seats [1 2 3] behaves differently per order: in one
// order the second leave lands on an already-finished game and marks nothing.
func runLeaves(seats []wire.ParticipantID, turn int, order []wire.ParticipantID) leaveOutcome {
	r := NewRing(len(seats))
	r.SetSeats(seats, turn)
	ph := Playing
	out := leaveOutcome{winner: -1}
	for _, id := range order {
		winner, over := r.NoteLeft(id, ph)
		if over {
			out.winner, out.over = winner, true
			ph = Over
		}
	}
	out.gone, out.turn = goneString(r.Gone), r.Turn
	return out
}

// TestRingLeaveOrderPermutations3Seats enumerates EVERY leave order for each
// fixed leave set on seats [1 2 3] with the turn on seat 0 (player 1), pinning
// the exact outcome per order: final Gone, Turn, winner and over.
func TestRingLeaveOrderPermutations3Seats(t *testing.T) {
	seats := []wire.ParticipantID{pid(1), pid(2), pid(3)}
	const turn = 0 // player 1 opens; it holds the turn throughout
	for _, tc := range []struct {
		order  []wire.ParticipantID
		gone   string
		turn   int
		winner int
		over   bool
	}{
		// Single off-turn leave: play on, turn untouched.
		{[]wire.ParticipantID{pid(2)}, "FTF", 0, -1, false},
		{[]wire.ParticipantID{pid(3)}, "FFT", 0, -1, false},
		// Single turn-holder leave: ends as abandoned.
		{[]wire.ParticipantID{pid(1)}, "TFF", 0, -1, true},
		// Set including the turn holder: order decides. Off-turn leaver first
		// leaves one active seat, so the OTHER survivor wins; turn holder first
		// abandons the game and the second leave marks nothing.
		{[]wire.ParticipantID{pid(1), pid(2)}, "TFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(1)}, "TTF", 0, 2, true},
		{[]wire.ParticipantID{pid(1), pid(3)}, "TFF", 0, -1, true},
		{[]wire.ParticipantID{pid(3), pid(1)}, "TFT", 0, 1, true},
		// Set excluding the turn holder: order-independent — seat 0 survives.
		{[]wire.ParticipantID{pid(2), pid(3)}, "FTT", 0, 0, true},
		{[]wire.ParticipantID{pid(3), pid(2)}, "FTT", 0, 0, true},
		// Everyone leaves: outcome depends on where the turn holder falls.
		{[]wire.ParticipantID{pid(1), pid(2), pid(3)}, "TFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(3), pid(2)}, "TFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(1), pid(3)}, "TTF", 0, 2, true},
		{[]wire.ParticipantID{pid(2), pid(3), pid(1)}, "FTT", 0, 0, true},
		{[]wire.ParticipantID{pid(3), pid(1), pid(2)}, "TFT", 0, 1, true},
		{[]wire.ParticipantID{pid(3), pid(2), pid(1)}, "FTT", 0, 0, true},
	} {
		t.Run(orderName(tc.order), func(t *testing.T) {
			got := runLeaves(seats, turn, tc.order)
			if got.gone != tc.gone || got.turn != tc.turn || got.winner != tc.winner || got.over != tc.over {
				t.Fatalf("order %v: got gone=%s turn=%d winner=%d over=%v; "+
					"want gone=%s turn=%d winner=%d over=%v",
					tc.order, got.gone, got.turn, got.winner, got.over,
					tc.gone, tc.turn, tc.winner, tc.over)
			}
		})
	}
}

// TestRingLeaveOrderPermutations4Seats is the same exhaustive pin for seats
// [1 2 3 4] with the turn on seat 0 (player 1). The interesting shapes: a
// single off-turn leave continues; a single turn-holder leave ends; a set
// containing the turn holder abandons (-1) when the turn holder is processed
// first but yields a sole survivor when it is processed last; and a set of
// everyone ends in every order (with a winner that depends on the order).
func TestRingLeaveOrderPermutations4Seats(t *testing.T) {
	seats := []wire.ParticipantID{pid(1), pid(2), pid(3), pid(4)}
	const turn = 0 // player 1 opens
	for _, tc := range []struct {
		order  []wire.ParticipantID
		gone   string
		turn   int
		winner int
		over   bool
	}{
		// Single off-turn leave: play on.
		{[]wire.ParticipantID{pid(2)}, "FTFF", 0, -1, false},
		{[]wire.ParticipantID{pid(3)}, "FFTF", 0, -1, false},
		// Single turn-holder leave: ends as abandoned.
		{[]wire.ParticipantID{pid(1)}, "TFFF", 0, -1, true},
		// Two off-turn leavers: still two active (seats 0 and 3), play on.
		{[]wire.ParticipantID{pid(2), pid(3)}, "FTTF", 0, -1, false},
		{[]wire.ParticipantID{pid(3), pid(2)}, "FTTF", 0, -1, false},
		// Turn holder involved: abandoned either way, but Gone differs.
		{[]wire.ParticipantID{pid(1), pid(2)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(1)}, "TTFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(3)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(3), pid(1)}, "TFTF", 0, -1, true},
		// Turn holder processed last: a sole survivor wins (seat 3) instead of
		// the abandoned -1 that the turn-holder-first orders produce.
		{[]wire.ParticipantID{pid(1), pid(2), pid(3)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(3), pid(2)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(1), pid(3)}, "TTFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(3), pid(1)}, "TTTF", 0, 3, true},
		{[]wire.ParticipantID{pid(3), pid(1), pid(2)}, "TFTF", 0, -1, true},
		{[]wire.ParticipantID{pid(3), pid(2), pid(1)}, "TTTF", 0, 3, true},
		// All three off-turn seats leave: seat 0 is the sole survivor, every order.
		{[]wire.ParticipantID{pid(2), pid(3), pid(4)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(2), pid(4), pid(3)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(3), pid(2), pid(4)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(3), pid(4), pid(2)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(4), pid(2), pid(3)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(4), pid(3), pid(2)}, "FTTT", 0, 0, true},
		// Everyone leaves: every order ends the game, but the winner depends on
		// where the turn holder falls in the order.
		{[]wire.ParticipantID{pid(1), pid(2), pid(3), pid(4)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(2), pid(4), pid(3)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(3), pid(2), pid(4)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(3), pid(4), pid(2)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(4), pid(2), pid(3)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(1), pid(4), pid(3), pid(2)}, "TFFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(1), pid(3), pid(4)}, "TTFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(1), pid(4), pid(3)}, "TTFF", 0, -1, true},
		{[]wire.ParticipantID{pid(2), pid(3), pid(1), pid(4)}, "TTTF", 0, 3, true},
		{[]wire.ParticipantID{pid(2), pid(3), pid(4), pid(1)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(2), pid(4), pid(1), pid(3)}, "TTFT", 0, 2, true},
		{[]wire.ParticipantID{pid(2), pid(4), pid(3), pid(1)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(3), pid(1), pid(2), pid(4)}, "TFTF", 0, -1, true},
		{[]wire.ParticipantID{pid(3), pid(1), pid(4), pid(2)}, "TFTF", 0, -1, true},
		{[]wire.ParticipantID{pid(3), pid(2), pid(1), pid(4)}, "TTTF", 0, 3, true},
		{[]wire.ParticipantID{pid(3), pid(2), pid(4), pid(1)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(3), pid(4), pid(1), pid(2)}, "TFTT", 0, 1, true},
		{[]wire.ParticipantID{pid(3), pid(4), pid(2), pid(1)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(4), pid(1), pid(2), pid(3)}, "TFFT", 0, -1, true},
		{[]wire.ParticipantID{pid(4), pid(1), pid(3), pid(2)}, "TFFT", 0, -1, true},
		{[]wire.ParticipantID{pid(4), pid(2), pid(1), pid(3)}, "TTFT", 0, 2, true},
		{[]wire.ParticipantID{pid(4), pid(2), pid(3), pid(1)}, "FTTT", 0, 0, true},
		{[]wire.ParticipantID{pid(4), pid(3), pid(1), pid(2)}, "TFTT", 0, 1, true},
		{[]wire.ParticipantID{pid(4), pid(3), pid(2), pid(1)}, "FTTT", 0, 0, true},
	} {
		t.Run(orderName(tc.order), func(t *testing.T) {
			got := runLeaves(seats, turn, tc.order)
			if got.gone != tc.gone || got.turn != tc.turn || got.winner != tc.winner || got.over != tc.over {
				t.Fatalf("order %v: got gone=%s turn=%d winner=%d over=%v; "+
					"want gone=%s turn=%d winner=%d over=%v",
					tc.order, got.gone, got.turn, got.winner, got.over,
					tc.gone, tc.turn, tc.winner, tc.over)
			}
		})
	}
}

// TestRingLeaveOrderDependenceIsPinned makes the known order-dependence
// explicit: ONE departed set, TWO leave orders, TWO different winners.
//
// This is a pin of current behaviour, NOT a claim of correctness. The decision
// is not order-independent for a set containing the turn holder, because the
// turn-holder branch reads the current turn — which on a real end depends on
// whether an in-flight move has already been applied. It is unobservable today
// only because parley's relay hub is a single goroutine per session, so all
// ends are fed the same membership events in the same global order.
//
// Changing this into a genuinely order-independent rule is a GAMEPLAY DECISION
// requiring a documented ruling (docs/09-LEAVE-RULES-PROPOSAL.md), not a
// refactor. If that decision is taken, update this test deliberately.
func TestRingLeaveOrderDependenceIsPinned(t *testing.T) {
	seats := []wire.ParticipantID{pid(1), pid(2), pid(3)}
	const turn = 0 // seat 0 = player 1 holds the turn
	departed := []wire.ParticipantID{pid(1), pid(2)}

	// Order A — the off-turn leaver first. With two seats still active and the
	// turn intact, that leave plays on; the turn holder's leave then drops the
	// game to a single active seat, so the third player (seat 2) wins outright.
	orderA := runLeaves(seats, turn, []wire.ParticipantID{departed[1], departed[0]})
	// Order B — the turn holder first. That ends the game immediately as
	// abandoned; the off-turn leaver's leave then arrives on an Over game and is
	// recorded without marking the seat.
	orderB := runLeaves(seats, turn, []wire.ParticipantID{departed[0], departed[1]})

	if want := (leaveOutcome{gone: "TTF", turn: 0, winner: 2, over: true}); orderA != want {
		t.Fatalf("order A (2 then 1) = %+v, want %+v (sole survivor wins)", orderA, want)
	}
	if want := (leaveOutcome{gone: "TFF", turn: 0, winner: -1, over: true}); orderB != want {
		t.Fatalf("order B (1 then 2) = %+v, want %+v (abandoned)", orderB, want)
	}

	// The divergence itself: same departures, different winner and Gone set.
	if orderA.winner == orderB.winner {
		t.Fatalf("expected the leave order to decide the winner, but both orders gave winner=%d; "+
			"if the rule was deliberately made order-independent, update this pin and "+
			"docs/09-LEAVE-RULES-PROPOSAL.md together", orderA.winner)
	}
	if orderA.gone == orderB.gone {
		t.Fatalf("expected different Gone sets per order, got %q for both", orderA.gone)
	}
}

// TestRingLeaveOrderIndependenceHolds covers the part of the rule that IS
// order-independent: for any leave set that excludes the current turn holder
// and leaves at least two active seats, every processing order of that set
// yields the same final (Gone, Turn, winner, over). The turn-holder branch can
// never fire in that case, so only the pure ActiveCount branch decides.
//
// Enumerated programmatically for 3 and 4 seats and every possible turn
// position — no hardcoded orders, so the coverage is exhaustive by
// construction.
func TestRingLeaveOrderIndependenceHolds(t *testing.T) {
	for _, seats := range [][]wire.ParticipantID{
		{pid(1), pid(2), pid(3)},
		{pid(1), pid(2), pid(3), pid(4)},
	} {
		for turn := range seats {
			// Candidates: everyone except the current turn holder.
			others := make([]wire.ParticipantID, 0, len(seats)-1)
			for i, id := range seats {
				if i != turn {
					others = append(others, id)
				}
			}
			for _, set := range nonEmptySubsets(others) {
				// Only sets that leave >= 2 active seats: with fewer, the
				// ActiveCount branch ends the game and the sole-survivor result
				// still does not depend on order, but it is a different case
				// (covered by the permutation tables above).
				if len(seats)-len(set) < 2 {
					continue
				}
				name := "seats" + strconv.Itoa(len(seats)) + "-turn" + strconv.Itoa(turn) + "-" + orderName(set)
				t.Run(name, func(t *testing.T) {
					orders := permute(set)
					want := runLeaves(seats, turn, orders[0])
					for _, order := range orders[1:] {
						if got := runLeaves(seats, turn, order); got != want {
							t.Fatalf("departed set %v: order %v gave %+v, order %v gave %+v; "+
								"an off-turn leave set must be order-independent",
								set, orders[0], want, order, got)
						}
					}
				})
			}
		}
	}
}
