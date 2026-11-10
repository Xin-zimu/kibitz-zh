// checkersboard.js — the checkers module: 8×8 board over the 32-dark-square
// state, click-click path building against the core's legal move set
// (multi-jumps by prefix filtering, the backgammon trick).
(() => {
  "use strict";

  window.GameModules = window.GameModules || {};
  window.GameModules.checkers = { label: "⛀ Checkers", paneId: "game-checkers", create };

  // dark-square index ↔ (row, col)
  const rowOf = (s) => Math.floor(s / 4);
  const colOf = (s) => (rowOf(s) % 2 === 0 ? 2 * (s % 4) + 1 : 2 * (s % 4));

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;
    let path = [];      // square indices being built
    let visible = false;
    // one-shot animation data (computed on state change, consumed by render)
    let anim = { slide: null, captured: new Set(), crowned: -1 };

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const isPlayer = () => isHotseat() || (g && (g.p1Id === ctx.self() || g.p2Id === ctx.self()));
    const myTurn = () => g && (isHotseat() || g.turnId === ctx.self());
    const over = () => g && g.outcome !== "";

    function candidates() {
      return (g.legal || []).filter((m) =>
        m.length >= path.length && path.every((s, i) => m[i] === s));
    }
    function nextSquares() {
      const at = path.length;
      const out = new Set();
      for (const m of candidates()) if (m.length > at) out.add(m[at]);
      return out;
    }
    function sources() {
      const out = new Set();
      for (const m of g.legal || []) out.add(m[0]);
      return out;
    }

    function onSquare(s) {
      if (!g || !myTurn() || over() || !isPlayer()) return;
      if (path.length > 0 && nextSquares().has(s)) {
        path.push(s);
        const cands = candidates();
        if (cands.some((m) => m.length === path.length)) {
          send({ type: "checkers.move", path });
          path = [];
        }
        render();
        return;
      }
      path = sources().has(s) ? [s] : [];
      render();
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("checkers-moves"), g.history);
      const statusEl = $("checkers-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart","Waiting for the game to start…");
        return;
      }
      statusEl.textContent = statusText();
      $("checkers-resign").classList.toggle("hidden", !isPlayer() || over());
      $("checkers-draw").classList.toggle("hidden", !isPlayer() || over() || (ctx.vsBot && ctx.vsBot()));
      renderBoard();
    }

    function statusText() {
      if (over()) return g.outcome;
      if (isHotseat()) return window.T("table.toMove","{color} to move").replace("{color}", g.turnId === g.p1Id ? window.T("checkers.dark","Dark") : window.T("checkers.light","Light"));
      return (myTurn() ? "Your move" : ctx.name(g.turnId) + " to move") +
        (isPlayer() ? ` · you are ${g.p1Id === ctx.self() ? "dark" : "light"}` : "");
    }

    function renderBoard() {
      const el = $("checkers-board");
      el.classList.toggle("my-turn", myTurn() && !over());
      el.innerHTML = "";
      const hiNext = path.length ? nextSquares() : new Set();
      const hiSrc = path.length === 0 && myTurn() ? sources() : new Set();
      const flip = g.p1Id === ctx.self();
      for (let vr = 0; vr < 8; vr++) {
        for (let vc = 0; vc < 8; vc++) {
          el.appendChild(cellFor(vr, vc, flip, hiNext, hiSrc));
        }
      }
      applySlide(el);
      anim = { slide: null, captured: new Set(), crowned: -1 };
    }

    function cellFor(vr, vc, flip, hiNext, hiSrc) {
      const r = flip ? 7 - vr : vr;
      const c = flip ? 7 - vc : vc;
      const cell = document.createElement("button");
      cell.type = "button";
      const dark = (r + c) % 2 === 1;
      cell.className = "ck-cell " + (dark ? "dark" : "light");
      if (dark) fillDark(cell, r, c, hiNext, hiSrc);
      return cell;
    }

    function fillDark(cell, r, c, hiNext, hiSrc) {
      const s = r % 2 === 0 ? r * 4 + (c - 1) / 2 : r * 4 + c / 2;
      cell.dataset.sq = s;
      const v = g.board[s];
      if (v !== 0) cell.appendChild(pieceFor(v, s));
      if (anim.captured.has(s)) cell.classList.add("captured");
      if (path.includes(s)) cell.classList.add("selected");
      if (hiNext.has(s)) cell.classList.add("target");
      if (hiSrc.has(s)) cell.classList.add("source");
      cell.addEventListener("click", () => onSquare(s));
    }

    function pieceFor(v, s) {
      const piece = document.createElement("span");
      piece.className = "ck-piece " + (v > 0 ? "p1" : "p2");
      piece.textContent = Math.abs(v) === 2 ? "♛" : "";
      if (s === anim.crowned) piece.classList.add("crowned");
      return piece;
    }

    // Slide the moved piece from its origin to its destination.
    function applySlide(el) {
      if (anim.slide && window.fx) {
        const fromCell = el.querySelector(`.ck-cell[data-sq="${anim.slide.from}"]`);
        const toPiece = el.querySelector(`.ck-cell[data-sq="${anim.slide.to}"] .ck-piece`);
        if (fromCell && toPiece) {
          const a = fromCell.getBoundingClientRect();
          const b = toPiece.closest(".ck-cell").getBoundingClientRect();
          window.fx.slideFrom(toPiece, a.left - b.left, a.top - b.top);
        }
      }
    }

    $("checkers-resign").addEventListener("click", () => {
      if (confirm("Resign checkers?")) send({ type: "checkers.resign" });
    });
    $("checkers-draw").addEventListener("click", () => send({ type: "checkers.offerDraw" }));
    $("checkers-agree-draw").addEventListener("click", () => send({ type: "checkers.agreeDraw" }));

    function outcomeWon() {
      if (!isPlayer() || g.outcome === "draw") return null;
      const iAmDark = g.p1Id === ctx.self();
      return g.outcome.startsWith(iAmDark ? "black wins" : "white wins");
    }

    // computeAnim diffs prev→new board to drive slide / capture / crown fx.
    function computeAnim(prev) {
      anim = { slide: null, captured: new Set(), crowned: -1 };
      if (!prev || !prev.board || !g.lastPath || g.lastPath.length < 2) return;
      const p = g.lastPath;
      const from = p[0], to = p[p.length - 1];
      anim.slide = { from, to };
      let capture = false;
      for (let s = 0; s < 32; s++) {
        if (prev.board[s] !== 0 && g.board[s] === 0 && s !== from) {
          anim.captured.add(s);
          capture = true;
        }
      }
      if (Math.abs(prev.board[from]) === 1 && Math.abs(g.board[to]) === 2) anim.crowned = to;
      if (window.fx) capture ? window.fx.sound.capture() : window.fx.sound.move();
    }

    return {
      onEvent(type, e) {
        switch (type) {
          case "checkers.state": {
            const prev = g;
            g = e;
            path = [];
            $("checkers-agree-draw").classList.add("hidden");
            computeAnim(prev);
            render();
            if (prev && prev.playing && prev.outcome === "" && over() && window.fx) {
              window.fx.celebrate($("game-checkers"), outcomeWon(), window.fx.result(outcomeWon(),
                { draw: g.outcome === "draw", spectator: g.outcome, hotseat: isHotseat() }));
            }
            break;
          }
          case "checkers.drawOffered":
            if (isPlayer()) {
              $("checkers-agree-draw").classList.remove("hidden");
              toast(window.T("table.drawOffered","Draw offered — accept?"));
            }
            break;
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
