package integration

import (
	"slices"
	"testing"
	"time"

	"github.com/richardwooding/kibitz/internal/proto"
	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/kibitz/internal/service/gomokup"
	"github.com/richardwooding/parley/session"
	"github.com/richardwooding/parley/wire"
)

type gpTable struct {
	client *session.Client
	mux    *service.Mux
	gp     *gomokup.Service
}

func hostGP(t *testing.T, url string) (*gpTable, string) {
	t.Helper()
	c, phrase, err := session.Host(testCtx(t), url, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gp := gomokup.New()
	return &gpTable{client: c, mux: service.NewMux(c, gp), gp: gp}, phrase
}

func joinGP(t *testing.T, url, phrase string) *gpTable {
	t.Helper()
	c, err := session.Join(testCtx(t), url, phrase, proto.Options()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	gp := gomokup.New()
	return &gpTable{client: c, mux: service.NewMux(c, gp), gp: gp}
}

func gpDrain(tb *gpTable) {
	go func() {
		for range tb.mux.Events() { //nolint:revive // discard
		}
	}()
}

// gpWaitRoster reads tb's mux events until the roster has n members (so the host
// has keyed all joiners), then returns; the caller starts draining afterward.
func gpWaitRoster(t *testing.T, tb *gpTable, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
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
			t.Fatalf("timed out waiting for roster of %d", n)
		}
	}
}

func gpWait(t *testing.T, tb *gpTable, match func(gomokup.State) bool) gomokup.State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if st := tb.gp.State(); match(st) {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out (last: %+v)", tb.gp.State())
	panic("unreachable")
}

func seatsHave(st gomokup.State, id wire.ParticipantID) bool {
	return slices.Contains(st.Seats, id)
}

// openAndSeat opens the lobby (host), has each joiner take a seat, and waits for
// every end to see all three seated. Returns once the lobby holds host+p1+p2.
func openAndSeat(t *testing.T, host, p1, p2 *gpTable) {
	t.Helper()
	if err := host.gp.Start(); err != nil { // opens the lobby (host auto-seats)
		t.Fatalf("open: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return s.Lobby })
	}
	if err := p1.gp.TakeSeat(); err != nil {
		t.Fatalf("p1 take seat: %v", err)
	}
	if err := p2.gp.TakeSeat(); err != nil {
		t.Fatalf("p2 take seat: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool {
			return s.Lobby && len(s.Seats) == 3 &&
				seatsHave(s, host.client.Self()) && seatsHave(s, p1.client.Self()) && seatsHave(s, p2.client.Self())
		})
	}
}

// TestGomokuPartyLobbyThreeHanded exercises the full lobby → play path: the host
// opens a table, two joiners take seats (host auto-seated), the host begins, and
// a 3-player game plays to a five-in-a-row win with all ends converged.
func TestGomokuPartyLobbyThreeHanded(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)
	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)

	openAndSeat(t, host, p1, p2)

	if err := host.gp.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return s.Playing && len(s.Seats) == 3 })
	}

	// The host always holds seat 0, but p1's and p2's claim directs race to the
	// host, so seats 1 and 2 are arrival order — play in the authoritative seat
	// order. Host builds a five on row 0 while the others fill separate rows.
	seats := host.gp.State().Seats
	byID := map[wire.ParticipantID]*gpTable{p1.client.Self(): p1, p2.client.Self(): p2}
	second, third := byID[seats[1]], byID[seats[2]]
	type mv struct {
		tb   *gpTable
		r, c int8
	}
	seq := []mv{
		{host, 0, 0}, {second, 5, 0}, {third, 10, 0},
		{host, 0, 1}, {second, 5, 1}, {third, 10, 1},
		{host, 0, 2}, {second, 5, 2}, {third, 10, 2},
		{host, 0, 3}, {second, 5, 3}, {third, 10, 3},
		{host, 0, 4}, // host's fifth → win
	}
	for _, m := range seq {
		self := uint32(m.tb.client.Self())
		gpWait(t, m.tb, func(s gomokup.State) bool { return s.Playing && uint32(s.TurnID) == self })
		if err := m.tb.gp.Place(m.r, m.c); err != nil {
			t.Fatalf("place (%d,%d): %v", m.r, m.c, err)
		}
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		st := gpWait(t, tb, func(s gomokup.State) bool { return s.Outcome != "" })
		if uint32(st.WinnerID) != uint32(host.client.Self()) {
			t.Fatalf("winner = %d, want host %d", st.WinnerID, host.client.Self())
		}
		if len(st.WinCells) != 5 {
			t.Fatalf("win cells = %v, want 5", st.WinCells)
		}
	}
}

// TestGomokuPartyLobbyLeaveAndGate: a claimant can release a seat (freeing it on
// every end), and the host cannot begin with fewer than two seated.
func TestGomokuPartyLobbyLeaveAndGate(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)
	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)

	if err := host.gp.Start(); err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return s.Lobby && len(s.Seats) == 1 })
	}

	// p1 takes a seat, then leaves — the seat frees on every end.
	if err := p1.gp.TakeSeat(); err != nil {
		t.Fatalf("take: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return len(s.Seats) == 2 })
	}
	if err := p1.gp.LeaveSeat(); err != nil {
		t.Fatalf("leave: %v", err)
	}
	for _, tb := range []*gpTable{host, p1, p2} {
		gpWait(t, tb, func(s gomokup.State) bool { return len(s.Seats) == 1 })
	}

	// With only the host seated, Begin is refused.
	if err := host.gp.Begin(); err == nil {
		t.Fatal("begin with one seated should fail")
	}
}

// TestGomokuPartyLeaveContinuesForRemainingPlayers replaces upstream's
// TestGomokuPartyLeaveEndsGame, which pinned the behaviour this fork changed:
// upstream ended a 3- or 4-player game whenever ANY player left, so one person
// closing a tab killed the game for everyone still connected.
//
// New rule (see internal/service/game/ring.go):
//   - an OFF-TURN player leaving marks their seat Gone and play continues;
//   - the TURN HOLDER leaving has no order-independent resolution, so the game
//     ends rather than hanging on a seat nobody occupies;
//   - dropping to a single active seat ends the game with that seat winning.
//
// NOTE ON SEAT ORDER: seats are assigned in CLAIM ARRIVAL order, not by join
// order, so this test reads the authoritative seat list instead of assuming
// p1 holds seat 1. (Assuming it made this test flaky under -race.)
func TestGomokuPartyLeaveContinuesForRemainingPlayers(t *testing.T) {
	url := startRelay(t)
	host, phrase := hostGP(t, url)
	p1 := joinGP(t, url, phrase)
	p2 := joinGP(t, url, phrase)
	gpWaitRoster(t, host, 3)
	gpDrain(host)
	gpDrain(p1)
	gpDrain(p2)

	openAndSeat(t, host, p1, p2)
	if err := host.gp.Begin(); err != nil {
		t.Fatalf("begin: %v", err)
	}
	byID := map[wire.ParticipantID]*gpTable{
		host.client.Self(): host,
		p1.client.Self():   p1,
		p2.client.Self():   p2,
	}
	st0 := gpWait(t, host, func(s gomokup.State) bool {
		return s.Playing && uint32(s.TurnID) == uint32(host.client.Self())
	})
	// Sanity: the host must actually hold the opening turn for this scenario.
	if len(st0.Seats) != 3 {
		t.Fatalf("seats=%v, want 3", st0.Seats)
	}
	if err := host.gp.Place(0, 0); err != nil {
		t.Fatalf("place: %v", err)
	}

	// --- case 1: an OFF-TURN player leaves; the game must continue ----------
	afterMove := gpWait(t, host, func(s gomokup.State) bool {
		return s.Board[0] != 0 && s.TurnID != 0
	})
	leaverID := afterMove.TurnID // the player whose turn it now IS
	// Choose an off-turn leaver: any seated player who is neither the host nor
	// the current turn holder.
	var offTurnSeat = -1
	for i, id := range afterMove.Seats {
		if id != host.client.Self() && id != leaverID {
			offTurnSeat = i
			break
		}
	}
	if offTurnSeat < 0 {
		t.Fatal("no off-turn seat to test with")
	}
	offTurnTable := byID[afterMove.Seats[offTurnSeat]]
	if offTurnTable == nil {
		t.Fatalf("seat %d has no table", offTurnSeat)
	}
	if err := offTurnTable.client.Close(); err != nil {
		t.Fatal(err)
	}

	survivors := []*gpTable{host}
	for _, tb := range []*gpTable{p1, p2} {
		if tb != offTurnTable {
			survivors = append(survivors, tb)
		}
	}
	for _, tb := range survivors {
		st := gpWait(t, tb, func(s gomokup.State) bool { return s.Gone[offTurnSeat] })
		if st.Outcome != "" {
			t.Fatalf("off-turn leave ended the game (outcome=%q); "+
				"the remaining players should still be able to finish", st.Outcome)
		}
		if !st.Playing {
			t.Fatal("game should still be playing after an off-turn leave")
		}
	}

	// --- the survivors can still play a real move --------------------------
	turnHolder := byID[gpWait(t, host, func(s gomokup.State) bool { return s.Playing }).TurnID]
	if turnHolder == nil {
		t.Fatal("no table for the current turn holder")
	}
	row, col := int8(5), int8(5)
	if err := turnHolder.gp.Place(row, col); err != nil {
		t.Fatalf("survivor could not move after a teammate left: %v", err)
	}
	for _, tb := range survivors {
		gpWait(t, tb, func(s gomokup.State) bool {
			return s.Board[int(row)*gomokup.Size+int(col)] != 0
		})
	}

	// --- case 2: resign down to one active seat; survivor wins -------------
	// Resigning is used rather than closing a socket so the outcome is
	// deterministic regardless of which seat happens to hold the turn (closing
	// the turn holder's socket legitimately ends the game as abandoned).
	for _, tb := range survivors {
		if tb == host {
			continue
		}
		if st := tb.gp.State(); st.Outcome == "" {
			if err := tb.gp.Resign(); err != nil {
				t.Fatalf("resign: %v", err)
			}
		}
	}
	st := gpWait(t, host, func(s gomokup.State) bool { return s.Outcome != "" })
	if uint32(st.WinnerID) != uint32(host.client.Self()) {
		t.Fatalf("after dropping to one active seat: outcome=%q winner=%d, want the survivor (%d) to win",
			st.Outcome, st.WinnerID, host.client.Self())
	}
}
