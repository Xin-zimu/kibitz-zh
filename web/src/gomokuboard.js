// gomokuboard.js — the Gomoku module: 15×15 grid, click an empty point to
// place a stone; first to five in a row wins.
(() => {
  "use strict";

  const SIZE = 15;

  window.GameModules = window.GameModules || {};
  window.GameModules.gomoku = { label: "⚫ Gomoku", paneId: "game-gomoku", create };

  function create(ctx) {
    const { $, send } = ctx;
    let g = null;
    let visible = false;

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const isPlayer = () => isHotseat() || (g && (g.p1Id === ctx.self() || g.p2Id === ctx.self()));
    // In solo the user drives both sides, so it is always "their" turn.
    const myTurn = () => g && (isHotseat() || g.turnId === ctx.self());
    const over = () => g && g.outcome !== "";
    // Only the host and the seated opponent may start the next game (the same
    // rule the service enforces in game.Table.AuthorizeStart); a kibitzer can
    // watch but not seat players.
    const canStart = () => ctx.role() === "host" || isPlayer();

    function statusText() {
      if (over()) return window.TO(g.outcome);
      const stone = g.turnId === g.p1Id ? "⚫" : "⚪";
      if (isHotseat()) return window.T("table.toMove","{color} to move").replace("{color}", stone);
      const mine = g.turnId === ctx.self();
      let s = mine ? window.T("table.yourMoveColor","Your move {color}").replace("{color}", stone)
                   : window.T("table.theirMoveColor","{name}'s move {color}").replace("{name}", ctx.name(g.turnId)).replace("{color}", stone);
      if (isPlayer()) s += window.T("table.youAre"," · you are {color}").replace("{color}", g.p1Id === ctx.self() ? "⚫" : "⚪");
      return s;
    }

    // renderRematch offers "next game" in the pane itself once a game is over:
    // a button for the host/opponent, and a plain explanation for a kibitzer so
    // the end of a game never reads as "the only way out is to leave".
    function renderRematch(finished) {
      $("gomoku-rematch").classList.toggle("hidden", !(finished && canStart()));
      $("gomoku-rematch-hint").classList.toggle("hidden", !(finished && !canStart()));
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("gomoku-moves"), g.history);
      const statusEl = $("gomoku-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart", "Waiting for the game to start…");
        renderRematch(false);
        return;
      }
      statusEl.textContent = statusText();
      $("gomoku-resign").classList.toggle("hidden", !isPlayer() || over());
      renderRematch(over());
      renderBoard(myTurn() && !over());
    }

    function renderBoard(canPlay) {
      const el = $("gomoku-board");
      el.classList.toggle("my-turn", canPlay);
      el.innerHTML = "";
      const win = new Set(g.winCells || []);
      for (let idx = 0; idx < SIZE * SIZE; idx++) {
        el.appendChild(cellFor(idx, win, canPlay));
      }
    }

    function cellFor(idx, win, canPlay) {
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "gm-cell";
      const v = g.board[idx];
      if (v !== 0) {
        const stone = document.createElement("span");
        stone.className = "gm-stone " + (v === 1 ? "black" : "white");
        cell.appendChild(stone);
      }
      if (win.has(idx)) cell.classList.add("win");
      if (idx === g.last) cell.classList.add("last");
      if (canPlay && v === 0) {
        const row = Math.floor(idx / SIZE), col = idx % SIZE;
        cell.classList.add("open");
        cell.addEventListener("click", () => send({ type: "gomoku.place", row, col }));
      }
      return cell;
    }

    $("gomoku-resign").addEventListener("click", () => {
      if (confirm("Resign Gomoku?")) send({ type: "gomoku.resign" });
    });
    // Rematch reuses the picker's start path: the host seats players directly,
    // a seated opponent's request is relayed to the host as a startReq.
    $("gomoku-rematch").addEventListener("click", () => send({ type: "game.start", game: "gomoku" }));

    function outcomeWon() {
      if (g.outcome === "draw" || !isPlayer()) return null;
      const iAmBlack = g.p1Id === ctx.self();
      return g.outcome === (iAmBlack ? "black wins" : "white wins");
    }

    return {
      onEvent(type, e) {
        if (type !== "gomoku.state") return;
        const prev = g;
        g = e;
        if (prev && prev.board && g.board && g.last >= 0 &&
            prev.board[g.last] === 0 && g.board[g.last] !== 0 && window.fx) {
          window.fx.sound.drop();
        }
        render();
        if (prev && prev.playing && !over_(prev) && over() && window.fx) {
          window.fx.celebrate($("game-gomoku"), outcomeWon(), window.fx.result(outcomeWon(),
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
