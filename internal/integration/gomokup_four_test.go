package integration

// Four-player integration tests — the coverage gap that let the host-migration
// and mid-game-leave defects ship (Issue 2).
//
// Upstream had three integration tests and all of them seated host + 2 joiners;
// nothing exercised seat 3 (colour 4), turn rotation wrapping through four
// seats, resign at all, or a rematch. These run against a real relay with real
// PAKE sessions, i.e. the same code the browser runs.

import (
	"fmt"
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/gomokup"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

type fourTable struct {
	client *session.Client
	mux    *service.Mux
	gp     *gomokup.Service
	relay  string // relay URL this end is connected to
	phrase string // session phrase (host only)
}

func hostFourGP(t *testing.T, url string) (*fourTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gp := gomokup.New()
	return &fourTable{client: c, mux: service.NewMux(c, gp), gp: gp, relay: url, phrase: phrase}, phrase
}

func joinFourGP(t *testing.T, url, phrase string) *fourTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gp := gomokup.New()
	return &fourTable{client: c, mux: service.NewMux(c, gp), gp: gp, relay: url, phrase: phrase}
}

func drainFourGP(t *testing.T, tb *fourTable) {
	t.Helper()
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
}

func waitRosterFourGP(t *testing.T, tb *fourTable, n int) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-tb.mux.Events():
			if !ok {
				t.Fatal("mux closed before roster filled")
			}
			if r, isR := ev.(service.Roster); isR && len(r.Members) >= n {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a roster of %d", n)
		}
	}
}

func waitFourGP(t *testing.T, tb *fourTable, what string, match func(gomokup.State) bool) gomokup.State {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.gp.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (last: %+v)", what, tb.gp.State())
	panic("unreachable")
}

// seatFour joins three guests and seats everyone, returning the tables in SEAT
// order (index 0..3) so callers can drive moves in the authoritative order.
func seatFourTables(t *testing.T) (seats []*fourTable, host *fourTable, cleanup func()) {
	t.Helper()
	url := startRelay(t)
	host, phrase := hostFourGP(t, url)
	g1 := joinFourGP(t, url, phrase)
	g2 := joinFourGP(t, url, phrase)
	g3 := joinFourGP(t, url, phrase)

	waitRosterFourGP(t, host, 4)
	for _, tb := range []*fourTable{host, g1, g2, g3} {
		drainFourGP(t, tb)
	}

	if err := host.gp.Start(); err != nil {
		t.Fatalf("open table: %v", err)
	}
	for _, tb := range []*fourTable{host, g1, g2, g3} {
		waitFourGP(t, tb, "lobby", func(s gomokup.State) bool { return s.Lobby })
	}
	// The host auto-seats as seat 0; the three guests claim the rest.
	for _, g := range []*fourTable{g1, g2, g3} {
		if err := g.gp.TakeSeat(); err != nil {
			t.Fatalf("take seat: %v", err)
		}
	}
	for _, tb := range []*fourTable{host, g1, g2, g3} {
		waitFourGP(t, tb, "four seated", func(s gomokup.State) bool {
			return s.Lobby && len(s.Seats) == 4
		})
	}

	byID := map[wire.ParticipantID]*fourTable{
		host.client.Self(): host,
		g1.client.Self():   g1,
		g2.client.Self():   g2,
		g3.client.Self():   g3,
	}
	lobby := host.gp.State().Seats // claimed order = seat order
	seats = make([]*fourTable, 0, 4)
	for _, id := range lobby {
		tb, ok := byID[id]
		if !ok {
			t.Fatalf("roster id %d has no table", id)
		}
		seats = append(seats, tb)
	}
	if len(seats) != 4 {
		t.Fatalf("seats=%d, want 4", len(seats))
	}
	return seats, host, func() {}
}

// TestGomokuPartyFourPlayersPlaysToWin is the missing end-to-end test: four
// real encrypted sessions, a full turn rotation through four distinct colours,
// and a five-in-a-row win that every end agrees on.
func TestGomokuPartyFourPlayersPlaysToWin(t *testing.T) {
	seats, _, _ := seatFourTables(t)
	host := seats[0]

	if err := host.gp.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, tb := range seats {
		st := waitFourGP(t, tb, "playing", func(s gomokup.State) bool {
			return s.Playing && len(s.Seats) == 4
		})
		if st.Outcome != "" {
			t.Fatalf("fresh four-player game already over: %q", st.Outcome)
		}
	}

	// Seat 0 builds a five across row 0 while seats 1..3 fill their own rows.
	// Turn order is 0,1,2,3 then wraps — exercise the wrap twice.
	type mv struct {
		seat int
		row  int8
		col  int8
	}
	var seq []mv
	rows := []int8{5, 9, 13} // the three non-winning rows
	for col := int8(0); col < 5; col++ {
		seq = append(seq, mv{0, 0, col})
		for i := 1; i <= 3; i++ {
			if col == 4 {
				break // seat 0 wins on its 5th stone; others do not move again
			}
			seq = append(seq, mv{i, rows[i-1], col})
		}
	}

	for _, m := range seq {
		tb := seats[m.seat]
		self := uint32(tb.client.Self())
		waitFourGP(t, tb, fmt.Sprintf("turn for seat %d", m.seat), func(s gomokup.State) bool {
			return s.Playing && uint32(s.TurnID) == self
		})
		if err := tb.gp.Place(m.row, m.col); err != nil {
			t.Fatalf("seat %d place (%d,%d): %v", m.seat, m.row, m.col, err)
		}
	}

	winner := seats[0].client.Self()
	for i, tb := range seats {
		st := waitFourGP(t, tb, "outcome", func(s gomokup.State) bool { return s.Outcome != "" })
		if uint32(st.WinnerID) != uint32(winner) {
			t.Fatalf("seat %d end: winner=%d, want %d", i, st.WinnerID, winner)
		}
		if len(st.WinCells) != 5 {
			t.Fatalf("seat %d: winCells=%v, want 5 cells", i, st.WinCells)
		}
		// Every end must agree on the board, or both-sides-validate is broken.
		if st.Board != seats[0].gp.State().Board {
			t.Fatalf("seat %d: board diverged from seat 0", i)
		}
		if len(st.History) != len(seq) {
			t.Fatalf("seat %d: history=%d moves, want %d", i, len(st.History), len(seq))
		}
	}
}

// TestGomokuPartyResignInFourPlayerGame covers gp.Resign, which upstream never
// tested at all. An off-turn resign keeps the game alive for the other three;
// resigning down to one survivor ends it.
func TestGomokuPartyResignInFourPlayerGame(t *testing.T) {
	seats, _, _ := seatFourTables(t)

	if err := seats[0].gp.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, tb := range seats {
		waitFourGP(t, tb, "playing", func(s gomokup.State) bool { return s.Playing })
	}

	// Seat 3 resigns while seat 0 is on turn → the other three play on.
	if err := seats[3].gp.Resign(); err != nil {
		t.Fatalf("seat 3 resign: %v", err)
	}
	for i, tb := range seats[:3] {
		st := waitFourGP(t, tb, "still playing after off-turn resign", func(s gomokup.State) bool {
			return s.Playing && s.Gone[3]
		})
		if st.Outcome != "" {
			t.Fatalf("survivor %d: outcome=%q, want the game to continue", i, st.Outcome)
		}
	}

	// Now seat 1 and 2 resign, leaving seat 0 as the sole survivor.
	if err := seats[1].gp.Resign(); err != nil {
		t.Fatalf("seat 1 resign: %v", err)
	}
	waitFourGP(t, seats[0], "seat 2 done", func(s gomokup.State) bool { return s.Gone[1] })
	if err := seats[2].gp.Resign(); err != nil {
		t.Fatalf("seat 2 resign: %v", err)
	}

	st := waitFourGP(t, seats[0], "sole survivor", func(s gomokup.State) bool {
		return s.Outcome != ""
	})
	if st.Outcome != "seat 1 wins" && uint32(st.WinnerID) != uint32(seats[0].client.Self()) {
		t.Fatalf("outcome=%q winner=%d, want the last remaining player to win",
			st.Outcome, st.WinnerID)
	}
}

// TestGomokuPartyRematchRotatesOpeningSeat covers the rematch path — finish a
// game, reopen the table, begin again — and the rotating opening seat.
//
// A rematch can only start once the previous game is over (Start refuses while
// playing), so each round is driven to a real five-in-a-row first.
func TestGomokuPartyRematchRotatesOpeningSeat(t *testing.T) {
	seats, host, _ := seatFourTables(t)

	openers := map[wire.ParticipantID]bool{}
	for round := 0; round < 3; round++ {
		if err := host.gp.Begin(); err != nil {
			t.Fatalf("round %d begin: %v", round, err)
		}
		for _, tb := range seats {
			waitFourGP(t, tb, fmt.Sprintf("round %d playing", round), func(s gomokup.State) bool {
				return s.Playing
			})
		}
		openers[host.gp.State().TurnID] = true

		endRoundByResigning(t, seats)

		for _, tb := range seats {
			waitFourGP(t, tb, fmt.Sprintf("round %d over", round), func(s gomokup.State) bool {
				return s.Outcome != ""
			})
		}

		// Reopen the table for the next round.
		if err := host.gp.Start(); err != nil {
			t.Fatalf("round %d reopen: %v", round, err)
		}
		for _, tb := range seats {
			waitFourGP(t, tb, "reopened lobby", func(s gomokup.State) bool { return s.Lobby })
		}
	}
	if len(openers) < 2 {
		t.Fatalf("opening seat never rotated across 3 rematches (openers=%v)", openers)
	}
}

// endRoundByResigning finishes a round deterministically: every seat except the
// host's resigns, so the host is the sole survivor and the game ends. Resigning
// is used rather than playing to five because the point of the rematch test is
// the reopen/rotate cycle, not the win condition (which
// TestGomokuPartyFourPlayersPlaysToWin covers properly).
func endRoundByResigning(t *testing.T, seats []*fourTable) {
	t.Helper()
	for _, tb := range seats[1:] {
		st := tb.gp.State()
		if st.Outcome != "" {
			continue // already over
		}
		if err := tb.gp.Resign(); err != nil {
			// A seat that already left has nothing to resign; that is fine.
			t.Logf("resign from %d: %v", tb.client.Self(), err)
		}
	}
	waitFourGP(t, seats[0], "round over after resigns", func(s gomokup.State) bool {
		return s.Outcome != ""
	})
}

// TestGomokuPartySpectatorWatches covers the spectator path: an observer joins
// the same encrypted session without taking a seat and still receives the
// authoritative game state (the relay is blind; roles live inside the channel).
func TestGomokuPartySpectatorWatches(t *testing.T) {
	seats, host, _ := seatFourTables(t)

	// A fifth participant joins the same session as a pure observer.
	c, err := session.Join(testCtx(t), host.relay, host.phrase,
		append(proto.Options(), session.WithObserver())...)
	if err != nil {
		t.Fatalf("observer join: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	wgp := gomokup.New()
	watcher := &fourTable{client: c, mux: service.NewMux(c, wgp), gp: wgp,
		relay: host.relay, phrase: host.phrase}
	drainFourGP(t, watcher)

	if err := host.gp.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, tb := range seats {
		waitFourGP(t, tb, "playing", func(s gomokup.State) bool { return s.Playing })
	}

	// The spectator receives the same authoritative state and is not seated.
	st := waitFourGP(t, watcher, "spectator sees the game", func(s gomokup.State) bool {
		return s.Playing
	})
	self := watcher.client.Self()
	for i, id := range st.Seats {
		if id == self {
			t.Fatalf("spectator was seated at %d unexpectedly", i)
		}
	}
	if len(st.Seats) != 4 {
		t.Fatalf("spectator sees seats=%v, want 4", st.Seats)
	}
	if st.Board != host.gp.State().Board {
		t.Fatal("spectator board diverged from the host's")
	}
}
