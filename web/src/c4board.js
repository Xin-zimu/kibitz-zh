// c4board.js — the Connect Four module: 7×6 grid, click a column to drop.
(() => {
  "use strict";

  const COLS = 7, ROWS = 6;

  window.GameModules = window.GameModules || {};
  window.GameModules.connect4 = { label: "🔴 Connect Four", paneId: "game-c4", create };

  function create(ctx) {
    const { $, send } = ctx;
    let g = null;
    let visible = false;
    let dropCell = -1; // board index to animate this render (the last drop)

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const isPlayer = () => isHotseat() || (g && (g.p1Id === ctx.self() || g.p2Id === ctx.self()));
    // In solo the user drives both sides, so it is always "their" turn.
    const myTurn = () => g && (isHotseat() || g.turnId === ctx.self());
    const over = () => g && g.outcome !== "";
    const myColorClass = () => (g.p1Id === ctx.self() ? "ghost-red" : "ghost-yellow");

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("c4-moves"), g.history);
      const statusEl = $("c4-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart", "Waiting for the game to start…");
        return;
      }
      statusEl.textContent = statusText();
      $("c4-resign").classList.toggle("hidden", !isPlayer() || over());
      renderBoard(myTurn() && !over());
      dropCell = -1;
    }

    function statusText() {
      if (over()) return window.TO(g.outcome);
      const color = g.turnId === g.p1Id ? "🔴" : "🟡";
      if (isHotseat()) return window.T("table.toMove", "{color} to move").replace("{color}", color);
      const mine = g.turnId === ctx.self();
      let s = mine
        ? window.T("table.yourMoveColor", "Your move {color}").replace("{color}", color)
        : window.T("table.theirMoveColor", "{name}'s move {color}").replace("{name}", ctx.name(g.turnId)).replace("{color}", color);
      if (isPlayer()) s += window.T("table.youAre", " · you are {color}").replace("{color}", g.p1Id === ctx.self() ? "🔴" : "🟡");
      return s;
    }

    function renderBoard(canPlay) {
      const el = $("c4-board");
      el.classList.toggle("my-turn", myTurn() && !over());
      el.innerHTML = "";
      const win = new Set(g.winCells || []);
      // Draw top row first: row = ROWS-1 down to 0.
      for (let row = ROWS - 1; row >= 0; row--) {
        for (let col = 0; col < COLS; col++) {
          el.appendChild(cellFor(col * ROWS + row, col, win, canPlay, el));
        }
      }
      if (canPlay) el.addEventListener("mouseleave", () => clearGhost(el), { once: true });
    }

    function cellFor(idx, col, win, canPlay, el) {
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "c4-cell";
      cell.dataset.col = col;
      const v = g.board[idx];
      if (v === 1) cell.classList.add("red");
      if (v === 2) cell.classList.add("yellow");
      if (win.has(idx)) cell.classList.add("win");
      if (idx === dropCell) cell.classList.add("dropping");
      if (canPlay) {
        cell.addEventListener("click", () => send({ type: "c4.drop", col }));
        cell.addEventListener("mouseenter", () => showGhost(el, col));
      }
      return cell;
    }

    // showGhost previews a translucent disc in the landing slot of a column.
    function showGhost(el, col) {
      clearGhost(el);
      // Landing = lowest empty cell in the column.
      let landing = -1;
      for (let row = 0; row < ROWS; row++) {
        if (g.board[col * ROWS + row] === 0) { landing = col * ROWS + row; break; }
      }
      if (landing < 0) return;
      // DOM is row-major top→bottom; the column's buttons run row5…row0, so
      // landing row r maps to DOM index (ROWS-1 - r).
      const buttons = el.querySelectorAll(`.c4-cell[data-col="${col}"]`);
      // buttons are ordered top(row5)…bottom(row0); landing row r → index (5 - r).
      const landingRow = landing % ROWS;
      const domIdx = (ROWS - 1) - landingRow;
      const target = buttons[domIdx];
      if (target && !target.classList.contains("red") && !target.classList.contains("yellow")) {
        target.classList.add(myColorClass());
      }
    }
    function clearGhost(el) {
      el.querySelectorAll(".ghost-red, .ghost-yellow").forEach((c) =>
        c.classList.remove("ghost-red", "ghost-yellow"));
    }

    $("c4-resign").addEventListener("click", () => {
      if (confirm("Resign Connect Four?")) send({ type: "c4.resign" });
    });

    function outcomeWon() {
      if (g.outcome === "draw" || !isPlayer()) return null;
      const iAmRed = g.p1Id === ctx.self();
      return g.outcome === (iAmRed ? "red wins" : "yellow wins");
    }

    return {
      onEvent(type, e) {
        if (type !== "connect4.state") return;
        const prev = g;
        g = e;
        // Find the freshly dropped disc (a cell that changed from empty).
        if (prev && prev.board && g.board) {
          for (let i = 0; i < g.board.length; i++) {
            if (prev.board[i] === 0 && g.board[i] !== 0) { dropCell = i; break; }
          }
          if (dropCell >= 0 && window.fx) window.fx.sound.drop();
        }
        render();
        if (prev && prev.playing && !over_(prev) && over() && window.fx) {
          window.fx.celebrate($("game-c4"), outcomeWon(), window.fx.result(outcomeWon(),
            { draw: g.outcome === "draw", spectator: g.outcome, hotseat: isHotseat() }));
        }
      },
      setVisible(v) { visible = v; if (v) render(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (over()) return { status: "over", detail: g.outcome };
        return { status: "live", myTurn: myTurn() };
      },
    };

    function over_(s) { return s && s.outcome !== ""; }
  }
})();
