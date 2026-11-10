// dotsboard.js — the Dots and Boxes module: a 6×6 grid of dots with clickable
// edge segments between them. Draw an edge; completing a box claims it and
// grants another turn. Most boxes wins. Red = P1, Blue = P2.
//
// The board is laid out as an (2*DOTS-1)×(2*DOTS-1) CSS grid. Even/even cells
// are dots, even/odd are horizontal edges, odd/even are vertical edges, and
// odd/odd are box interiors. Edge ids mirror the Go engine:
//   horizontal: id = r*BOXES + c        (0..29)
//   vertical:   id = HEDGES + r*DOTS + c (30..59)
(() => {
  "use strict";

  const BOXES = 5, DOTS = 6, HEDGES = 30;
  const N = 2 * DOTS - 1; // 11 grid tracks per side

  window.GameModules = window.GameModules || {};
  window.GameModules.dots = { label: "🔳 Dots & Boxes", paneId: "game-dots", create };

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
      const glyph = g.turnId === g.p1Id ? "🟥" : "🟦";
      let s;
      if (isHotseat()) {
        s = window.T("table.toMove","{color} to move").replace("{color}", glyph);
      } else {
        const mine = g.turnId === ctx.self();
        s = mine ? window.T("table.yourMoveColor","Your move {color}").replace("{color}", glyph)
                 : window.T("table.theirMoveColor","{name}'s move {color}").replace("{name}", ctx.name(g.turnId)).replace("{color}", glyph);
        if (isPlayer()) s += window.T("table.youAre"," · you are {color}").replace("{color}", g.p1Id === ctx.self() ? "🟥" : "🟦");
      }
      return `${s} · 🟥 ${g.scoreP1} – ${g.scoreP2} 🟦`;
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("dots-moves"), g.history);
      const statusEl = $("dots-status");
      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart","Waiting for the game to start…");
        return;
      }
      statusEl.textContent = statusText();
      $("dots-resign").classList.toggle("hidden", !isPlayer() || over());

      const el = $("dots-board");
      el.classList.toggle("my-turn", myTurn() && !over());
      el.innerHTML = "";
      const canPlay = myTurn() && !over();
      for (let gr = 0; gr < N; gr++) {
        for (let gc = 0; gc < N; gc++) {
          el.appendChild(buildCell(gr, gc, canPlay));
        }
      }
    }

    // buildCell returns the DOM node for one grid slot (dot / edge / box).
    function buildCell(gr, gc, canPlay) {
      const evenR = gr % 2 === 0, evenC = gc % 2 === 0;
      if (evenR && evenC) return dotNode();
      if (evenR) return edgeNode("h", (gr / 2) * BOXES + (gc - 1) / 2, canPlay);
      if (evenC) return edgeNode("v", HEDGES + ((gr - 1) / 2) * DOTS + gc / 2, canPlay);
      return boxNode(((gr - 1) / 2) * BOXES + (gc - 1) / 2);
    }

    function dotNode() {
      const d = document.createElement("span");
      d.className = "dots-dot";
      return d;
    }

    function edgeNode(orient, id, canPlay) {
      const drawn = g.edges[id] !== 0;
      const b = document.createElement("button");
      b.type = "button";
      b.className = `dots-edge ${orient}` + (drawn ? " drawn" : "");
      if (id === g.last) b.classList.add("last");
      if (canPlay && !drawn) {
        b.classList.add("live");
        b.addEventListener("click", () => send({ type: "dots.draw", edge: id }));
      }
      return b;
    }

    function boxNode(id) {
      const d = document.createElement("div");
      d.className = "dots-box";
      const owner = g.boxes[id];
      if (owner === 1) { d.classList.add("p1"); d.textContent = "🟥"; }
      else if (owner === 2) { d.classList.add("p2"); d.textContent = "🟦"; }
      return d;
    }

    $("dots-resign").addEventListener("click", () => {
      if (confirm("Resign Dots & Boxes?")) send({ type: "dots.resign" });
    });

    function outcomeWon() {
      if (!isPlayer() || g.outcome.startsWith("draw")) return null;
      const iAmRed = g.p1Id === ctx.self();
      return g.outcome.startsWith(iAmRed ? "red wins" : "blue wins");
    }

    return {
      onEvent(type, e) {
        if (type !== "dots.state") return;
        const prev = g;
        g = e;
        if (prev && prev.edges && g.edges && g.last >= 0 &&
            prev.edges[g.last] === 0 && g.edges[g.last] !== 0 && window.fx) {
          window.fx.sound.drop();
        }
        render();
        if (prev && prev.playing && !over_(prev) && over() && window.fx) {
          window.fx.celebrate($("game-dots"), outcomeWon(), window.fx.result(outcomeWon(),
            { draw: g.outcome.startsWith("draw"), spectator: g.outcome, hotseat: isHotseat() }));
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
