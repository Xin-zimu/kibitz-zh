package gomokup

import (
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/parley/wire"
)

// nullSender satisfies service.Sender; a joiner-side service under test has
// nothing meaningful to transmit.
type nullSender struct{}

func (nullSender) Broadcast(string, []byte) error                  { return nil }
func (nullSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

func attachJoiner(t *testing.T, self, host wire.ParticipantID) *Service {
	t.Helper()
	s := New()
	s.Attach(service.Context{Send: nullSender{}, Emit: func(any) {}, Self: self, HostID: host})
	return s
}

func newGameFrame(t *testing.T, seats []uint32, turn uint8) []byte {
	t.Helper()
	body, err := wire.Marshal(msg{Kind: kindNewGame, Seats: seats, Turn: turn})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// A session leave has no cross-sender ordering against the host's newGame
// broadcast, so it can arrive first — while this end still has no ring to
// mark. The seat list that then lands still names the departed player.
//
// Since Gomoku Party now keeps playing when an off-turn player leaves, the game
// must be LIVE with the departed seat marked Gone (and the remaining players
// able to finish), rather than either hanging or being killed outright.
func TestNewGameAfterLeaveMarksGoneAndContinues(t *testing.T) {
	s := attachJoiner(t, 2, 1)
	s.MemberLeft(3) // overtook the newGame below
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 0)); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.Outcome != "" {
		t.Fatalf("outcome=%q, want a live game (departed seat 2 was not on turn)", st.Outcome)
	}
	if !st.Playing {
		t.Fatal("game should be playing")
	}
	if !st.Gone[2] {
		t.Fatalf("gone=%v, want departed seat 2 marked", st.Gone)
	}
	if st.Gone[0] || st.Gone[1] {
		t.Fatalf("gone=%v, want only seat 2 marked", st.Gone)
	}
	if st.TurnID != 1 {
		t.Fatalf("turnID=%d, want seat 0 (opening turn unchanged)", st.TurnID)
	}
}

// A leave that beats a newGame in which the departed player OWNS the opening
// turn has no order-independent resolution, so the game ends immediately
// instead of sitting live with the turn on an empty seat.
func TestNewGameAfterLeaveEndsWhenTurnHolderGone(t *testing.T) {
	s := attachJoiner(t, 2, 1)
	s.MemberLeft(1) // seat 0, the opening turn holder
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 0)); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.Outcome != "abandoned" || st.WinnerID != 0 {
		t.Fatalf("outcome=%q winner=%d, want abandoned/0", st.Outcome, st.WinnerID)
	}
	if !st.Gone[0] {
		t.Fatalf("gone=%v, want departed seat 0 marked", st.Gone)
	}
}

// Two seats with one departed leaves a sole survivor: that seat wins rather
// than the game being abandoned.
func TestNewGameAfterLeaveSoleSurvivorWins(t *testing.T) {
	s := attachJoiner(t, 2, 1)
	s.MemberLeft(1)
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2}, 0)); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if st.WinnerID != 2 {
		t.Fatalf("winner=%d, want sole survivor 2", st.WinnerID)
	}
}

// The same race via snapshot: a late joiner restores a Playing snapshot that
// was captured before the host processed the leave. The departed seat must be
// marked Gone on the late joiner too, so its board agrees with everyone else.
func TestRestoreAfterLeaveMarksGone(t *testing.T) {
	host := attachJoiner(t, 1, 1)
	if err := host.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 0)); err != nil {
		t.Fatal(err)
	}
	blob, err := host.Snapshot()
	if err != nil || blob == nil {
		t.Fatalf("snapshot: %v (blob nil=%v)", err, blob == nil)
	}

	late := attachJoiner(t, 4, 1)
	late.MemberLeft(3) // the leave beat the snapshot here
	if err := late.Restore(blob); err != nil {
		t.Fatal(err)
	}
	st := late.State()
	if !st.Playing {
		t.Fatal("late joiner should be in a live game")
	}
	if !st.Gone[2] {
		t.Fatalf("gone=%v, want departed seat 2 marked after restore", st.Gone)
	}
	if st.Outcome != "" {
		t.Fatalf("outcome=%q, want live (off-turn leaver)", st.Outcome)
	}
}
