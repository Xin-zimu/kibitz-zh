// bsboard.js — the battleship module: fleet placement (click a ship, hover to
// preview, click to drop; Rotate toggles orientation) and the two grids
// (their waters / your fleet) with A–J / 1–10 coordinate gutters. All crypto
// and rules live in the core; this file draws and forwards intents.
(() => {
  "use strict";

  const SHIPS = [
    { id: 1, name: "Carrier", len: 5 },
    { id: 2, name: "Battleship", len: 4 },
    { id: 3, name: "Cruiser", len: 3 },
    { id: 4, name: "Submarine", len: 3 },
    { id: 5, name: "Destroyer", len: 2 },
  ];

  window.GameModules = window.GameModules || {};
  window.GameModules.battleship = { label: "🚢 Battleship", paneId: "game-bs", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;
    let visible = false;
    // placement state
    let fleet = new Array(100).fill(0);
    let placing = null;   // ship id being placed
    let orient = "h";     // 'h' | 'v'
    let hover = -1;        // cell under the cursor (preview)
    // shot fx (computed on state change, consumed by render)
    let justTheir = new Set();
    let justOwn = new Set();

    const mySide = () => (g.p1Id === ctx.self() ? 0 : (g.p2Id === ctx.self() ? 1 : -1));
    const isPlayer = () => g && mySide() >= 0;
    const myTurn = () => g && g.turnId === ctx.self();

    // ---- placement ----------------------------------------------------------

    function placedIds() {
      const ids = new Set();
      for (const v of fleet) if (v) ids.add(v);
      return ids;
    }

    // footprint returns the cells a ship would occupy from `origin` in the
    // current orientation, or null if it runs off-board or overlaps another
    // (already-placed) ship.
    function footprint(shipId, origin, o) {
      const len = SHIPS.find((s) => s.id === shipId).len;
      const x = origin % 10, y = (origin / 10) | 0;
      const cells = [];
      for (let i = 0; i < len; i++) {
        if (o === "h") {
          if (x + i > 9) return null;
          cells.push(y * 10 + x + i);
        } else {
          if (y + i > 9) return null;
          cells.push((y + i) * 10 + x);
        }
      }
      if (cells.some((c) => fleet[c] !== 0 && fleet[c] !== shipId)) return null;
      return cells;
    }

    function randomizeFleet() {
      fleet = new Array(100).fill(0);
      for (const ship of SHIPS) {
        for (let attempt = 0; attempt < 500; attempt++) {
          const o = Math.random() < 0.5 ? "h" : "v";
          const origin = Math.floor(Math.random() * 100);
          const cells = footprint(ship.id, origin, o);
          if (cells && cells.every((c) => fleet[c] === 0)) {
            for (const c of cells) fleet[c] = ship.id;
            break;
          }
        }
      }
      placing = null;
      hover = -1;
    }

    function onPlaceCell(cell) {
      if (!placing) { toast(window.T("bs.pickShip","Pick a ship to place first.")); return; }
      const cells = footprint(placing, cell, orient);
      if (!cells) { toast(window.T("bs.doesntFit","Doesn't fit there — try Rotate or another cell.")); return; }
      fleet = fleet.map((v) => (v === placing ? 0 : v)); // clear old placement
      for (const c of cells) fleet[c] = placing;
      // Advance to the next unplaced ship for a smooth flow.
      const placed = placedIds();
      const next = SHIPS.find((s) => !placed.has(s.id));
      placing = next ? next.id : null;
      hover = -1;
      render();
    }

    // ---- coordinate gutters -------------------------------------------------

    function addCoords(gridEl) {
      if (!gridEl || gridEl.parentElement.classList.contains("bs-gridwrap")) return;
      const wrap = document.createElement("div");
      wrap.className = "bs-gridwrap";
      const corner = document.createElement("div");
      const top = document.createElement("div");
      top.className = "bs-coltop";
      "ABCDEFGHIJ".split("").forEach((l) => {
        const s = document.createElement("span"); s.textContent = l; top.appendChild(s);
      });
      const side = document.createElement("div");
      side.className = "bs-rowside";
      for (let n = 1; n <= 10; n++) {
        const s = document.createElement("span"); s.textContent = String(n); side.appendChild(s);
      }
      gridEl.replaceWith(wrap);
      wrap.append(corner, top, side, gridEl);
    }

    // ---- rendering ----------------------------------------------------------

    function grid(el, cellFn, clickFn, hoverFn) {
      el.innerHTML = "";
      for (let cell = 0; cell < 100; cell++) {
        const div = document.createElement("button");
        div.type = "button";
        div.className = "bs-cell " + cellFn(cell);
        if (clickFn) div.addEventListener("click", () => clickFn(cell));
        if (hoverFn) div.addEventListener("mouseenter", () => hoverFn(cell));
        el.appendChild(div);
      }
    }

    function render() {
      if (!visible || !g) return;
      ctx.renderMoves($("bs-moves"), g.history);
      const statusEl = $("bs-status");
      const placingPane = $("bs-placing");
      const playPane = $("bs-playing");

      if (!g.playing) {
        statusEl.textContent = window.T("table.waitingStart","Waiting for the game to start…");
        placingPane.classList.add("hidden");
        playPane.classList.add("hidden");
        return;
      }

      const side = mySide();
      if (g.phase === "placing" && isPlayer() && !g.committed[side]) {
        statusEl.textContent = window.T("bs.placeFleet","Place your fleet — nobody can see it, and the commitment makes lying impossible.");
        placingPane.classList.remove("hidden");
        playPane.classList.add("hidden");
        renderPlacement();
        return;
      }
      placingPane.classList.add("hidden");
      playPane.classList.remove("hidden");

      statusEl.textContent = playStatusText(side);
      $("bs-resign").classList.toggle("hidden", !isPlayer() || g.phase === "over" || g.phase === "validating");
      renderBoards(side);
    }

    // playStatusText is the perspective-aware status line for the play view
    // (placing-done / shooting / over), matching the phase-based branches.
    function playStatusText(side) {
      if (g.phase === "placing") {
        return isPlayer() ? window.T("bs.fleetLocked","Fleet locked in — waiting for your opponent…")
          : window.T("bs.playersPlacing","Players are placing their fleets…");
      }
      if (g.phase === "shooting") {
        return myTurn() ? window.T("bs.yourShot","Your shot — click their waters")
          : (isPlayer() ? window.T("bs.incoming","Incoming…") : window.T("table.kibitzing","Kibitzing"));
      }
      let s = outcomeLabel();
      if (g.cheatBy) s += window.T("bs.participant"," (participant {n})").replace("{n}", g.cheatBy);
      return s;
    }

    // renderBoards draws the two play grids (their waters / your fleet), their
    // labels, and the sunk-ships summary for the given seat.
    function renderBoards(side) {
      const oppSide = side >= 0 ? 1 - side : 1;
      const ownSide = side >= 0 ? side : 0;
      const oppId = side >= 0 ? (side === 0 ? g.p2Id : g.p1Id) : g.p2Id;
      $("bs-their-label").textContent = side >= 0
        ? window.T("bs.theirWaters","{name}'s waters").replace("{name}", ctx.name(oppId))
        : window.T("bs.player2Board","Player 2's board");
      $("bs-own-label").textContent = side >= 0
        ? window.T("bs.yourFleet","Your fleet")
        : window.T("bs.player1Board","Player 1's board");
      const sunkTheirs = (g.sunk && g.sunk[oppSide]) || [];
      const sunkMine = (g.sunk && g.sunk[ownSide]) || [];
      const canShoot = g.phase === "shooting" && myTurn();

      const theirEl = $("bs-their");
      theirEl.classList.toggle("my-turn", canShoot);
      grid(theirEl, (cell) => theirCellClass(cell, oppSide, canShoot, sunkTheirs), (cell) => {
        if (canShoot && g.reveals[oppSide][cell] === -1) send({ type: "bs.shot", cell });
      });

      grid($("bs-own"), (cell) => ownCellClass(cell, ownSide, side, sunkMine), null);

      renderSunk(sunkTheirs, sunkMine);
      justTheir = new Set();
      justOwn = new Set();
    }

    function theirCellClass(cell, oppSide, canShoot, sunkTheirs) {
      const v = g.reveals[oppSide][cell];
      let cls;
      if (v === -1) cls = canShoot ? "unknown shootable" : "unknown";
      else if (v === 0) cls = "miss";
      else cls = "hit" + (sunkTheirs.includes(v) ? " sunk" : "");
      if (justTheir.has(cell)) cls += " just";
      return cls;
    }

    function ownCellClass(cell, ownSide, side, sunkMine) {
      const shot = g.reveals[ownSide][cell];
      const ship = side >= 0 ? g.myFleet[cell] : Math.max(0, shot);
      let cls = ship > 0 ? "ship" : "unknown";
      if (shot === 0) cls = "miss";
      if (shot > 0) cls = "hit ship" + (sunkMine.includes(shot) ? " sunk" : "");
      if (justOwn.has(cell)) cls += " just";
      return cls;
    }

    // shipLabel translates a ship for DISPLAY only; SHIPS[].name stays the
    // stable identifier the rest of the module and the wire use.
    function shipLabel(ship) { return window.T("bs.ship." + ship.id, ship.name); }

    function renderSunk(sunkTheirs, sunkMine) {
      const mineSunk = sunkMine.map((id) => shipLabel(SHIPS.find((s) => s.id === id)));
      const theirsSunk = sunkTheirs.map((id) => shipLabel(SHIPS.find((s) => s.id === id)));
      $("bs-sunk").textContent =
        (theirsSunk.length ? window.T("bs.youSank","You sank: {ships}. ").replace("{ships}", theirsSunk.join(", ")) : "") +
        (mineSunk.length ? window.T("bs.lost","Lost: {ships}.").replace("{ships}", mineSunk.join(", ")) : "");
    }

    function renderPlacement() {
      const shipsEl = $("bs-ships");
      shipsEl.innerHTML = "";
      const placed = placedIds();
      for (const ship of SHIPS) {
        const b = document.createElement("button");
        b.className = "start-btn" + (placing === ship.id ? " active" : "");
        b.textContent = `${shipLabel(ship)} (${ship.len})` + (placed.has(ship.id) ? " ✓" : "");
        b.addEventListener("click", () => { placing = ship.id; hover = -1; render(); });
        shipsEl.appendChild(b);
      }
      // Build the grid ONCE. The hover preview updates classes in place
      // (never rebuilds) — a rebuild on mouseenter would destroy the cell a
      // touch tap is landing on, so taps could never place a ship.
      grid($("bs-place-grid"), (cell) => (fleet[cell] ? "ship" : "unknown placeable"),
        onPlaceCell, (cell) => { hover = cell; applyPreview(); });
      applyPreview();
      $("bs-confirm").disabled = placed.size !== 5;
    }

    // applyPreview toggles the footprint highlight on the existing placement
    // cells without rebuilding the grid.
    function applyPreview() {
      const cells = $("bs-place-grid").children;
      for (const c of cells) c.classList.remove("preview", "preview-bad");
      if (!placing || hover < 0) return;
      const fp = footprint(placing, hover, orient);
      if (fp) fp.forEach((c) => cells[c] && cells[c].classList.add("preview"));
      else if (cells[hover]) cells[hover].classList.add("preview-bad");
    }

    // ---- controls -----------------------------------------------------------

    addCoords($("bs-their"));
    addCoords($("bs-own"));
    addCoords($("bs-place-grid"));

    $("bs-rotate").addEventListener("click", () => { orient = orient === "h" ? "v" : "h"; render(); });
    document.addEventListener("keydown", (e) => {
      if (visible && g && g.phase === "placing" && (e.key === "r" || e.key === "R")) {
        orient = orient === "h" ? "v" : "h";
        render();
      }
    });
    $("bs-randomize").addEventListener("click", () => { randomizeFleet(); render(); });
    $("bs-confirm").addEventListener("click", () => send({ type: "bs.commit", fleet }));
    $("bs-resign").addEventListener("click", () => {
      if (confirm(window.T("bs.resignConfirm","Resign battleship?"))) send({ type: "bs.resign" });
    });

    function bsWon() {
      if (!isPlayer() || g.cheatBy) return null;
      if (g.outcome === "player 1 wins") return mySide() === 0;
      if (g.outcome === "player 2 wins") return mySide() === 1;
      return null;
    }

    // bsFactual is the neutral "<winner> wins" line for the win banner's
    // spectator branch (fx.result handles the player's "You win!"/"You lost").
    function bsFactual() {
      const base = (g.outcome || "").split(" (")[0];
      const winner = base === "player 1 wins" ? 0 : base === "player 2 wins" ? 1 : -1;
      if (winner < 0) return window.TO(g.outcome) || window.T("bs.gameOver","Game over"); // cheat / unknown
      return window.T("bs.wins","{name} wins").replace("{name}", ctx.name(winner === 0 ? g.p1Id : g.p2Id));
    }

    // outcomeLabel turns the seat-based outcome ("player 2 wins") into a
    // perspective-aware label — "You win!" for the local player, otherwise the
    // winner's name ("Computer wins" / "Ada wins") — preserving any trailing
    // note like " (verifying boards…)".
    function outcomeLabel() {
      const o = g.outcome || "";
      const i = o.indexOf(" (");
      const note = i >= 0 ? o.slice(i) : "";
      const base = i >= 0 ? o.slice(0, i) : o;
      const winner = base === "player 1 wins" ? 0 : base === "player 2 wins" ? 1 : -1;
      if (winner < 0) return window.TO(o); // cheat / unknown text → mapped for display
      if (isPlayer() && winner === mySide()) return window.T("table.youWin","You win!") + note;
      return window.T("bs.wins","{name} wins").replace("{name}", ctx.name(winner === 0 ? g.p1Id : g.p2Id)) + note;
    }

    // computeShotFx diffs reveal grids prev→new to drive splash/boom/sunk +
    // sounds. Applies to both boards so you also see/hear incoming shots.
    function computeShotFx(prev) {
      justTheir = new Set();
      justOwn = new Set();
      if (!prev || !prev.reveals || g.phase === "placing") return;
      const side = mySide();
      const oppSide = side >= 0 ? 1 - side : 1;
      const ownSide = side >= 0 ? side : 0;
      const t = diffReveals(prev, oppSide, justTheir);
      const o = diffReveals(prev, ownSide, justOwn);
      if (!window.fx) return;
      playShotSound(prev, oppSide, ownSide, t.hit || o.hit, t.miss || o.miss);
    }

    // diffReveals records cells newly revealed since `prev` on the `which` board
    // into `into`, and reports whether any of them were a hit / a miss.
    function diffReveals(prev, which, into) {
      const a = prev.reveals[which], b = g.reveals[which];
      let hit = false, miss = false;
      if (!a || !b) return { hit, miss };
      for (let c = 0; c < 100; c++) {
        if (a[c] === -1 && b[c] !== -1) {
          into.add(c);
          if (b[c] > 0) hit = true; else miss = true;
        }
      }
      return { hit, miss };
    }

    function sunkGrew(prev, which) {
      return ((g.sunk && g.sunk[which]) || []).length >
        ((prev.sunk && prev.sunk[which]) || []).length;
    }

    function playShotSound(prev, oppSide, ownSide, hitSound, missSound) {
      const newlySunk = sunkGrew(prev, oppSide) || sunkGrew(prev, ownSide);
      if (newlySunk) window.fx.sound.sunk();
      else if (hitSound) window.fx.sound.hit();
      else if (missSound) window.fx.sound.splash();
    }

    return {
      onEvent(type, e) {
        if (type !== "battleship.state") return;
        const prev = g;
        g = e;
        computeShotFx(prev);
        render();
        if (prev && prev.playing && prev.phase !== "over" && g.phase === "over" && window.fx) {
          window.fx.celebrate($("game-bs"), bsWon(), window.fx.result(bsWon(), { spectator: bsFactual() }));
        }
      },
      setVisible(v) { visible = v; if (v) render(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (g.phase === "over") return { status: "over", detail: g.outcome };
        const needsMe = (g.phase === "placing" && isPlayer() && !g.committed[mySide()]) ||
          (g.phase === "shooting" && myTurn());
        return { status: "live", myTurn: needsMe };
      },
    };
  }
})();
