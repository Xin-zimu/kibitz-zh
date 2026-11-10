package gomokup

import (
	"errors"
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/parley/wire"
)

// --- Issue 3: a failed broadcast must not desync the local board -------------
//
// Upstream mutated the board and advanced the turn under the lock and only then
// called Broadcast. When the send failed the local end had already moved while
// no peer ever saw the frame, so the both-sides-validate hash check rejected
// every later move and the game was unrecoverable without a rejoin. These tests
// pin the rollback.

// failSender fails every send, modelling a dead or backed-up WebSocket.
type failSender struct{ calls int }

func (f *failSender) Broadcast(string, []byte) error {
	f.calls++
	return errors.New("send: connection lost")
}
func (f *failSender) SendTo(wire.ParticipantID, string, []byte) error {
	return errors.New("send: connection lost")
}

// okThenFailSender succeeds while ok>0, then fails — for tests that need a
// successful setup phase followed by a broken connection.
type okThenFailSender struct {
	ok    int
	calls int
}

func (f *okThenFailSender) Broadcast(string, []byte) error {
	f.calls++
	if f.calls > f.ok {
		return errors.New("send: connection lost")
	}
	return nil
}
func (f *okThenFailSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

func hostService(t *testing.T, sender service.Sender) *Service {
	t.Helper()
	s := New()
	s.Attach(service.Context{Send: sender, Emit: func(any) {}, Self: 1, HostID: 1, Host: true})
	return s
}

// A failed Place must leave the board and turn exactly as they were.
func TestPlaceRollsBackOnBroadcastFailure(t *testing.T) {
	s := hostService(t, &failSender{})
	// Seat a 2-player game directly (Begin would also try to broadcast).
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2}, 0)); err != nil {
		t.Fatal(err)
	}
	before := s.State()
	if !before.Playing || before.TurnID != 1 {
		t.Fatalf("precondition: playing=%v turn=%d", before.Playing, before.TurnID)
	}

	if err := s.Place(0, 0); err == nil {
		t.Fatal("Place should report the broadcast failure")
	}

	after := s.State()
	if after.Board[0] != 0 {
		t.Fatalf("ROLLBACK FAILED: board[0]=%d, want 0 (stone must not survive a failed broadcast)", after.Board[0])
	}
	if after.TurnID != before.TurnID {
		t.Fatalf("ROLLBACK FAILED: turn %d -> %d, want unchanged", before.TurnID, after.TurnID)
	}
	if len(after.History) != len(before.History) {
		t.Fatalf("ROLLBACK FAILED: history grew %d -> %d", len(before.History), len(after.History))
	}
	if after.Last != before.Last {
		t.Fatalf("ROLLBACK FAILED: last %d -> %d", before.Last, after.Last)
	}
}

// After a rolled-back move the SAME player must be able to move again — the
// rollback has to restore playability, not just the pixels.
func TestPlaceRemainsPlayableAfterRollback(t *testing.T) {
	sender := &okThenFailSender{}
	s := hostService(t, sender)
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2}, 0)); err != nil {
		t.Fatal(err)
	}
	if st := s.State(); st.TurnID != 1 {
		t.Fatalf("precondition turn=%d, want seat 0", st.TurnID)
	}

	// Sender budget is 0 → this Place fails and must roll back.
	if err := s.Place(0, 0); err == nil {
		t.Fatal("expected the place to fail")
	}
	after := s.State()
	if after.Board[0] != 0 {
		t.Fatalf("ROLLBACK FAILED: board[0]=%d, want 0", after.Board[0])
	}
	if after.TurnID != 1 {
		t.Fatalf("turn=%d, want seat 0 still to move", after.TurnID)
	}
	if len(after.History) != 0 {
		t.Fatalf("history=%v, want empty", after.History)
	}

	// The game must still be playable: restore the sender and retry the move.
	sender.ok = 1 << 20
	if err := s.Place(0, 0); err != nil {
		t.Fatalf("place after rollback should succeed: %v", err)
	}
	st := s.State()
	if got := st.Board[0]; got != 1 {
		t.Fatalf("board[0]=%d, want 1 after a successful retry", got)
	}
	if len(st.History) != 1 {
		t.Fatalf("history=%v, want exactly one entry", st.History)
	}
}

// A failed Begin must not leave this end in a game no peer knows about.
func TestBeginRollsBackOnBroadcastFailure(t *testing.T) {
	s := hostService(t, &failSender{})
	// Build the lobby without broadcasting: claim two seats directly.
	s.mu.Lock()
	s.lobby = true
	s.claimed = []wire.ParticipantID{1, 2}
	s.mu.Unlock()

	if err := s.Begin(); err == nil {
		t.Fatal("Begin should report the broadcast failure")
	}

	st := s.State()
	if st.Playing {
		t.Fatalf("ROLLBACK FAILED: still playing after a failed Begin")
	}
	if !st.Lobby {
		t.Fatalf("ROLLBACK FAILED: lobby closed by a Begin that never left the wire")
	}
	s.mu.Lock()
	games := s.ring.Games
	s.mu.Unlock()
	if games != 0 {
		t.Fatalf("ROLLBACK FAILED: ring.Games=%d, want 0 (the rematch counter advanced)", games)
	}
}

// A failed seat-list broadcast must not leave the host's claimed list ahead of
// what the joiners were told.
func TestHostSeatChangeRollsBackOnBroadcastFailure(t *testing.T) {
	s := hostService(t, &failSender{})
	s.mu.Lock()
	s.lobby = true
	s.claimed = []wire.ParticipantID{1}
	s.mu.Unlock()

	if err := s.hostClaim(3); err == nil {
		t.Fatal("hostClaim should report the broadcast failure")
	}
	st := s.State()
	if len(st.Seats) != 1 || st.Seats[0] != 1 {
		t.Fatalf("ROLLBACK FAILED: claimed=%v, want [1]", st.Seats)
	}
}

// --- helpers ---------------------------------------------------------------
