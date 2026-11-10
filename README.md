# kibitz-zh

> **This is a fork of [richardwooding/kibitz](https://github.com/richardwooding/kibitz)**,
> maintained separately. All original design and code are his work under the MIT
> licence (see [LICENSE](LICENSE)). The fork exists to add a Simplified Chinese
> UI and to fix a set of concurrency defects in the four-player game; those
> changes are listed below so it is never ambiguous what came from where.
>
> Upstream is the canonical project — if you want kibitz itself, go there.

## What this fork changes

Everything here is on top of upstream `v0.32.0` (`5897967`).

**Simplified Chinese UI (the `/zh` part).** The client had no i18n mechanism at
all, so one was added: 218 keys, Chinese default with English fully retained and
switchable from the home screen. All thirteen games are covered — titles, status
lines, buttons, toasts — not just the landing page. Crucially, the game outcome
strings that boards compare against (`"player 1 wins"`, `"draw"`, …) are
**protocol values, not prose**: they are left untouched and only projected to
Chinese at the rendering boundary, so nothing can desync from the engine.

**Concurrency fixes in Gomoku Party (`internal/service/gomokup`).** Two defects
found by review, both in the broadcast-rollback protocol:

- `gen`, the counter that tells a rollback whether anything changed while a
  send was in flight, counted only the local actions. Every inbound path (frame
  handlers, `MemberLeft`, `Restore`, `OnPromote`) wrote state without bumping
  it, so a rollback could not see a peer's write and restored an older snapshot
  over it — a peer's new game or seat list silently vanished. All write paths now
  share one protocol that bumps only when the state genuinely changed, so an
  idempotent frame cannot cancel a rollback that was needed.
- `ring.Departed` was snapshotted and restored, so a departure observed while a
  broadcast was in flight was rolled back along with everything else. Since a
  departure is the only replay source for a leave that raced a newGame or a
  snapshot — and participant IDs are never reused — the erasure was permanent,
  and the next raced newGame seated a departed player as live. It is now a
  monotone fact set, replayed rather than restored.

A subtlety worth knowing if you touch this: a leave deliberately does **not**
bump `gen`. A local move advances the turn before it is broadcast, so a leave
arriving mid-send is judged against a turn no peer can see; letting the rollback
run and replaying the departure is the only way both ends converge. The comments
in `service.go` spell this out, and `race_test.go` pins it.

**Smaller items.** `internal/relaycfg` makes the shipped rate limits a single
source of truth shared with the acceptance tests (previously the tests asserted
against a copy, so lowering a limit would not have failed them). Leave-order
behaviour in `ring.go` is documented honestly — parts of it are order-dependent,
though unreachable on a single-node relay — with permutation tests pinning the
current rules; [`docs/09-LEAVE-RULES-PROPOSAL.md`](docs/09-LEAVE-RULES-PROPOSAL.md)
lays out the options rather than changing gameplay unilaterally.

Nothing in the other twelve games' logic, the wire protocol, or the module
layout was altered.

---

# kibitz

> Pull up a chair.

**kibitz** is [croc](https://github.com/schollz/croc)-style pairing for
long-lived, end-to-end-encrypted sessions — chat and a dozen turn-based games
instead of file transfer.

- **Pair like croc**: the host gets a code phrase like `lion-42-maple`, plus a
  share link and QR code. Friends join by clicking or typing.
- **The relay can't read anything**: the phrase seeds a PAKE key exchange;
  the relay only forwards opaque encrypted frames. Self-host it or use a
  hosted one — either way it's blind.
- **One binary**: the relay embeds the whole web client. `kibitz --listen
  :8080`, open a browser, play.
- **Spectators welcome**: sessions hold two players and any number of
  kibitzers, all in the same encrypted chat.

## Status

**Twelve games.** Chess, backgammon (provably fair commit-reveal dice),
checkers, reversi, Connect Four, Gomoku, Hex, Dots & Boxes, Go, Xiangqi, and
two games where cheating is *cryptographically* impossible:

- **Battleship** — each player commits to every cell of their board before the
  first shot; every reveal is verified by every participant (spectators
  included), and both full boards are opened and legality-checked at game end.
- **Gin Rummy** — dealt by a "mental poker" shuffle: the two players jointly
  encrypt and shuffle the deck with a commuting cipher, so neither of them (nor
  the relay) learns the order or the opponent's hand, yet the deck is proven to
  be a fair, untouched 52 cards when keys are revealed at showdown. Full match
  play to 100 with lay-off and dealer alternation.

All of it runs without any server ever seeing a thing, because the relay only
forwards ciphertext. Games start on demand from the picker and run side by side
in one session; rematches swap seats. Eleven of the twelve ship with a "Hard"
computer opponent for solo play (Gin is networked-two-player only).

## How it works

The relay forwards frames it can never read. The code phrase seeds a PAKE
handshake (as in croc); the host wraps a session group key to each joiner;
all chat and moves are XChaCha20-Poly1305 envelopes. Games are
both-sides-validate: every client runs the same rules engine and checks a
position hash on every move — there's no server to cheat past, because the
server is blind. Details: [docs/THREAT-MODEL.md](docs/THREAT-MODEL.md).

## Hosted instance

A public relay runs at **https://kibitz-play.fly.dev** — open it, start a table,
share the phrase. (Remember: the relay is blind either way; you never have to
trust it.)

## Self-hosting

Grab a release binary (or `go install github.com/richardwooding/kibitz/cmd/kibitz@latest`) and:

```sh
kibitz --listen :8080
```

Or run the container image:

```sh
docker run -p 8080:8080 ghcr.io/richardwooding/kibitz
```

Put TLS in front with your reverse proxy of choice. That's it — the web UI,
relay, and everything else is in the one binary. Useful flags:
`--max-sessions` (default 1000), `--version`.

## Development

```sh
make serve    # build the WASM client and run the relay on :8080
make test     # go test -race ./...
```

## Sponsor

If this saves you time, you can [sponsor its maintenance](https://github.com/sponsors/richardwooding).
Sponsorship pays for the unglamorous half — triage, dependency bumps, release plumbing — and is
never a condition of getting help here.

## License

MIT