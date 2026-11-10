// board.js — the chess module: pure-DOM board rendered from FEN plus the
// chess input/render glue. Zero game logic: the WASM core owns rules; this
// file draws, reports clicks, and forwards intents.
//
// Registers itself in window.GameModules; app.js is only a router.
(() => {
  "use strict";

  const GLYPHS = {
    K: "♔", Q: "♕", R: "♖", B: "♗", N: "♘", P: "♙",
    k: "♚", q: "♛", r: "♜", b: "♝", n: "♞", p: "♟",
  };
  const FILES = "abcdefgh";

  function squareName(fileIdx, rankIdx) {
    return FILES[fileIdx] + (rankIdx + 1);
  }

  function parseFen(fen) {
    const placement = fen.split(" ")[0];
    const out = {};
    const ranks = placement.split("/");
    for (let r = 0; r < 8; r++) {
      let file = 0;
      for (const ch of ranks[r]) {
        if (ch >= "1" && ch <= "8") {
          file += Number(ch);
        } else {
          out[squareName(file, 7 - r)] = ch;
          file++;
        }
      }
    }
    return out;
  }

  function pieceCount(fen) {
    return (fen.split(" ")[0].match(/[a-zA-Z]/g) || []).length;
  }

  window.GameModules = window.GameModules || {};
  window.GameModules.chess = { label: "♞ Chess", paneId: "game-chess", create };

  function create(ctx) {
    const { $, send, toast } = ctx;
    let g = null;             // last chess.state
    let selected = null;      // selected square
    let selectedPiece = null; // FEN char on it
    let visible = false;
    let animatedUci = null;   // last move we've already slide-animated

    const isHotseat = () => ctx.hotseat && ctx.hotseat();
    const isPlayer = () => isHotseat() || (g && (g.whiteId === ctx.self() || g.blackId === ctx.self()));
    const myTurn = () => g && (isHotseat() || g.turnId === ctx.self()) && !over();
    const over = () => g && g.outcome !== "*";
    const opts = () => ({
      flipped: g && g.blackId === ctx.self(),
      lastMove: g && g.lastUci,
    });

    function render(el, fen, o, targets) {
      const pieces = parseFen(fen);
      el.classList.toggle("my-turn", myTurn());
      el.innerHTML = "";
      for (let row = 0; row < 8; row++) {
        for (let col = 0; col < 8; col++) {
          el.appendChild(squareFor(row, col, o, pieces, targets));
        }
      }
    }

    // squareFor builds one board button: colour, piece glyph, coord labels,
    // selection/target/last-move state, and the click handler.
    function squareFor(row, col, o, pieces, targets) {
      const rankIdx = o.flipped ? row : 7 - row;
      const fileIdx = o.flipped ? 7 - col : col;
      const sq = squareName(fileIdx, rankIdx);
      const cell = document.createElement("button");
      cell.type = "button";
      cell.className = "sq " + ((fileIdx + rankIdx) % 2 ? "light" : "dark");
      cell.dataset.sq = sq;
      const piece = pieces[sq];
      if (piece) placePiece(cell, piece);
      // Coordinate labels along the bottom rank and left file (Lichess-style).
      if (row === 7) cell.appendChild(coord("file", FILES[fileIdx]));
      if (col === 0) cell.appendChild(coord("rank", String(rankIdx + 1)));
      markSquareState(cell, sq, o, targets);
      cell.addEventListener("click", () => onSquare(sq, piece || null));
      return cell;
    }

    function placePiece(cell, piece) {
      const gl = document.createElement("span");
      gl.className = "slide-piece";
      gl.textContent = GLYPHS[piece];
      cell.appendChild(gl);
      cell.classList.add(piece === piece.toUpperCase() ? "white-piece" : "black-piece");
    }

    function markSquareState(cell, sq, o, targets) {
      if (sq === selected) cell.classList.add("selected");
      if (targets && targets.includes(sq)) cell.classList.add("target");
      if (o.lastMove && (sq === o.lastMove.slice(0, 2) || sq === o.lastMove.slice(2, 4))) {
        cell.classList.add("last-move");
      }
    }

    function coord(kind, text) {
      const s = document.createElement("span");
      s.className = "coord " + kind;
      s.textContent = text;
      return s;
    }

    // slideLastMove animates the moved piece from its origin square to its
    // destination, after the board has been (re)built.
    function slideLastMove(el, uci) {
      if (!window.fx || !uci || uci.length < 4) return;
      const from = uci.slice(0, 2), to = uci.slice(2, 4);
      const fromCell = el.querySelector(`.sq[data-sq="${from}"]`);
      const toPiece = el.querySelector(`.sq[data-sq="${to}"] .slide-piece`);
      if (!fromCell || !toPiece) return;
      const a = fromCell.getBoundingClientRect();
      const b = toPiece.closest(".sq").getBoundingClientRect();
      window.fx.slideFrom(toPiece, a.left - b.left, a.top - b.top);
    }

    function onSquare(sq, piece) {
      if (!g || !g.playing || over() || !isPlayer()) return;
      if (selected && selected !== sq && isTargetSquare(sq)) { commitMove(sq); return; }
      // Which colour may be picked up: your own normally; in solo, whichever
      // side is to move (you drive both).
      const pickWhite = isHotseat() ? (g.turnId === g.whiteId) : (g.whiteId === ctx.self());
      if (piece && (piece === piece.toUpperCase()) === pickWhite) {
        selected = sq;
        selectedPiece = piece;
        send({ type: "chess.targets", from: sq, id: Date.now() });
      } else {
        clearSelection();
      }
    }

    function isTargetSquare(sq) {
      return [...document.querySelectorAll("#board .sq.target")]
        .some((c) => c.dataset.sq === sq);
    }

    function promotionFor(sq) {
      return (selectedPiece === "P" && sq[1] === "8") ||
             (selectedPiece === "p" && sq[1] === "1") ? "q" : "";
    }

    function commitMove(sq) {
      if (!myTurn()) { toast(window.T("table.notYourTurn","Not your turn.")); return; }
      send({ type: "chess.move", uci: selected + sq + promotionFor(sq) });
      clearSelection();
    }

    function clearSelection() {
      selected = null;
      selectedPiece = null;
      render($("board"), g.fen, opts(), []);
    }

    function renderPane() {
      if (!visible || !g) return;
      ctx.renderMoves($("chess-moves"), g.history);
      $("chess-pgn").classList.toggle("hidden", !(g.history && g.history.length));
      const el = $("status-line");
      if (!g.playing) {
        el.textContent = window.T("table.waitingStart","Waiting for the game to start…");
        return;
      }
      el.textContent = statusText();
      $("btn-resign").classList.toggle("hidden", !isPlayer() || over());
      $("btn-draw").classList.toggle("hidden", !isPlayer() || over() || (ctx.vsBot && ctx.vsBot()));
      clearSelection();
    }

    // statusText is the status-line string for a playing game (not started is
    // handled by the caller).
    function statusText() {
      if (over()) return `${resultText()} — ${g.method}`;
      if (isHotseat()) {
        const side = g.turnId === g.whiteId ? window.T("chess.white","White") : window.T("chess.black","Black");
        return window.T("table.toMove","{color} to move").replace("{color}", side);
      }
      return (g.turnId === ctx.self() ? window.T("table.yourMove","Your move")
                                      : window.T("table.theirTurn","{name} to move").replace("{name}", ctx.name(g.turnId))) +
        (isPlayer() ? "" : window.T("chess.kibitzingSuffix"," (you're kibitzing)"));
    }

    // resultText renders g.outcome as a human phrase (shared by status + celebrate).
    function resultText() {
      // Chess uses PGN result tokens as its protocol outcome, not prose.
      return g.outcome === "1/2-1/2" ? window.T("table.draw","Draw")
           : g.outcome === "1-0" ? window.T("chess.whiteWins","White wins")
           : window.T("chess.blackWins","Black wins");
    }

    // one-time control wiring
    $("btn-resign").addEventListener("click", () => {
      if (confirm(window.T("chess.resignConfirm","Resign the game?"))) send({ type: "chess.resign" });
    });
    $("btn-draw").addEventListener("click", () => send({ type: "chess.offerDraw" }));
    $("btn-agree-draw").addEventListener("click", () => send({ type: "chess.agreeDraw" }));
    $("chess-pgn").addEventListener("click", async () => {
      if (!g || !g.pgn) return;
      try {
        await navigator.clipboard.writeText(g.pgn);
        toast(window.T("table.pgnCopied","PGN copied."));
      } catch {
        toast(window.T("table.copyFailed","Couldn't copy — select the moves to copy manually."));
      }
    });

    function outcomeWon() {
      if (!isPlayer() || g.outcome === "1/2-1/2") return null;
      return g.outcome === (g.whiteId === ctx.self() ? "1-0" : "0-1");
    }

    function onState(e) {
      const prev = g;
      g = e;
      $("btn-agree-draw").classList.add("hidden");
      playMoveSound(prev);
      renderPane();
      if (visible && g.lastUci && g.lastUci !== animatedUci) {
        slideLastMove($("board"), g.lastUci);
      }
      animatedUci = g.lastUci;
      if (prev && prev.playing && prev.outcome === "*" && over() && window.fx) {
        celebrateOutcome();
      }
    }

    // Move/capture sound when a new move landed.
    function playMoveSound(prev) {
      if (!(window.fx && g.lastUci && g.lastUci !== animatedUci && prev && prev.fen)) return;
      const captured = pieceCount(g.fen) < pieceCount(prev.fen);
      captured ? window.fx.sound.capture() : window.fx.sound.move();
    }

    function celebrateOutcome() {
      const factual = resultText();
      window.fx.celebrate($("game-chess"), outcomeWon(), window.fx.result(outcomeWon(),
        { draw: g.outcome === "1/2-1/2", spectator: factual, detail: g.method, hotseat: isHotseat() }));
    }

    function onTargets(e) {
      if (visible && e.from === selected) {
        render($("board"), g.fen, opts(), e.targets);
      }
    }

    function onDrawOffered() {
      if (isPlayer()) {
        $("btn-agree-draw").classList.remove("hidden");
        toast(window.T("table.drawOffered","Draw offered — accept?"));
      } else {
        toast(window.T("table.drawWasOffered","A draw was offered."));
      }
    }

    return {
      onEvent(type, e) {
        switch (type) {
          case "chess.state": onState(e); break;
          case "chess.targets": onTargets(e); break;
          case "chess.drawOffered": onDrawOffered(); break;
        }
      },
      setVisible(v) { visible = v; if (v) renderPane(); },
      card() {
        if (!g || !g.playing) return { status: "idle" };
        if (over()) {
          const result = g.outcome === "1/2-1/2" ? "Draw" :
            (g.outcome === "1-0" ? "1-0" : "0-1");
          return { status: "over", detail: result };
        }
        return { status: "live", myTurn: myTurn() };
      },
    };
  }
})();
