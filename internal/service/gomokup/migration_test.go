package gomokup

// Host-migration regression tests (Issue 1).
//
// Upstream pruned the lobby's `claimed` list only when Ctx().Host was already
// true. parley dispatches session.MemberLeft to every service BEFORE promoting
// a successor (mux.dispatch: observeMembers, then maybeMigrate), so on the end
// about to be promoted the prune was skipped and the DEPARTED HOST stayed in
// `claimed`. OnPromote zeroes ring.Games, so the next Begin opened on
// seat 0 — the dead host — and applyDepartedLocked resolved the fresh game as
// abandoned (or as a phantom walkover). Net effect: after a host handoff the new
// host could never start a real game again.

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/parley/wire"
)

// promote applies what parley's mux does to a service on host migration:
// re-Attach with Host=true/HostID=self, then the Promotable hook.
func promote(s *Service, self wire.ParticipantID, sender service.Sender) {
	s.Attach(service.Context{Send: sender, Emit: func(any) {}, Self: self, HostID: self, Host: true})
	s.OnPromote()
}

// The core regression: after the host leaves, the promoted host must not carry
// the departed host in its lobby list, and must be able to begin a real game.
func TestHostMigrationPrunesDepartedHostAndCanBegin(t *testing.T) {
	sender := &okThenFailSender{ok: 1 << 20}
	s := New()
	// This end is participant 2, a joiner; participant 1 is the host.
	s.Attach(service.Context{Send: sender, Emit: func(any) {}, Self: 2, HostID: 1, Host: false})

	// The host opened a lobby with three claimants: {1 (host), 2 (self), 3}.
	if err := s.HandleFrame(1, openFrame(t, []uint32{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); !st.Lobby || len(st.Seats) != 3 {
		t.Fatalf("precondition: lobby=%v seats=%v", st.Lobby, st.Seats)
	}

	// The host leaves. MemberLeft arrives BEFORE this end is promoted, exactly
	// as parley orders it.
	s.MemberLeft(1)
	promote(s, 2, sender)

	st := s.State()
	if !st.Lobby {
		t.Fatalf("still expected an open lobby after the host left")
	}
	for _, id := range st.Seats {
		if id == 1 {
			t.Fatalf("STALE SEAT: departed host 1 still claimed=%v", st.Seats)
		}
	}
	if len(st.Seats) != 2 {
		t.Fatalf("claimed=%v, want 2 seats {2,3}", st.Seats)
	}

	// And the promoted host must be able to start play for real.
	if err := s.Begin(); err != nil {
		t.Fatalf("promoted host begin: %v", err)
	}
	live := s.State()
	if !live.Playing {
		t.Fatalf("expected a live game, got %+v", live)
	}
	if live.Outcome != "" {
		t.Fatalf("REGRESSION: fresh game ended immediately with outcome=%q "+
			"(the dead seat was installed and resolved as over)", live.Outcome)
	}
	if len(live.Seats) != 2 {
		t.Fatalf("game seats=%v, want 2", live.Seats)
	}
	for i, id := range live.Seats {
		if id == 1 {
			t.Fatalf("departed host installed as live seat %d", i)
		}
		if live.Gone[i] {
			t.Fatalf("seat %d (%d) is Gone in a freshly begun game", i, id)
		}
	}
	// A stone must actually be playable — the real end-to-end proof.
	if live.TurnID != 2 && live.TurnID != 3 {
		t.Fatalf("turnID=%d, want one of the live seats", live.TurnID)
	}
}

// If the promoted host is the only one left seated, Begin must refuse rather
// than manufacture a game against a departed seat.
func TestHostMigrationAloneCannotBegin(t *testing.T) {
	sender := &okThenFailSender{ok: 1 << 20}
	s := New()
	s.Attach(service.Context{Send: sender, Emit: func(any) {}, Self: 2, HostID: 1, Host: false})
	if err := s.HandleFrame(1, openFrame(t, []uint32{1, 2})); err != nil {
		t.Fatal(err)
	}
	s.MemberLeft(1)
	promote(s, 2, sender)

	st := s.State()
	if len(st.Seats) != 1 || st.Seats[0] != 2 {
		t.Fatalf("claimed=%v, want just {2}", st.Seats)
	}
	if err := s.Begin(); err == nil {
		t.Fatal("Begin with a single seated player must be refused")
	}
}

// Migration during PLAY (not the lobby): the departed host's seat is marked
// Gone; if it was not on turn the game continues.
func TestHostMigrationMidGameContinuesWhenOffTurn(t *testing.T) {
	sender := &okThenFailSender{ok: 1 << 20}
	s := New()
	s.Attach(service.Context{Send: sender, Emit: func(any) {}, Self: 2, HostID: 1, Host: false})
	// Three-player game, seat 1 (self) to move; seat 0 is the host.
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 1)); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); !st.Playing || st.TurnID != 2 {
		t.Fatalf("precondition: playing=%v turn=%d", st.Playing, st.TurnID)
	}

	s.MemberLeft(1) // host leaves; self was on turn, so play continues
	promote(s, 2, sender)

	st := s.State()
	if st.Outcome != "" {
		t.Fatalf("outcome=%q, want the game to continue", st.Outcome)
	}
	if !st.Playing || !st.Gone[0] {
		t.Fatalf("playing=%v gone=%v, want live game with seat 0 Gone", st.Playing, st.Gone)
	}
	if st.TurnID != 2 {
		t.Fatalf("turnID=%d, want still self (seat 1)", st.TurnID)
	}
	// Self must still be able to move. Stone colour is seat index + 1, and
	// self sits in seat 1, so the stone is colour 2.
	if err := s.Place(0, 0); err != nil {
		t.Fatalf("place after host migration: %v", err)
	}
	if got := s.State().Board[0]; got != 2 {
		t.Fatalf("board[0]=%d, want self's stone (colour 2)", got)
	}
}

func openFrame(t *testing.T, seats []uint32) []byte {
	t.Helper()
	body, err := wire.Marshal(msg{Kind: kindOpen, Seats: seats})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
