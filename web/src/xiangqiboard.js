// xiangqiboard.js — the Xiangqi (Chinese chess) module: a 9×10 board rendered
// with Chinese glyphs. Click one of your pieces to highlight its legal targets
// (filtered from the core's g.legal list), then click a target to move. Same
// select-then-move pattern as the checkers board.
(() => {
  "use strict";

  const FILES = 9;
  const RANKS = 10;

  // Piece glyphs by |value| (1..7): General, Advisor, Elephant, Horse,
  // Chariot, Cannon, Soldier — red set and black set.
  const RED = ["", "帥", "仕", "相", "馬", "車", "炮", "兵"];
  const BLACK = ["", "將", "士", "象", "馬", "車", "砲", "卒"];

  window.GameModules = window.GameModules || {};
  window.GameModules.xiangqi = { label: "🏯 Xiangqi", paneId: "game-xiangqi", create };

  function create(ctx) {
    const { $, send } = ctx;
    let g = null;
    let sel = -1; // selected origin square, or -1
    let visible = false;

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const isPlayer = () => isHotseat() || (g && (g.p1Id === ctx.self() || g.p2Id === ctx.self()));
    const myTurn = () => g && (isHotseat() || g.turnId === ctx.self());
    const over = () => g && g.outcome !== "";

    // sources: squares from which I have at least one legal move.
    function sources() {
      const out = new Set();
      for (const m of g.legal || []) out.add(m[0]);
      return out;
    }
    // targets: legal destinations from the selected origin.
    function targets() {
      const out = new Set();
      if (sel < 0) return out;
      for (const m of g.legal || []) if (m[0] === sel) out.add(m[1]);
      return out;
    }

    function onSquare(sq) {
      if (!g || !myTurn() || over() || !isPlayer()) return;
      if (sel >= 0 && targets().has(sq)) {
        send({ type: "xiangqi.move", frm: sel, to: sq });
        sel = -1;
        render();
        return;
      }
      sel = sources().has(sq) ? sq : -1;
      render();
    }

    function statusText() {
      if (over()) return g.outcome;
      const check = g.inCheck ? " · check!" : "";
      if (isHotseat()) return window.T("table.toMove","{color} to move").replace("{color}", g.turnId === g.p1Id ? window.T("xiangqi.red","Red") : window.T("xiangqi.black","Black")) + check;
      const mine = g.turnId === ctx.self();
      const who = mine ? "Your move" : ctx.name(g.turnId) + " to move";
      const you = isPlayer() ? ` · you are ${g.p1Id === ctx.self() ? "red" : "black"}` : "";
      return who + you + check;
    }

    function cellFor(rank, file) {
      const sq = rank * FILES + file;
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "xq-cell";
      if (file >= 3 && file <= 5 && (rank <= 2 || rank >= 7)) cell.classList.add("palace");
      cell.classList.add(rank <= 4 ? "half-red" : "half-black");
      const v = g.board[sq];
      if (v !== 0) {
        const p = document.createElement("span");
        p.className = "xq-piece " + (v > 0 ? "red" : "black");
        p.textContent = (v > 0 ? RED : BLACK)[Math.abs(v)];
        cell.appendChild(p);
      }
      if (sq === sel) cell.classList.add("selected");
      if (sq === g.lastFrom || sq === g.lastTo) cell.classList.add("last");
      return cell;
    }

    // Build one cell for viewport coords (vr,vc), mapping through the board
    // flip and applying source/target highlights.
    function buildCell(vr, vc, flip, hiSrc, hiTgt) {
      const rank = flip ? vr : RANKS - 1 - vr;
      const file = flip ? FILES - 1 - vc : vc;
      const sq = rank * FILES + file;
      const cell = cellFor(rank, file);
      if (hiSrc.has(sq)) cell.classList.add("source");
      if (hiTgt.has(sq)) cell.classList.add("target");
      cell.addEventListener("click", () => onSquare(sq));
      return cell;
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("xiangqi-moves"), g.history);
      const statusEl = $("xiangqi-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart","Waiting for the game to start…");
        return;
      }
      statusEl.textContent = statusText();
      $("xiangqi-resign").classList.toggle("hidden", !isPlayer() || over());

      const el = $("xiangqi-board");
      el.classList.toggle("my-turn", myTurn() && !over());
      el.innerHTML = "";
      // Flip so the local player's side sits at the bottom (black flips).
      const flip = g.p2Id === ctx.self();
      const hiSrc = sel < 0 && myTurn() && !over() ? sources() : new Set();
      const hiTgt = targets();
      for (let vr = 0; vr < RANKS; vr++) {
        for (let vc = 0; vc < FILES; vc++) {
          el.appendChild(buildCell(vr, vc, flip, hiSrc, hiTgt));
        }
      }
    }

    $("xiangqi-resign").addEventListener("click", () => {
      if (confirm("Resign Xiangqi?")) send({ type: "xiangqi.resign" });
    });

    function outcomeWon() {
      if (!isPlayer() || over() === false) return null;
      const iAmRed = g.p1Id === ctx.self();
      return g.outcome === (iAmRed ? "red wins" : "black wins");
    }

    return {
      onEvent(type, e) {
        if (type !== "xiangqi.state") return;
        const prev = g;
        g = e;
        sel = -1;
        if (prev && prev.board && g.lastTo >= 0 &&
            prev.board[g.lastTo] !== g.board[g.lastTo] && window.fx) {
          const captured = prev.board[g.lastTo] !== 0;
          captured ? window.fx.sound.capture() : window.fx.sound.move();
        }
        render();
        if (prev && prev.playing && prev.outcome === "" && over() && window.fx) {
          window.fx.celebrate($("game-xiangqi"), outcomeWon(), window.fx.result(outcomeWon(),
            { draw: false, spectator: g.outcome, hotseat: isHotseat() }));
        }
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
