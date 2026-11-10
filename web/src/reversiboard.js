// reversiboard.js — the reversi module: 8×8 green grid, legal-move dots,
// click to place. Passes are computed by the core; the UI just toasts them.
(() => {
  "use strict";

  window.GameModules = window.GameModules || {};
  window.GameModules.reversi = { label: "⚪ Reversi", paneId: "game-reversi", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;
    let visible = false;
    let lastPassToasted = false;
    let flips = new Set(); // squares to flip-animate this render

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const isPlayer = () => isHotseat() || (g && (g.p1Id === ctx.self() || g.p2Id === ctx.self()));
    const myTurn = () => g && (isHotseat() || g.turnId === ctx.self());
    const over = () => g && g.outcome !== "";

    function statusText() {
      const score = ` · ⚫${g.black} ⚪${g.white}`;
      if (over()) return window.TO(g.outcome) + score;
      if (isHotseat()) return window.T("table.toMove","{color} to move").replace("{color}", g.turnId === g.p1Id ? "⚫" : "⚪") + score;
      return (myTurn() ? window.T("table.yourMove","Your move")
                       : window.T("table.theirTurn","{name} to move").replace("{name}", ctx.name(g.turnId))) +
        score + (isPlayer() ? window.T("table.youAre"," · you are {color}").replace("{color}", g.p1Id === ctx.self() ? "⚫" : "⚪") : "");
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("reversi-moves"), g.history);
      const statusEl = $("reversi-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart","Waiting for the game to start…");
        return;
      }
      statusEl.textContent = statusText();
      $("reversi-resign").classList.toggle("hidden", !isPlayer() || over());
      renderBoard();
    }

    function renderBoard() {
      const el = $("reversi-board");
      el.classList.toggle("my-turn", myTurn() && !over());
      el.innerHTML = "";
      const legal = new Set(myTurn() ? (g.legal || []) : []);
      for (let sq = 0; sq < 64; sq++) el.appendChild(cellFor(sq, legal));
      flips = new Set();
    }

    function cellFor(sq, legal) {
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "rv-cell";
      const v = g.board[sq];
      if (v !== 0) {
        const disc = document.createElement("span");
        disc.className = "rv-disc " + (v > 0 ? "black" : "white");
        if (flips.has(sq)) disc.classList.add("flip");
        cell.appendChild(disc);
      }
      if (sq === g.lastSq) cell.classList.add("last", "place-pop");
      if (legal.has(sq)) {
        cell.classList.add("legal");
        cell.addEventListener("click", () => send({ type: "reversi.place", sq }));
      }
      return cell;
    }

    $("reversi-resign").addEventListener("click", () => {
      if (confirm("Resign reversi?")) send({ type: "reversi.resign" });
    });

    function outcomeWon() {
      if (!isPlayer() || g.outcome.startsWith("draw")) return null;
      const iAmBlack = g.p1Id === ctx.self();
      return g.outcome.startsWith(iAmBlack ? "black wins" : "white wins");
    }

    // Discs whose color flipped since last state get the flip animation.
    function detectFlips(prev) {
      flips = new Set();
      if (!(prev && prev.board)) return;
      let flipped = false;
      for (let i = 0; i < 64; i++) {
        if (prev.board[i] !== 0 && g.board[i] !== 0 && prev.board[i] !== g.board[i]) {
          flips.add(i);
          flipped = true;
        }
      }
      const placed = prev.board[g.lastSq] === 0 && g.board[g.lastSq] !== 0;
      if ((placed || flipped) && window.fx) window.fx.sound.move();
    }

    function announcePass() {
      if (g.passed && !lastPassToasted) {
        toast(myTurn() ? window.T("reversi.noMoveYou","Opponent had no move — you go again.")
                   : window.T("reversi.noMoveThem","No legal move — turn passed."));
      }
      lastPassToasted = g.passed;
    }

    function maybeCelebrate(prev) {
      if (prev && prev.playing && prev.outcome === "" && over() && window.fx) {
        window.fx.celebrate($("game-reversi"), outcomeWon(), window.fx.result(outcomeWon(),
          { draw: g.outcome.startsWith("draw"), spectator: g.outcome, hotseat: isHotseat() }));
      }
    }

    return {
      onEvent(type, e) {
        if (type !== "reversi.state") return;
        const prev = g;
        g = e;
        detectFlips(prev);
        announcePass();
        render();
        maybeCelebrate(prev);
      },
      setVisible(v) { visible = v; if (v) render(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (over()) return { status: "over", detail: g.outcome };
        return { status: "live", myTurn: myTurn() };
      },
    };
  }
})();
