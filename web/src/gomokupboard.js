// gomokupboard.js — Gomoku Party: five-in-a-row for 2–4 players on a shared
// 19×19 board with rotating turns. Each seat has a color; click an empty point
// on your turn to place. Networked-only (no solo). Hidden nothing — every end
// runs the same board and verifies a position hash, so no one can cheat.
(() => {
  "use strict";

  const SIZE = 19;
  // Seat colors, in seat order — matches the service's 🔴🔵🟢🟡 notation glyphs.
  const COLORS = ["#e5484d", "#4c8bf5", "#30a46c", "#f5d90a"];
  const NAMES = ["red", "blue", "green", "yellow"];

  window.GameModules = window.GameModules || {};
  window.GameModules.gomokup = { label: "🌈 Gomoku Party", paneId: "game-gomokup", create };

  function create(ctx) {
    const { $, send } = ctx;
    let g = null;
    let visible = false;

    const seats = () => (g && g.seats) || [];
    const mySeat = () => seats().indexOf(ctx.self());
    const isPlayer = () => mySeat() >= 0;
    const myTurn = () => g && g.playing && g.turnId === ctx.self();
    const over = () => g && g.outcome !== "";
    // The host reopens the lobby; a seated player asks the host to. Anyone else
    // is a kibitzer and cannot seat a table (the service enforces the same).
    const canStart = () => ctx.role() === "host" || isPlayer();

    // ---- status + outcome ---------------------------------------------------

    function outcomeText() {
      if (g.draw) return window.T("gomokup.drawFull","Draw — the board filled");
      if (g.winnerId) {
        return g.winnerId === ctx.self()
          ? window.T("gomokup.youWin","You win! 🎉")
          : window.T("gomokup.wins","{name} wins").replace("{name}", ctx.name(g.winnerId));
      }
      return window.T("outcome.abandoned","Game abandoned");
    }

    function statusText() {
      if (over()) return outcomeText();
      if (myTurn()) return window.T("gomokup.yourTurn","Your turn — place a stone");
      return window.T("gomokup.theirTurn","{name}'s turn").replace("{name}", ctx.name(g.turnId));
    }

    function dot(color) {
      const d = document.createElement("span");
      d.className = "gp-dot";
      d.style.background = color;
      return d;
    }

    // ---- player roster ------------------------------------------------------

    function renderPlayers() {
      const el = $("gomokup-players");
      if (!el) return;
      el.innerHTML = "";
      const claimed = seats();
      claimed.forEach((id, i) => {
        const row = document.createElement("span");
        row.className = "gp-player";
        if (!over() && id === g.turnId) row.classList.add("on-turn");
        if (g.gone && g.gone[i]) row.classList.add("gone");
        row.append(dot(COLORS[i] || "#888"));
        const nm = document.createElement("span");
        nm.textContent = id === ctx.self()
          ? window.T("table.youSuffix","{name} (you)").replace("{name}", ctx.name(id))
          : ctx.name(id);
        row.appendChild(nm);
        el.appendChild(row);
      });
      if (g.lobby) {
        for (let i = claimed.length; i < (g.maxSeats || 4); i++) {
          const slot = document.createElement("span");
          slot.className = "gp-player gp-empty";
          slot.textContent = window.T("gomokup.openSeat","＋ open seat");
          el.appendChild(slot);
        }
      }
    }

    // ---- lobby --------------------------------------------------------------

    function lobbyBtn(text, onClick, primary) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "gp-lobby-btn" + (primary ? " primary" : "");
      b.textContent = text;
      b.addEventListener("click", onClick);
      return b;
    }

    function note(text) {
      const d = document.createElement("div");
      d.className = "gp-note";
      d.textContent = text;
      return d;
    }

    function renderLobbyControls() {
      const el = $("gomokup-lobby");
      if (!el) return;
      el.innerHTML = "";
      const claimed = seats();
      if (isPlayer()) {
        el.appendChild(lobbyBtn(window.T("gomokup.leaveSeat","Leave seat"), () => send({ type: "gomokup.leaveSeat" })));
      } else if (claimed.length < (g.maxSeats || 4)) {
        el.appendChild(lobbyBtn(window.T("gomokup.takeSeat","Take a seat"), () => send({ type: "gomokup.takeSeat" })));
      } else {
        el.appendChild(note(window.T("gomokup.tableFull","Table full — you're watching")));
      }
      if (g.canBegin) {
        el.appendChild(lobbyBtn(window.T("gomokup.beginGame","Begin game ▶"), () => send({ type: "gomokup.begin" }), true));
      } else {
        el.appendChild(note(claimed.length < 2
          ? window.T("gomokup.needTwo","Need at least two seated to begin…")
          : window.T("gomokup.waitingBegin","Waiting for the host to begin…")));
      }
    }

    // ---- board --------------------------------------------------------------

    // renderRematch mirrors Gomoku: a "next game" button for the host or a
    // seated player, and an explanation for a kibitzer.
    function renderRematch(finished) {
      $("gomokup-rematch").classList.toggle("hidden", !(finished && canStart()));
      $("gomokup-rematch-hint").classList.toggle("hidden", !(finished && !canStart()));
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("gomokup-moves"), g.history);
      const statusEl = $("gomokup-status");
      const lobby = !!g.lobby;
      $("gomokup-lobby").classList.toggle("hidden", !lobby);
      $("gomokup-board").classList.toggle("hidden", lobby);
      renderPlayers();
      if (lobby) {
        statusEl.textContent = window.T("gomokup.openTable","Open table — take a seat to play (2–4 players)");
        renderLobbyControls();
        $("gomokup-resign").classList.add("hidden");
        renderRematch(false);
        return;
      }
      if (!g.playing && !over()) {
        statusEl.textContent = window.T("gomokup.waitingHost","Waiting for the host to open a table…");
        renderRematch(false);
        return;
      }
      statusEl.textContent = statusText();
      $("gomokup-resign").classList.toggle("hidden", !isPlayer() || over());
      renderRematch(over());
      renderBoard(myTurn() && !over());
    }

    function renderBoard(canPlay) {
      const el = $("gomokup-board");
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
      cell.className = "gp-cell";
      const v = g.board[idx];
      if (v !== 0) {
        const stone = document.createElement("span");
        stone.className = "gp-stone";
        stone.style.background = COLORS[v - 1] || "#888";
        cell.appendChild(stone);
      }
      if (win.has(idx)) cell.classList.add("win");
      if (idx === g.last) cell.classList.add("last");
      if (canPlay && v === 0) {
        const row = Math.floor(idx / SIZE), col = idx % SIZE;
        cell.classList.add("open");
        cell.addEventListener("click", () => send({ type: "gomokup.place", row, col }));
      }
      return cell;
    }

    const resignBtn = $("gomokup-resign");
    if (resignBtn) {
      resignBtn.addEventListener("click", () => {
        if (confirm(window.T("gomokup.resignConfirm","Resign Gomoku Party?"))) send({ type: "gomokup.resign" });
      });
    }
    // Rematch reopens the seating lobby (start path shared with the picker).
    $("gomokup-rematch").addEventListener("click", () => send({ type: "game.start", game: "gomokup" }));

    function won() {
      if (g.draw || !isPlayer()) return null;
      if (g.winnerId) return g.winnerId === ctx.self();
      return null; // abandoned
    }

    return {
      onEvent(type, e) {
        if (type !== "gomokup.state") return;
        const prev = g;
        g = e;
        if (prev && prev.board && g.board && g.last >= 0 &&
            prev.board[g.last] === 0 && g.board[g.last] !== 0 && window.fx) {
          window.fx.sound.drop();
        }
        render();
        if (prev && prev.playing && !overOf(prev) && over() && window.fx) {
          window.fx.celebrate($("game-gomokup"), won(),
            window.fx.result(won(), { draw: g.draw, spectator: g.outcome }));
        }
      },
      setVisible(v) { visible = v; if (v) render(); },
      card() {
        if (!g) return { status: "idle" };
        if (g.lobby) return { status: "lobby" };
        if (!g.playing) return { status: "idle" };
        if (over()) return { status: "over", detail: outcomeText() };
        return { status: "live", myTurn: myTurn() };
      },
    };

    function overOf(s) { return s && s.outcome !== ""; }
  }

  void NAMES; // reserved for aria labels
})();
