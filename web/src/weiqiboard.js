// weiqiboard.js — the Go (Weiqi) module: 9×9 board, click an empty legal point
// to place a stone, or pass. Captures and legality (suicide/ko) are computed by
// the core; two passes end the game and the board is scored (area/Chinese,
// komi 6.5 to white). The UI just renders State.
(() => {
  "use strict";

  const SIZE = 9;

  window.GameModules = window.GameModules || {};
  window.GameModules.weiqi = { label: "🟢 Go", paneId: "game-weiqi", create };

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
      const caps = window.T("weiqi.captures"," · captures ⚫{b} ⚪{w}").replace("{b}", g.capturesB).replace("{w}", g.capturesW);
      if (over()) {
        return `${window.TO(g.outcome)} · ⚫${fmt(g.scoreB)} ⚪${fmt(g.scoreW)}`;
      }
      const stone = g.turnId === g.p1Id ? "⚫" : "⚪";
      if (isHotseat()) return window.T("table.toMove", "{color} to move").replace("{color}", stone) + caps;
      const mine = g.turnId === ctx.self();
      let s = mine ? window.T("table.yourMoveColor","Your move {color}").replace("{color}", stone)
                   : window.T("table.theirMoveColor","{name}'s move {color}").replace("{name}", ctx.name(g.turnId)).replace("{color}", stone);
      if (isPlayer()) s += window.T("table.youAre"," · you are {color}").replace("{color}", g.p1Id === ctx.self() ? "⚫" : "⚪");
      return s + caps;
    }

    function fmt(n) { return Number.isInteger(n) ? String(n) : n.toFixed(1); }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("weiqi-moves"), g.history);
      const statusEl = $("weiqi-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart", "Waiting for the game to start…");
        return;
      }
      statusEl.textContent = statusText();

      const canPlay = myTurn() && !over();
      $("weiqi-resign").classList.toggle("hidden", !isPlayer() || over());
      $("weiqi-pass").classList.toggle("hidden", !canPlay);
      renderBoard(canPlay);
    }

    function renderBoard(canPlay) {
      const el = $("weiqi-board");
      el.classList.toggle("my-turn", canPlay);
      el.innerHTML = "";
      const legal = new Set(canPlay ? (g.legal || []) : []);
      for (let idx = 0; idx < SIZE * SIZE; idx++) {
        el.appendChild(cellFor(idx, legal));
      }
    }

    function cellFor(idx, legal) {
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "go-cell";
      const v = g.board[idx];
      if (v !== 0) {
        const stone = document.createElement("span");
        stone.className = "go-stone " + (v === 1 ? "black" : "white");
        cell.appendChild(stone);
      }
      if (idx === g.last) cell.classList.add("last");
      if (legal.has(idx)) {
        const row = Math.floor(idx / SIZE), col = idx % SIZE;
        cell.classList.add("open");
        cell.addEventListener("click", () => send({ type: "weiqi.place", row, col }));
      }
      return cell;
    }

    $("weiqi-resign").addEventListener("click", () => {
      if (confirm("Resign Go?")) send({ type: "weiqi.resign" });
    });
    $("weiqi-pass").addEventListener("click", () => send({ type: "weiqi.pass" }));

    function outcomeWon() {
      if (!isPlayer()) return null;
      const iAmBlack = g.p1Id === ctx.self();
      return g.outcome === (iAmBlack ? "black wins" : "white wins");
    }

    return {
      onEvent(type, e) {
        if (type !== "weiqi.state") return;
        const prev = g;
        g = e;
        if (prev && prev.board && g.board && g.last >= 0 &&
            prev.board[g.last] === 0 && g.board[g.last] !== 0 && window.fx) {
          window.fx.sound.drop();
        }
        render();
        if (prev && prev.playing && prev.outcome === "" && over() && window.fx) {
          window.fx.celebrate($("game-weiqi"), outcomeWon(), window.fx.result(outcomeWon(),
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
