package chess

import (
	"testing"
)

// TestTakebackRevertsLastMove drives a full offer/accept cycle: black plays,
// offers a takeback, white accepts, and both ends roll back exactly one move.
func TestTakebackRevertsLastMove(t *testing.T) {
	p := newPair(t)
	p.moveAs(t, p.host, "e2e4")   // white (host, id 1)
	p.moveAs(t, p.player, "e7e5") // black (player, id 2)

	// Black just moved, so it's white's turn: black may offer, white may not.
	if !p.player.State().CanTakeback {
		t.Fatalf("player (last mover) should be able to offer a takeback")
	}
	if p.host.State().CanTakeback {
		t.Fatalf("host (on turn) should not be able to offer a takeback")
	}

	if err := p.player.OfferTakeback(); err != nil {
		t.Fatalf("offer: %v", err)
	}
	p.pump(t)

	// The offer is visible on both ends; once offered, CanTakeback is false.
	if p.host.State().TakebackBy != 2 {
		t.Fatalf("host TakebackBy = %d, want 2", p.host.State().TakebackBy)
	}
	if p.player.State().CanTakeback {
		t.Fatalf("player CanTakeback should be false while its own offer is pending")
	}

	// Only the opponent (host) may accept.
	if err := p.player.AcceptTakeback(); err == nil {
		t.Fatalf("offerer must not be able to accept its own takeback")
	}
	if err := p.host.AcceptTakeback(); err != nil {
		t.Fatalf("accept: %v", err)
	}
	p.pump(t)

	// Both ends rolled back to the position after 1.e4 — black to move again.
	hs, ps := p.host.State(), p.player.State()
	if hs.FEN != ps.FEN {
		t.Fatalf("FEN diverged after takeback: host %q player %q", hs.FEN, ps.FEN)
	}
	if len(hs.History) != 1 || hs.History[0] != "e4" {
		t.Fatalf("history after takeback = %v, want [e4]", hs.History)
	}
	if hs.TurnID != 2 {
		t.Fatalf("turn after takeback = %d, want 2 (black)", hs.TurnID)
	}
	if hs.LastUCI != "e2e4" {
		t.Fatalf("lastUCI after takeback = %q, want e2e4", hs.LastUCI)
	}
	if hs.TakebackBy != 0 {
		t.Fatalf("TakebackBy should reset to 0 after revert, got %d", hs.TakebackBy)
	}

	// Play continues normally from the reverted position.
	p.moveAs(t, p.player, "e7e5")
	if got := p.player.State().History; len(got) != 2 {
		t.Fatalf("history after replay = %v, want 2 moves", got)
	}
}

// TestTakebackUnavailableBeforeAnyMove guards the eligibility check.
func TestTakebackUnavailableBeforeAnyMove(t *testing.T) {
	p := newPair(t)
	if p.host.State().CanTakeback || p.player.State().CanTakeback {
		t.Fatalf("no takeback should be available before any move")
	}
	if err := p.host.OfferTakeback(); err == nil {
		t.Fatalf("OfferTakeback should fail before any move")
	}
}

// TestTakebackRevertsFirstMove covers the stash taken before the very first
// move: its PGN is the bare result marker ("*"), which the engine can't replay.
// The revert must fall back to the start position rather than no-op silently —
// otherwise the offer is consumed and the move stays on the board.
func TestTakebackRevertsFirstMove(t *testing.T) {
	p := newPair(t)
	p.moveAs(t, p.host, "e2e4")

	if !p.host.State().CanTakeback {
		t.Fatalf("host (last mover) should be able to offer a takeback")
	}
	if err := p.host.OfferTakeback(); err != nil {
		t.Fatalf("offer: %v", err)
	}
	p.pump(t)
	if err := p.player.AcceptTakeback(); err != nil {
		t.Fatalf("accept: %v", err)
	}
	p.pump(t)

	for _, st := range []State{p.host.State(), p.player.State()} {
		if len(st.History) != 0 {
			t.Fatalf("first move not reverted: history=%v", st.History)
		}
		if st.TurnID != st.WhiteID {
			t.Fatalf("turn after revert = %d, want white (%d)", st.TurnID, st.WhiteID)
		}
		if st.TakebackBy != 0 {
			t.Fatalf("takeback offer not cleared: %d", st.TakebackBy)
		}
	}

	// Play continues normally from the start position.
	p.moveAs(t, p.host, "d2d4")
	if got := p.host.State().History; len(got) != 1 || got[0] != "d4" {
		t.Fatalf("history after replay = %v, want [d4]", got)
	}
}
