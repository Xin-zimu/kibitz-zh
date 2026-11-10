// hexboard.js — the Hex module: an 11×11 rhombus, sheared row-by-row so it
// reads as the classic hexagonal connection board. Click an empty cell to place
// a stone. Red (P1) connects top↔bottom, blue (P2) connects left↔right. There
// are no draws — exactly one player ever connects their two edges.
(() => {
  "use strict";

  const N = 11;

  window.GameModules = window.GameModules || {};
  window.GameModules.hex = { label: "⬡ Hex", paneId: "game-hex", create };

  function create(ctx) {
    const { $, send } = ctx;
    let g = null;
    let visible = false;

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const isPlayer = () => isHotseat() || (g && (g.p1Id === ctx.self() || g.p2Id === ctx.self()));
    // In solo the user drives both sides, so it is always "their" turn.
    const myTurn = () => g && (isHotseat() || g.turnId === ctx.self());
    const over = () => g && g.outcome !== "";

    function statusText() {
      if (over()) return window.TO(g.outcome);
      const stone = g.turnId === g.p1Id ? "🔴" : "🔵";
      if (isHotseat()) return window.T("table.toMove","{color} to move").replace("{color}", stone);
      const mine = g.turnId === ctx.self();
      let txt = mine ? window.T("table.yourMoveColor","Your move {color}").replace("{color}", stone)
                     : window.T("table.theirMoveColor","{name}'s move {color}").replace("{name}", ctx.name(g.turnId)).replace("{color}", stone);
      if (isPlayer()) txt += window.T("table.youAre"," · you are {color}").replace("{color}", g.p1Id === ctx.self() ? "🔴" : "🔵");
      return txt;
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("hex-moves"), g.history);
      const statusEl = $("hex-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart", "Waiting for the game to start…");
        return;
      }
      statusEl.textContent = statusText();
      $("hex-resign").classList.toggle("hidden", !isPlayer() || over());
      renderBoard();
    }

    function renderBoard() {
      const el = $("hex-board");
      el.classList.toggle("my-turn", myTurn() && !over());
      el.innerHTML = "";
      const win = new Set(g.winCells || []);
      const canPlay = myTurn() && !over();
      for (let row = 0; row < N; row++) {
        const rowEl = document.createElement("div");
        rowEl.className = "hx-row";
        rowEl.style.marginLeft = `calc(var(--hx-cell) * ${row * 0.5})`;
        for (let col = 0; col < N; col++) {
          rowEl.appendChild(makeCell(row, col, win, canPlay));
        }
        el.appendChild(rowEl);
      }
    }

    function makeCell(row, col, win, canPlay) {
      const idx = row * N + col;
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "hx-cell";
      const v = g.board[idx];
      if (v !== 0) {
        const stone = document.createElement("span");
        stone.className = "hx-stone " + (v === 1 ? "red" : "blue");
        cell.appendChild(stone);
      }
      if (win.has(idx)) cell.classList.add("win");
      if (idx === g.last) cell.classList.add("last");
      if (canPlay && v === 0) {
        cell.classList.add("open");
        cell.addEventListener("click", () => send({ type: "hex.place", row, col }));
      }
      return cell;
    }

    $("hex-resign").addEventListener("click", () => {
      if (confirm("Resign Hex?")) send({ type: "hex.resign" });
    });

    function outcomeWon() {
      if (!isPlayer()) return null;
      const iAmRed = g.p1Id === ctx.self();
      return g.outcome === (iAmRed ? "red wins" : "blue wins");
    }

    return {
      onEvent(type, e) {
        if (type !== "hex.state") return;
        const prev = g;
        g = e;
        if (prev && prev.board && g.board && g.last >= 0 &&
            prev.board[g.last] === 0 && g.board[g.last] !== 0 && window.fx) {
          window.fx.sound.drop();
        }
        render();
        if (prev && prev.playing && prev.outcome === "" && over() && window.fx) {
          window.fx.celebrate($("game-hex"), outcomeWon(), window.fx.result(outcomeWon(),
            { spectator: g.outcome, hotseat: isHotseat() }));
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
