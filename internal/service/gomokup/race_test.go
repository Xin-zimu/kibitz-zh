package gomokup

// Regression tests for the two P1 defects an independent review found in the
// broadcast-rollback protocol. Both are races, but neither is timing-dependent:
// the peer event is delivered from a hook that runs inside Broadcast, on the
// same goroutine, with s.mu released — which is exactly the window a rollback
// has to survive. No sleeps, no goroutines, no -count sensitivity.
//
//	P1-a (cases A, B, G, H and the gen table)
//	  gen was bumped only by the local actions. An inbound frame (handleNewGame,
//	  handleOpen, handleSeats, handlePlace, handleResign), a MemberLeft, a
//	  Restore and an OnPromote all wrote state without bumping it, so a rollback
//	  that raced one of them could not see it and restored an older snapshot over
//	  the top: the peer's new game vanished, a peer's seat list was undone.
//
//	P1-b (cases C, D, E, F)
//	  saveLocked deep-copied ring.Departed, so a departure observed while a
//	  broadcast was in flight was rolled back along with everything else. The
//	  departure is a monotone fact and the only replay source that can ever
//	  correct it (participant IDs are never reused within a session), so the
//	  erasure was permanent: the next raced newGame or snapshot seated the
//	  departed player as a live player.

import (
	"errors"
	"testing"

	"github.com/richardwooding/kibitz/internal/service"
	"github.com/richardwooding/parley/wire"
)

// --- helpers ----------------------------------------------------------------

// raceSender is the controllable wire these tests need. Broadcast hands control
// to during() BEFORE it reports whether the send succeeded, which is where every
// "a peer frame landed while the local broadcast was in flight" case delivers
// its frame: the service mutex is released at that point.
//
// during is one-shot — it is cleared before it runs — so a hook that itself
// broadcasts, or a rollback that emits state, cannot re-enter it. Everything runs
// on the caller's goroutine, so the ordering is a fact, not a hope.
type raceSender struct {
	during func()
	ok     bool
	calls  int
}

func (r *raceSender) Broadcast(string, []byte) error {
	r.calls++
	if r.during != nil {
		during := r.during
		r.during = nil
		during()
	}
	if r.ok {
		return nil
	}
	return errors.New("send: connection lost")
}

func (r *raceSender) SendTo(wire.ParticipantID, string, []byte) error { return nil }

// raceJoiner is attachJoiner with a caller-supplied sender: a non-host end whose
// session host is participant 1. attachJoiner hard-wires nullSender, and the
// rollback tests need to fail the broadcast.
func raceJoiner(self wire.ParticipantID, sender service.Sender) *Service {
	s := New()
	s.Attach(service.Context{Send: sender, Emit: func(any) {}, Self: self, HostID: 1})
	return s
}

// raceGen reads the rollback generation counter (these tests are in-package; the
// counter is deliberately not part of the public State).
func raceGen(s *Service) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

func raceSeatsFrame(t *testing.T, seats []uint32) []byte {
	t.Helper()
	body, err := wire.Marshal(msg{Kind: kindSeats, Seats: seats})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func raceResignFrame(t *testing.T) []byte {
	t.Helper()
	body, err := wire.Marshal(msg{Kind: kindResign})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// racePlaceFrame builds the frame the peer mover would broadcast for (row,col) in
// a game with these seats and opening turn. The move is replayed on a throwaway
// end so the state hash matches what both-sides validation expects.
func racePlaceFrame(t *testing.T, seats []uint32, turn uint8, mover wire.ParticipantID, row, col int8) []byte {
	t.Helper()
	probe := raceJoiner(mover, &raceSender{ok: true})
	if err := probe.HandleFrame(1, newGameFrame(t, seats, turn)); err != nil {
		t.Fatal(err)
	}
	probe.mu.Lock()
	seat, err := probe.checkTurnLocked(mover)
	if err != nil {
		probe.mu.Unlock()
		t.Fatalf("racePlaceFrame: participant %d is not on turn: %v", mover, err)
	}
	if _, err := probe.placeLocked(seat, row, col); err != nil {
		probe.mu.Unlock()
		t.Fatal(err)
	}
	hash := probe.applyAndHashLocked()
	probe.mu.Unlock()

	body, err := wire.Marshal(msg{Kind: kindPlace, Row: row, Col: col, StateHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// raceLive3 starts a live three-seat game (participants 1,2,3) with the opening
// turn on the given seat. It is inbound only, so it never touches the wire.
func raceLive3(t *testing.T, s *Service, turn uint8) {
	t.Helper()
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, turn)); err != nil {
		t.Fatal(err)
	}
}

// --- A: a rollback must not discard a peer's new game ------------------------

// While this end's Place() is between "mutated under the lock" and its rollback,
// the host's kindNewGame lands and installs a fresh game whose opening turn is
// seat 2. Place()'s broadcast then fails.
//
// Before the fix handleNewGame was one of the paths that did not bump gen, so gen
// still matched the value captured before the broadcast, rollback() saw no
// intervening write, and it restored the OLD game: the host's new game (seats,
// turn, empty board) was silently thrown away, leaving this end alone on a
// different game from every peer.
func TestRaceRollbackKeepsPeerNewGame(t *testing.T) {
	sender := &raceSender{}
	s := raceJoiner(2, sender) // seated at seat 1 of this game, and on turn
	raceLive3(t, s, 1)
	if st := s.State(); !st.Playing || st.TurnID != 2 {
		t.Fatalf("precondition: playing=%v turn=%d, want a live game with self (2) on turn", st.Playing, st.TurnID)
	}

	var peerErr error
	sender.during = func() {
		peerErr = s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 2))
	}
	if err := s.Place(0, 0); err == nil {
		t.Fatal("Place should report the broadcast failure")
	}
	if peerErr != nil {
		t.Fatalf("peer newGame: %v", peerErr)
	}

	st := s.State()
	if st.TurnID != 3 {
		t.Fatalf("ROLLBACK CLOBBERED THE PEER'S NEW GAME: turn=%d, want 3 (seat 2 of the host's "+
			"new game). handleNewGame did not bump gen, so rollback() could not see it.", st.TurnID)
	}
	if !st.Playing || len(st.Seats) != 3 {
		t.Fatalf("playing=%v seats=%v, want the peer's live three-seat game", st.Playing, st.Seats)
	}
	if st.Board[0] != 0 {
		t.Fatalf("board[0]=%d, want 0: a new game starts from an empty board", st.Board[0])
	}
}

// --- B: a rollback must not undo a peer's authoritative seat list ------------

// Same window, different peer write: the host's kindSeats frame lands while the
// local Place() is in flight, then the send fails. The claimed list is the host's
// authoritative mirror of who is seated, so the rollback must keep it.
//
// Before the fix handleSeats did not bump gen and the rollback restored the
// pre-frame claimed list, so this end disagreed with the host about who was
// seated — and the host's next Begin would install seats this end had forgotten.
func TestRaceRollbackKeepsPeerSeatList(t *testing.T) {
	sender := &raceSender{}
	s := raceJoiner(2, sender)
	raceLive3(t, s, 1)

	var peerErr error
	sender.during = func() {
		peerErr = s.HandleFrame(1, raceSeatsFrame(t, []uint32{1, 2, 3}))
	}
	if err := s.Place(0, 0); err == nil {
		t.Fatal("Place should report the broadcast failure")
	}
	if peerErr != nil {
		t.Fatalf("peer seats frame: %v", peerErr)
	}

	s.mu.Lock()
	claimed := append([]wire.ParticipantID(nil), s.claimed...)
	s.mu.Unlock()
	if len(claimed) != 3 {
		t.Fatalf("ROLLBACK CLOBBERED THE PEER'S SEAT UPDATE: claimed=%v, want 3 entries. "+
			"handleSeats did not bump gen, so rollback() could not see it.", claimed)
	}
	for i, want := range []wire.ParticipantID{1, 2, 3} {
		if claimed[i] != want {
			t.Fatalf("claimed=%v, want [1 2 3] in the host's order", claimed)
		}
	}
}

// --- C: a departure must survive a rollback (the severe P1) -----------------

// A live three-seat game, self (seat 0) on turn. While Place() is broadcasting,
// participant 3 — an OFF-TURN player, so the game correctly continues — leaves the
// session. That records Departed[3] and marks seat 2 Gone. The Place broadcast
// then fails and the rollback runs.
//
// Before the fix saveLocked deep-copied ring.Departed into the snapshot and
// restoreLocked restored that copy, so the rollback erased the departure
// permanently: Departed is the only replay source for a leave that raced a
// newGame/snapshot (Ring.ApplyDeparted has nothing else to consult), and a
// participant ID is never reused within a session, so nothing could ever
// re-derive it. The end was then stuck believing a departed player was present,
// and the next newGame naming them seated them as a live player.
func TestDepartedRollbackKeepsDeparture(t *testing.T) {
	sender := &raceSender{}
	s := hostService(t, sender) // participant 1, seat 0, on turn
	raceLive3(t, s, 0)

	sender.during = func() { s.MemberLeft(3) } // off-turn leaver: play continues
	if err := s.Place(0, 0); err == nil {
		t.Fatal("Place should report the broadcast failure")
	}

	s.mu.Lock()
	departed := s.ring.Departed[3]
	gone := append([]bool(nil), s.ring.Gone...)
	s.mu.Unlock()

	if !departed {
		t.Fatalf("ROLLBACK ERASED THE DEPARTURE: Departed[3]=false after a rollback (gone=%v). "+
			"A departure is a monotone fact, not rewindable state — nothing can re-derive it.", gone)
	}
	if len(gone) != 3 || !gone[2] {
		t.Fatalf("ROLLBACK UNMARKED THE DEPARTED SEAT: gone=%v, want seat 2 Gone "+
			"(restoreLocked must replay departures via applyDepartedLocked)", gone)
	}

	// The harm that makes this severe: a raced newGame whose seat list still names
	// the departed player must not put them back into play.
	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 0)); err != nil {
		t.Fatal(err)
	}
	st := s.State()
	if !st.Playing {
		t.Fatalf("playing=false after a raced newGame with an off-turn departed seat")
	}
	if len(st.Gone) != 3 || !st.Gone[2] {
		t.Fatalf("DEPARTED PLAYER REINSTATED: gone=%v seats=%v; seat 2 (participant 3) must stay Gone",
			st.Gone, st.Seats)
	}
	if st.Seats[2] != 3 {
		t.Fatalf("seats=%v: seat index is the colour and the turn, so a departed entry must stay "+
			"in place and be marked Gone, never renumbered", st.Seats)
	}
}

// --- D: a departed player cannot re-enter a seat via a raced newGame --------

// The leave reaches this end before any ring exists to mark (Idle: the host has an
// open table but no game), and the host's newGame — whose seat list predates the
// leave — lands afterwards.
//
// The game must be live with the departed seat Gone, and the departed player must
// not remain reinstatable: the lobby mirror must have dropped them, otherwise the
// next Begin (or a rematch carried across by hostOpen) hands the same dead
// participant a seat again. Pre-fix, MemberLeft pruned the mirror only on the
// current host, so a joiner kept naming the departed claimant.
func TestDepartedCannotReenterSeatViaRacedNewGame(t *testing.T) {
	s := raceJoiner(2, &raceSender{ok: true})
	if err := s.HandleFrame(1, openFrame(t, []uint32{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	s.MemberLeft(3) // no ring yet: there is nothing to mark Gone, only a fact to record

	if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 0)); err != nil {
		t.Fatal(err)
	}

	st := s.State()
	if !st.Playing || st.Outcome != "" {
		t.Fatalf("playing=%v outcome=%q, want a live game (the departed seat is off turn)",
			st.Playing, st.Outcome)
	}
	if len(st.Gone) != 3 || !st.Gone[2] {
		t.Fatalf("DEPARTED PLAYER SEATED LIVE: gone=%v seats=%v, want seat 2 Gone", st.Gone, st.Seats)
	}
	if st.TurnID != 1 {
		t.Fatalf("turnID=%d, want seat 0 (participant 1): a Gone seat must not hold the turn", st.TurnID)
	}

	s.mu.Lock()
	claimed := append([]wire.ParticipantID(nil), s.claimed...)
	s.mu.Unlock()
	if len(claimed) != 2 {
		t.Fatalf("claimed=%v, want the two surviving claimants {1,2}", claimed)
	}

	// Not merely Gone once — unreachable. A rematch frame still carrying the
	// pre-leave list (it was already in flight when participant 3 left) must not put
	// the departed player back in the seating list, or the next Begin hands them a
	// seat again. Pre-fix handleOpen and hostOpen assigned the list verbatim, so the
	// departed claimant came straight back.
	if err := s.HandleFrame(1, openFrame(t, []uint32{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	claimed = append([]wire.ParticipantID(nil), s.claimed...)
	s.mu.Unlock()
	for _, id := range claimed {
		if id == 3 {
			t.Fatalf("DEPARTED PLAYER RE-ENTERED THE SEATING LIST: claimed=%v after a stale open "+
				"frame; every rebuild of the list must be filtered through Departed", claimed)
		}
	}
}

// --- E: a Restore must never reintroduce a departed player ------------------

// A late joiner applies the host's catch-up snapshot, but a session leave it has
// already observed has no ordering relationship with that snapshot — the leave can
// arrive first. Both branches of Restore must be filtered through ring.Departed.
func TestDepartedRestoreNeverReintroduces(t *testing.T) {
	// (a) LOBBY branch. Pre-fix, Restore assigned the snapshot's claimed list
	// verbatim, so the departed claimant reappeared in the late joiner's lobby —
	// and the next Begin installed them as a live seat.
	t.Run("lobby", func(t *testing.T) {
		host := hostService(t, &raceSender{ok: true})
		if err := host.HandleFrame(1, openFrame(t, []uint32{1, 2, 3})); err != nil {
			t.Fatal(err)
		}
		blob, err := host.Snapshot()
		if err != nil || blob == nil {
			t.Fatalf("snapshot: %v (blob nil=%v)", err, blob == nil)
		}

		late := raceJoiner(4, &raceSender{ok: true})
		late.MemberLeft(3) // the leave beat the snapshot here
		if err := late.Restore(blob); err != nil {
			t.Fatal(err)
		}

		st := late.State()
		if !st.Lobby {
			t.Fatalf("lobby=false after restoring a lobby snapshot")
		}
		for _, id := range st.Seats {
			if id == 3 {
				t.Fatalf("RESTORE REINTRODUCED A DEPARTED CLAIMANT: seats=%v; the lobby list must "+
					"be filtered through Departed", st.Seats)
			}
		}
		if len(st.Seats) != 2 {
			t.Fatalf("seats=%v, want the two surviving claimants {1,2}", st.Seats)
		}
	})

	// (b) PLAYING branch: the departed player's seat is installed verbatim (seat
	// index is the colour, so removing them would renumber the rest) but must be
	// marked Gone so the late joiner's board agrees with everyone else's.
	t.Run("playing", func(t *testing.T) {
		// The host carries a lobby list into the game (that is the normal path: the
		// seats were claimed, then Begin installed them), so the snapshot's Claimed
		// is non-empty and the mirror check below is meaningful.
		host := hostService(t, &raceSender{ok: true})
		if err := host.HandleFrame(1, openFrame(t, []uint32{1, 2, 3})); err != nil {
			t.Fatal(err)
		}
		raceLive3(t, host, 0)
		blob, err := host.Snapshot()
		if err != nil || blob == nil {
			t.Fatalf("snapshot: %v (blob nil=%v)", err, blob == nil)
		}

		late := raceJoiner(4, &raceSender{ok: true})
		late.MemberLeft(3)
		if err := late.Restore(blob); err != nil {
			t.Fatal(err)
		}

		st := late.State()
		if !st.Playing || st.Outcome != "" {
			t.Fatalf("playing=%v outcome=%q, want a live game (off-turn leaver)", st.Playing, st.Outcome)
		}
		if len(st.Gone) != 3 || !st.Gone[2] {
			t.Fatalf("RESTORE SEATED A DEPARTED PLAYER LIVE: gone=%v seats=%v, want seat 2 Gone",
				st.Gone, st.Seats)
		}
		if st.Seats[2] != 3 {
			t.Fatalf("seats=%v: the departed entry must stay at index 2 (it is the colour), not be "+
				"renumbered", st.Seats)
		}

		// The PLAYING branch writes the lobby mirror too, and that mirror is what the
		// next Begin installs as the seat list. Pre-fix it was assigned the
		// snapshot's claimed list verbatim (no removeDeparted), so the departed
		// claimant sat in the mirror of a late joiner that had already seen them go —
		// and the next Begin would seat them live.
		late.mu.Lock()
		claimed := append([]wire.ParticipantID(nil), late.claimed...)
		late.mu.Unlock()
		for _, id := range claimed {
			if id == 3 {
				t.Fatalf("RESTORE REFILLED THE SEATING MIRROR WITH A DEPARTED PLAYER: claimed=%v; "+
					"both Restore branches must be filtered through Departed", claimed)
			}
		}
	})
}

// --- F: host migration must not reintroduce a departed player ---------------

// parley dispatches MemberLeft to every service BEFORE promoting a successor, so
// the end that becomes host has already seen the leaves. Mirroring
// migration_test.go: a claimant (3) and then the old host (1) leave; the new host
// promotes and calls Begin.
//
// Pre-fix the lobby mirror was pruned only when Ctx().Host was already true, so on
// the about-to-be-promoted end neither leave was applied to claimed. Begin then
// installed a seat list naming both departed participants: their seats came up
// Gone, the opening turn (Games%len after OnPromote zeroed Games) pointed at seat
// 0 — the dead host — and applyDepartedLocked resolved the fresh game as
// abandoned. The new host could never start a real game again.
func TestDepartedHostMigrationNeverSeatsDeparted(t *testing.T) {
	sender := &okThenFailSender{ok: 1 << 20}
	s := raceJoiner(2, sender) // participant 2: a joiner; host is participant 1
	if err := s.HandleFrame(1, openFrame(t, []uint32{1, 2, 3, 4})); err != nil {
		t.Fatal(err)
	}

	s.MemberLeft(3) // a claimant leaves first
	s.MemberLeft(1) // then the host leaves
	promote(s, 2, sender)

	if err := s.Begin(); err != nil {
		t.Fatalf("promoted host begin: %v", err)
	}

	st := s.State()
	if !st.Playing || st.Outcome != "" {
		t.Fatalf("HOST MIGRATION MANUFACTURED A DEAD GAME: playing=%v outcome=%q seats=%v gone=%v "+
			"(the departed players were installed as live seats)", st.Playing, st.Outcome, st.Seats, st.Gone)
	}
	for i, id := range st.Seats {
		if id == 1 || id == 3 {
			t.Fatalf("HOST MIGRATION REINSTATED A DEPARTED PLAYER: departed participant %d installed "+
				"at seat %d (seats=%v)", id, i, st.Seats)
		}
		if st.Gone[i] {
			t.Fatalf("seat %d (%d) is Gone in a freshly begun game: seats=%v", i, id, st.Seats)
		}
	}
	if len(st.Seats) != 2 {
		t.Fatalf("seats=%v, want the two surviving claimants {2,4}", st.Seats)
	}
	if st.TurnID != 2 && st.TurnID != 4 {
		t.Fatalf("turnID=%d, want one of the live seats: the opening turn must not point at a "+
			"departed seat", st.TurnID)
	}

	// A claimed list captured before the leaves (that is what the old host would
	// still have broadcast) must not be able to re-add them after the fact: the
	// new host's own filter has to hold, so a retried or reordered kindSeats frame
	// cannot undo the migration.
	if err := s.HandleFrame(2, raceSeatsFrame(t, []uint32{1, 2, 3, 4})); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	claimed := append([]wire.ParticipantID(nil), s.claimed...)
	s.mu.Unlock()
	for _, id := range claimed {
		if id == 1 || id == 3 {
			t.Fatalf("HOST MIGRATION UNDONE BY A STALE SEAT LIST: claimed=%v; a departed player may "+
				"never be reinstatable", claimed)
		}
	}
}

// --- G: a no-change claim must be a complete no-op --------------------------

// hostClaim/hostRelease used to take a snapshot, bump gen and broadcast on every
// call, including calls that changed nothing (already claimed, already at
// MaxSeats, releasing a seat this end does not hold).
//
// A spurious bump is a correctness bug in the other direction: rollback() declines
// when gen moved, so a redundant claim landing while another goroutine's broadcast
// is in flight cancels a rollback that was genuinely needed — leaving this end (and
// only this end) ahead of every peer, which is the desync the rollback exists to
// prevent.
func TestGenNoOpClaimDoesNotBump(t *testing.T) {
	sender := &raceSender{ok: true}
	s := hostService(t, sender)
	s.mu.Lock()
	s.lobby = true
	s.claimed = []wire.ParticipantID{1}
	s.mu.Unlock()

	gen := raceGen(s)

	// Already claimed.
	if err := s.hostClaim(1); err != nil {
		t.Fatalf("hostClaim(already claimed): %v", err)
	}
	if got := raceGen(s); got != gen {
		t.Fatalf("SPURIOUS GEN BUMP ON A NO-OP CLAIM (%d -> %d): a redundant claim must not "+
			"cancel another goroutine's needed rollback", gen, got)
	}
	if sender.calls != 0 {
		t.Fatalf("REDUNDANT CLAIM HIT THE WIRE: %d broadcast(s); a no-op must not broadcast", sender.calls)
	}

	// Releasing a seat this end does not hold.
	if err := s.hostRelease(9); err != nil {
		t.Fatalf("hostRelease(unclaimed): %v", err)
	}
	if got := raceGen(s); got != gen {
		t.Fatalf("SPURIOUS GEN BUMP ON A NO-OP RELEASE (%d -> %d)", gen, got)
	}
	if sender.calls != 0 {
		t.Fatalf("REDUNDANT RELEASE HIT THE WIRE: %d broadcast(s)", sender.calls)
	}

	// Contrast: a claim that DOES change the list must bump exactly once and
	// broadcast, so the no-op rule is not achieved by disabling the path.
	if err := s.hostClaim(2); err != nil {
		t.Fatalf("hostClaim(2): %v", err)
	}
	if got := raceGen(s); got != gen+1 {
		t.Fatalf("a real claim did not bump gen exactly once (%d -> %d)", gen, got)
	}
	if sender.calls != 1 {
		t.Fatalf("a real claim broadcast %d time(s), want 1", sender.calls)
	}

	// A claim at the seat cap changes nothing either.
	s.mu.Lock()
	s.claimed = []wire.ParticipantID{1, 2, 3, 4}
	s.mu.Unlock()
	gen = raceGen(s)
	if err := s.hostClaim(5); err != nil {
		t.Fatalf("hostClaim at MaxSeats: %v", err)
	}
	if got := raceGen(s); got != gen {
		t.Fatalf("SPURIOUS GEN BUMP ON A CLAIM AT MaxSeats (%d -> %d)", gen, got)
	}
	if sender.calls != 1 {
		t.Fatalf("CLAIM AT MaxSeats HIT THE WIRE: %d broadcast(s), want the earlier 1", sender.calls)
	}
}

// --- H: gen is monotonic, and Restore is a write ---------------------------

// gen lives outside stateSnapshot, so neither a rollback nor a Restore can rewind
// it: it only ever increases. The second half is the P1-a half — a Restore changes
// the state a rollback can rewind, so it must count as a write; otherwise a
// rollback that raced a late-joiner catch-up restores the pre-snapshot state and
// strands the joiner on a game no peer is playing.
func TestGenMonotonicAcrossRollbackAndRestore(t *testing.T) {
	t.Run("rollback never rewinds gen", func(t *testing.T) {
		sender := &raceSender{} // every broadcast fails
		s := hostService(t, sender)
		raceLive3(t, s, 0)
		before := raceGen(s)

		// A peer write lands mid-broadcast. rollback() decides by comparing gen, so
		// that write MUST be visible in the counter — pre-fix MemberLeft did not bump
		// it, the rollback went ahead anyway, and the leave plus the stone were both
		// thrown away.
		sender.during = func() { s.MemberLeft(3) }
		if err := s.Place(0, 0); err == nil {
			t.Fatal("Place should report the broadcast failure")
		}
		after := raceGen(s)
		if after < before {
			t.Fatalf("ROLLBACK REWOUND GEN: %d -> %d; the counter must live outside the snapshot",
				before, after)
		}
		if after == before {
			t.Fatalf("A PEER WRITE DURING THE BROADCAST DID NOT MOVE GEN (%d): the rollback then "+
				"thinks nothing happened and restores over it", before)
		}
		// The newer state won: the rollback must have declined, so the move that did
		// go out stays applied and the peer's departure is not undone.
		st := s.State()
		if st.Board[0] != 1 {
			t.Fatalf("ROLLBACK RAN OVER A PEER WRITE: board[0]=%d, want 1 (gen moved, so the newer "+
				"state must win)", st.Board[0])
		}
		if len(st.Gone) != 3 || !st.Gone[2] {
			t.Fatalf("ROLLBACK UNDID THE PEER'S DEPARTURE: gone=%v, want seat 2 Gone", st.Gone)
		}
	})

	t.Run("restore counts as a write", func(t *testing.T) {
		host := hostService(t, &raceSender{ok: true})
		raceLive3(t, host, 0)
		blob, err := host.Snapshot()
		if err != nil || blob == nil {
			t.Fatalf("snapshot: %v (blob nil=%v)", err, blob == nil)
		}

		late := raceJoiner(4, &raceSender{ok: true})
		before := raceGen(late)
		if err := late.Restore(blob); err != nil {
			t.Fatal(err)
		}
		after := raceGen(late)
		if after <= before {
			t.Fatalf("RESTORE DID NOT BUMP GEN (%d -> %d): a rollback in flight across a late-joiner "+
				"catch-up would rewind the snapshot", before, after)
		}
	})
}

// --- the gen table: every inbound write path must be visible to rollback ----

// Each entry changes state from the mux goroutine while a local action may be
// broadcasting from another one. A missed bump makes that write invisible to
// rollback(); rollback() then restores an older snapshot over the top of it.
func TestGenInboundHandlersBumpOnChange(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Service)
		act   func(t *testing.T, s *Service)
	}{
		{
			name:  "handleNewGame",
			setup: nil,
			act: func(t *testing.T, s *Service) {
				if err := s.HandleFrame(1, newGameFrame(t, []uint32{1, 2, 3}, 0)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "handleOpen",
			setup: nil,
			act: func(t *testing.T, s *Service) {
				if err := s.HandleFrame(1, openFrame(t, []uint32{1, 2})); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "handleSeats",
			setup: func(t *testing.T, s *Service) {
				if err := s.HandleFrame(1, openFrame(t, []uint32{1})); err != nil {
					t.Fatal(err)
				}
			},
			act: func(t *testing.T, s *Service) {
				if err := s.HandleFrame(1, raceSeatsFrame(t, []uint32{1, 2})); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "handlePlace",
			setup: func(t *testing.T, s *Service) { raceLive3(t, s, 1) },
			act: func(t *testing.T, s *Service) {
				// Participant 2 is on turn (seat 1); the frame replays its move.
				if err := s.HandleFrame(2, racePlaceFrame(t, []uint32{1, 2, 3}, 1, 2, 3, 3)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "handleResign",
			setup: func(t *testing.T, s *Service) { raceLive3(t, s, 0) },
			act: func(t *testing.T, s *Service) {
				// Off-turn resignation: seat 1 goes Gone and play continues.
				if err := s.HandleFrame(2, raceResignFrame(t)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:  "MemberLeft",
			setup: func(t *testing.T, s *Service) { raceLive3(t, s, 0) },
			act: func(t *testing.T, s *Service) {
				s.MemberLeft(3) // off-turn leaver: seat 2 Gone, game continues
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := hostService(t, &raceSender{ok: true})
			if c.setup != nil {
				c.setup(t, s)
			}
			before := raceGen(s)
			c.act(t, s)
			after := raceGen(s)
			if after <= before {
				t.Fatalf("%s DID NOT BUMP GEN (%d -> %d): a rollback in flight on the other "+
					"goroutine would clobber this write", c.name, before, after)
			}
		})
	}

	// Counterpart: an idempotent frame is NOT a write. Bumping for it would cancel
	// a rollback that was needed, which is just as bad as a missed bump.
	t.Run("handleSeats idempotent", func(t *testing.T) {
		sender := &raceSender{ok: true}
		s := hostService(t, sender)
		if err := s.HandleFrame(1, openFrame(t, []uint32{1, 2})); err != nil {
			t.Fatal(err)
		}
		gen := raceGen(s)
		if err := s.HandleFrame(1, raceSeatsFrame(t, []uint32{1, 2})); err != nil {
			t.Fatal(err)
		}
		if got := raceGen(s); got != gen {
			t.Fatalf("IDEMPOTENT SEATS FRAME BUMPED GEN (%d -> %d): a repeated list changed "+
				"nothing, so it must not cancel a rollback", gen, got)
		}

		// ... and the very next frame, which DOES change the list, must bump. Without
		// this half the no-op rule could be "satisfied" by never bumping at all, which
		// is the original P1-a defect.
		if err := s.HandleFrame(1, raceSeatsFrame(t, []uint32{1, 2, 3})); err != nil {
			t.Fatal(err)
		}
		if got := raceGen(s); got <= gen {
			t.Fatalf("A SEATS FRAME THAT CHANGED THE LIST DID NOT BUMP GEN (%d -> %d): the no-op "+
				"rule must not be implemented by never bumping", gen, got)
		}

		// The harmful direction, end to end: the same repeated list landing while a
		// local Place is in flight must leave the rollback free to run.
		wireFail := &raceSender{} // fails every send
		game := raceJoiner(2, wireFail)
		raceLive3(t, game, 1) // self (2) is on turn
		if err := game.HandleFrame(1, raceSeatsFrame(t, []uint32{1, 2})); err != nil {
			t.Fatal(err)
		}
		var peerErr error
		wireFail.during = func() {
			peerErr = game.HandleFrame(1, raceSeatsFrame(t, []uint32{1, 2}))
		}
		if err := game.Place(0, 0); err == nil {
			t.Fatal("Place should report the broadcast failure")
		}
		if peerErr != nil {
			t.Fatalf("idempotent seats frame: %v", peerErr)
		}
		if got := game.State().Board[0]; got != 0 {
			t.Fatalf("SPURIOUS GEN BUMP SUPPRESSED A NEEDED ROLLBACK: board[0]=%d, want 0. "+
				"An idempotent peer frame is not a write.", got)
		}
	})
}
