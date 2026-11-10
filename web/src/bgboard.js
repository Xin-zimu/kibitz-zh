// bgboard.js — the backgammon module: pure-DOM board plus input/render glue.
// Zero rules: the WASM core supplies legal turns; this file draws points,
// stacks checkers, builds a turn hop by hop, and forwards intents.
//
// Board layout (white's global numbering — the shared orientation):
//
//   13 14 15 16 17 18 | bar | 19 20 21 22 23 24     off trays →
//   12 11 10  9  8  7 | bar |  6  5  4  3  2  1
(() => {
  "use strict";

  window.GameModules = window.GameModules || {};
  window.GameModules.bg = { label: "⚅ Backgammon", paneId: "game-bg", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;        // last bg.state
    let pending = [];    // hops (player-relative) being built
    let from = null;     // selected source (global)
    let visible = false;
    let popPoints = new Set(); // global points whose checkers just changed
    let diceRolled = false;    // fresh dice this render → tumble animation

    function isHotseat() { return ctx.hotseat && ctx.hotseat(); }
    function isWhite() { return g && g.whiteId === ctx.self(); }            // self's colour
    function moverIsWhite() { return g && g.turnId === g.whiteId; }         // colour to move
    // Coordinate mapping / pending preview use the on-turn side's perspective.
    // Normally that's you (you only move on your turn); in solo you drive both,
    // so it follows the side to move.
    function persWhite() { return isHotseat() ? moverIsWhite() : isWhite(); }
    function isPlayer() { return isHotseat() || (g && (g.whiteId === ctx.self() || g.blackId === ctx.self())); }
    function myTurn() { return g && (isHotseat() || g.turnId === ctx.self()); }
    function bgWon() {
      if (!isPlayer()) return null;
      if (g.outcome.startsWith("white")) return isWhite();
      if (g.outcome.startsWith("black")) return !isWhite();
      return null;
    }

    function relToGlobal(rel) { return (rel === 25 || rel === 0) ? rel : (persWhite() ? rel : 25 - rel); }
    function globalToRel(p) { return (p === 25 || p === 0) ? p : (persWhite() ? p : 25 - p); }

    function candidates() {
      return (g.legal || []).filter((turn) =>
        turn.length >= pending.length &&
        pending.every((h, i) => turn[i][0] === h[0] && turn[i][1] === h[1]));
    }
    function options() {
      const at = pending.length;
      const opts = [];
      for (const turn of candidates()) {
        if (turn.length > at) opts.push({ from: turn[at][0], to: turn[at][1] });
      }
      return opts;
    }

    // Display board = confirmed position + pending hops applied locally.
    function previewBoard() {
      const st = {
        points: [...g.points],
        barW: g.barW, barB: g.barB, offW: g.offW, offB: g.offB,
      };
      const sign = persWhite() ? 1 : -1;
      for (const [f, to] of pending) {
        removeFrom(st, sign, f);
        addTo(st, sign, to);
      }
      return st;
    }

    function removeFrom(st, sign, f) {
      if (f === 25) { if (persWhite()) st.barW--; else st.barB--; }
      else st.points[relToGlobal(f)] -= sign;
    }

    function addTo(st, sign, to) {
      if (to === 0) { if (persWhite()) st.offW++; else st.offB++; return; }
      const gp = relToGlobal(to);
      if (st.points[gp] === -sign) { // lone opposing blot: hit
        st.points[gp] = 0;
        if (persWhite()) st.barB++; else st.barW++;
      }
      st.points[gp] += sign;
    }

    function onPoint(p) {
      if (!g || g.phase !== "moving" || !myTurn() || !isPlayer()) return;
      if (from !== null && p !== from) {
        const match = options().find((o) => o.from === globalToRel(from) && o.to === globalToRel(p));
        if (match) {
          pending.push([match.from, match.to]);
          from = null;
          const cands = candidates();
          if (cands.length > 0 && cands[0].length === pending.length) {
            send({ type: "bg.move", hops: pending });
          }
          renderPane();
          return;
        }
      }
      const sources = new Set(options().map((o) => relToGlobal(o.from)));
      from = sources.has(p) ? p : null;
      renderPane();
    }

    // --- DOM builders --------------------------------------------------------

    function render(el, st, hi) {
      el.innerHTML = "";
      const top = [13, 14, 15, 16, 17, 18, -1, 19, 20, 21, 22, 23, 24];
      const bottom = [12, 11, 10, 9, 8, 7, -1, 6, 5, 4, 3, 2, 1];
      el.appendChild(row(top, true, st, hi));
      el.appendChild(row(bottom, false, st, hi));
      el.appendChild(trays(st, hi));
    }

    function row(pointNums, isTop, st, hi) {
      const r = document.createElement("div");
      r.className = "bg-row" + (isTop ? " top" : " bottom");
      for (const p of pointNums) {
        r.appendChild(p === -1 ? barCell(isTop, st, hi) : pointCell(p, isTop, st, hi));
      }
      return r;
    }

    function pointCell(p, isTop, st, hi) {
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "bg-point " + (p % 2 === (isTop ? 0 : 1) ? "odd" : "even") + (isTop ? " top" : " bottom");
      cell.dataset.point = String(p);
      if (hi.sources.has(p)) cell.classList.add("source");
      if (hi.targets.has(p)) cell.classList.add("target");
      const popThis = popPoints.has(p);
      const n = st.points[p];
      const count = Math.abs(n);
      const color = n > 0 ? "w" : "b";
      for (let i = 0; i < Math.min(count, 5); i++) {
        const c = document.createElement("span");
        c.className = "checker " + color;
        if (popThis && i === Math.min(count, 5) - 1) c.classList.add("pop");
        cell.appendChild(c);
      }
      if (count > 5) {
        const more = document.createElement("span");
        more.className = "count";
        more.textContent = String(count);
        cell.appendChild(more);
      }
      const label = document.createElement("span");
      label.className = "pt-label";
      label.textContent = String(p);
      cell.appendChild(label);
      cell.addEventListener("click", () => onPoint(p));
      return cell;
    }

    function barCell(isTop, st, hi) {
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "bg-bar";
      if (hi.sources.has(25)) cell.classList.add("source");
      const n = isTop ? st.barB : st.barW;
      const color = isTop ? "b" : "w";
      for (let i = 0; i < Math.min(n, 4); i++) {
        const c = document.createElement("span");
        c.className = "checker " + color;
        cell.appendChild(c);
      }
      if (n > 4) {
        const more = document.createElement("span");
        more.className = "count";
        more.textContent = String(n);
        cell.appendChild(more);
      }
      cell.addEventListener("click", () => onPoint(25));
      return cell;
    }

    function trays(st, hi) {
      const t = document.createElement("div");
      t.className = "bg-trays";
      for (const [who, n] of [["b", st.offB], ["w", st.offW]]) {
        const tray = document.createElement("button");
        tray.type = "button";
        tray.className = "bg-tray " + who;
        if (hi.targets.has(0) && hi.mover === who) tray.classList.add("target");
        tray.textContent = window.T("bg.off","{color} off: {n}").replace("{color}", who === "w" ? "⚪" : "⚫").replace("{n}", n);
        tray.addEventListener("click", () => onPoint(0));
        t.appendChild(tray);
      }
      return t;
    }

    // --- pane ----------------------------------------------------------------

    function renderPane() {
      if (!visible || !g) return;
      ctx.renderMoves($("bg-moves"), g.history);
      const statusEl = $("bg-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart","Waiting for the game to start…");
        $("bg-roll").classList.add("hidden");
        return;
      }
      statusEl.textContent = statusText();
      updateDice();
      updateControls();
      const hi = computeHighlights();
      render($("bg-board"), previewBoard(), hi);
      popPoints = new Set();
      diceRolled = false;
    }

    function rollStatus(moverIcon) {
      if (isHotseat()) return window.T("bg.toRoll","{color} to roll").replace("{color}", moverIcon);
      return myTurn() ? window.T("bg.yourRoll","Your roll") : window.T("bg.waitingRoll","Waiting for opponent to roll");
    }

    function statusWord() {
      const moverIcon = moverIsWhite() ? "⚪" : "⚫";
      if (g.phase === "over") return window.TO(g.outcome);
      if (g.phase === "rolling") return rollStatus(moverIcon);
      if (g.phase === "handshake") return window.T("bg.rolling","Rolling…");
      if (isHotseat()) return window.T("table.toMove","{color} to move").replace("{color}", moverIcon);
      if (myTurn()) return window.T("table.yourMove","Your move");
      if (isPlayer()) return window.T("table.theirTurn","{name} to move").replace("{name}", ctx.name(g.turnId));
      return window.T("table.kibitzing","Kibitzing");
    }

    function statusText() {
      const suffix = (isPlayer() && !isHotseat())
        ? window.T("table.youAre"," · you are {color}").replace("{color}", isWhite() ? "⚪" : "⚫") : "";
      return window.T("bg.status","{word} · pips ⚪{w} ⚫{b}{suffix}")
        .replace("{word}", statusWord()).replace("{w}", g.pipsW).replace("{b}", g.pipsB).replace("{suffix}", suffix);
    }

    function updateDice() {
      const faces = ["", "⚀", "⚁", "⚂", "⚃", "⚄", "⚅"];
      const diceEl = $("bg-dice");
      diceEl.textContent = g.phase === "moving"
        ? faces[g.dice[0]] + faces[g.dice[1]] + (g.dice[0] === g.dice[1] ? " ×4" : "")
        : "";
      if (diceRolled && g.phase === "moving") {
        diceEl.classList.remove("rolled");
        void diceEl.offsetWidth; // reflow so the animation re-triggers
        diceEl.classList.add("rolled");
      }
    }

    function updateControls() {
      $("bg-board").classList.toggle("my-turn", isPlayer() && myTurn() && g.phase !== "over" && g.phase !== "handshake");
      $("bg-roll").classList.toggle("hidden", !(g.phase === "rolling" && myTurn() && isPlayer()));
      $("bg-undo").classList.toggle("hidden", pending.length === 0);
      $("bg-resign").classList.toggle("hidden", !isPlayer() || g.phase === "over");
    }

    function computeHighlights() {
      const hi = { sources: new Set(), targets: new Set(), mover: persWhite() ? "w" : "b" };
      if (g.phase === "moving" && myTurn()) {
        for (const o of options()) {
          const src = relToGlobal(o.from);
          if (from === null) {
            hi.sources.add(src);
          } else if (src === from) {
            hi.targets.add(relToGlobal(o.to));
            hi.sources.add(src);
          }
        }
      }
      return hi;
    }

    // one-time control wiring
    $("bg-roll").addEventListener("click", () => send({ type: "bg.roll" }));
    $("bg-undo").addEventListener("click", () => {
      pending = [];
      from = null;
      renderPane();
    });
    $("bg-resign").addEventListener("click", () => {
      if (confirm("Resign the backgammon game?")) send({ type: "bg.resign" });
    });

    function diffState(prev) {
      for (let p = 1; p <= 24; p++) {
        if (prev.points[p] !== g.points[p]) popPoints.add(p);
      }
      const diceNow = g.phase === "moving" && g.dice[0] >= 1;
      const diceChanged = !prev.dice || prev.dice[0] !== g.dice[0] ||
        prev.dice[1] !== g.dice[1] || prev.phase !== "moving";
      diceRolled = diceNow && diceChanged;
      if (window.fx) {
        if (diceRolled) window.fx.sound.turn();
        else if (popPoints.size) window.fx.sound.move();
      }
    }

    function applyState(e) {
      const prev = g;
      g = e;
      pending = [];
      from = null;
      popPoints = new Set();
      diceRolled = false;
      if (prev && prev.points) diffState(prev);
      renderPane();
      if (prev && prev.playing && prev.phase !== "over" && g.phase === "over" && window.fx) {
        window.fx.celebrate($("game-bg"), bgWon(), window.fx.result(bgWon(),
          { spectator: g.outcome, hotseat: isHotseat() }));
      }
    }

    return {
      onEvent(type, e) {
        switch (type) {
          case "bg.state":
            applyState(e);
            break;
          case "bg.danced":
            toast(e.by === ctx.self() ? window.T("bg.noMovesYou","No legal moves — turn passed.")
                                  : window.T("bg.noMovesThem","Opponent danced (no legal moves)."));
            break;
        }
      },
      setVisible(v) { visible = v; if (v) renderPane(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (g.phase === "over") return { status: "over", detail: g.outcome };
        return { status: "live", myTurn: myTurn() };
      },
    };
  }
})();
