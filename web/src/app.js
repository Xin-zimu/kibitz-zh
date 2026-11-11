// app.js — session flow, chat, roster, and the game picker/router. All
// protocol, crypto, and rules live in the Go WASM core; per-game rendering
// and input live in each game's module file (board.js, bgboard.js, …),
// registered via window.GameModules. The bridge:
//   window.kibitz_send(json)   — UI → core (installed by the core)
//   window.kibitzOnEvent(json) — core → UI (defined here)
(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const views = { home: $("view-home"), lobby: $("view-lobby"), table: $("view-table") };

  const state = {
    self: 0,
    role: "", // host | player | spectator
    members: {},
    names: {}, // id -> screen name (from the ctl roster)
    activeGame: null, // module id when a pane is open; null = picker
    solo: false, // local session (no relay): pass-and-play or vs the computer
    vsBot: false, // solo vs the computer (user is player 1; a bot drives player 2)
    inSession: false, // pushed a history entry for the session (lobby/table)
    endpoints: {}, // id -> Web Push endpoint (from the ctl roster)
  };

  // displayName returns a participant's screen name, "you" for self, or a
  // "#id" fallback before a name is known.
  function displayName(id) {
    if (id === state.self) return "you";
    return state.names[id] || ("#" + id);
  }

  // seatedPlayers returns the two players (host + player roles), ascending;
  // spectators excluded. The pair is the same across all games at the table.
  function seatedPlayers() {
    const ids = [];
    for (const [idStr, role] of Object.entries(state.members)) {
      if (role === "host" || role === "player") ids.push(Number(idStr));
    }
    return ids.sort((a, b) => a - b);
  }

  // matchupText is "you vs Ada" (when you're a player) or "Ada vs Bo".
  function matchupText() {
    const ps = seatedPlayers();
    if (ps.length < 2) return "";
    if (ps.includes(state.self)) {
      return "you vs " + displayName(ps.find((id) => id !== state.self));
    }
    return displayName(ps[0]) + " vs " + displayName(ps[1]);
  }

  function renderLobbyName() {
    const n = state.names[state.self] || document.getElementById("display-name").value.trim();
    $("lobby-you").textContent = n
      ? window.T("home.hostingAs","You're hosting as {name}.").replace("{name}", n)
      : "You're hosting anonymously — set a name on the home screen next time.";
  }

  function show(name) {
    for (const [k, v] of Object.entries(views)) v.classList.toggle("hidden", k !== name);
  }

  // ---- navigation: home → table(picker) → game, mirrored by the OS/browser
  // Back button via the History API. Each level's "up" is one popstate.
  function pushSession() {
    if (state.inSession) return;
    state.inSession = true;
    history.pushState({ k: "session" }, "");
  }
  // Leaving a session (solo or networked) reloads to a clean home: it tears down
  // the core + session/loopback and drops any invite #phrase. Reload is the
  // simplest bulletproof reset (board modules hold state with no reset hook).
  function leaveToHome() {
    location.replace(location.pathname);
  }
  function leaveSession() {
    if (!state.solo) {
      const msg = state.role === "host" ? "Leave and close the table?" : "Leave the table?";
      if (!confirm(msg)) return;
    }
    leaveToHome();
  }
  window.addEventListener("popstate", () => {
    if (state.activeGame) { closeGame(); return; }        // game pane → picker
    if (views.home.classList.contains("hidden")) leaveToHome(); // in a session → home
    // already home: nothing to do
  });

  function send(obj) {
    if (window.kibitz_send) window.kibitz_send(JSON.stringify(obj));
  }

  let toastTimer = null;
  function toast(msg) {
    const el = $("toast");
    el.textContent = msg;
    el.classList.remove("hidden");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => el.classList.add("hidden"), 4000);
  }

  // ---- game modules ---------------------------------------------------------

  const ctx = {
    $, send, toast,
    self: () => state.self,
    role: () => state.role,
    name: displayName, // game modules label opponents by screen name
    solo: () => state.solo, // in a local session (no relay)
    hotseat: () => state.solo && !state.vsBot, // pass-and-play: drive both sides
    vsBot: () => state.vsBot, // playing the computer
    renderMoves, // shared move-list renderer (see below)
  };

  // renderMoves fills an <ol> move-list from a game's authoritative history
  // (state.History), numbering via the ordered list and keeping the newest move
  // in view. Shared by every board module so the panel looks identical.
  function renderMoves(el, moves) {
    if (!el) return;
    moves = moves || [];
    if (el.childElementCount === moves.length) return; // unchanged (avoid reflow)
    el.innerHTML = "";
    for (const m of moves) {
      const li = document.createElement("li");
      li.textContent = m;
      el.appendChild(li);
    }
    el.scrollTop = el.scrollHeight;
  }
  const games = {}; // id -> instantiated module
  for (const [id, def] of Object.entries(window.GameModules || {})) {
    games[id] = { ...def, ...def.create(ctx) };
  }

  function openGame(id) {
    if (state.activeGame !== id) history.pushState({ k: "game", id }, "");
    state.activeGame = id;
    $("game-picker").classList.add("hidden");
    $("game-pane").classList.remove("hidden");
    $("btn-leave").classList.add("hidden"); // in a game, "All games" is the only back control
    for (const [gid, mod] of Object.entries(games)) {
      $(mod.paneId).classList.toggle("hidden", gid !== id);
      mod.setVisible(gid === id);
    }
    updateTakebackBar(lastState[id]);
    $("game-pane").focus(); // move focus into the opened game (tabindex=-1)
  }

  function closeGame() {
    state.activeGame = null;
    $("game-pane").classList.add("hidden");
    $("game-picker").classList.remove("hidden");
    $("btn-leave").classList.remove("hidden");
    for (const mod of Object.values(games)) mod.setVisible(false);
    updateTakebackBar(null);
    renderPicker();
    $("game-picker").focus(); // return focus to the picker (tabindex=-1)
  }

  // ---- shared takeback bar --------------------------------------------------
  // Every deterministic game's state carries canTakeback (this end may offer)
  // and takebackBy (participant who has offered, 0 = none). One shared control
  // drives them all, keyed off the active game's latest state.
  const lastState = {}; // game id -> last "<id>.state" event
  function updateTakebackBar(e) {
    const tb = $("btn-takeback"), ab = $("btn-accept-takeback");
    // A takeback is a two-party negotiation: the offer only takes effect when
    // the OTHER end accepts. In a solo session the opponent is a bot (which
    // never answers an offer) or a silently-drained loopback end, so offering
    // one is a dead end — and because the control lives above the board, the
    // button appearing after every human move and vanishing when the reply
    // lands reflowed the whole pane. Never offer it without a real opponent.
    const canOffer = !!(e && e.canTakeback) && !state.solo;
    const canAccept = !!(e && e.takebackBy && e.takebackBy !== state.self) && !state.solo;
    tb.classList.toggle("hidden", !canOffer);
    ab.classList.toggle("hidden", !canAccept);
  }
  $("btn-takeback").addEventListener("click", () => {
    if (state.activeGame) send({ type: state.activeGame + ".offerTakeback" });
  });
  $("btn-accept-takeback").addEventListener("click", () => {
    if (state.activeGame) send({ type: state.activeGame + ".acceptTakeback" });
  });

  // Back-button controls funnel through history so in-app and OS Back agree.
  $("btn-back").addEventListener("click", () => history.back()); // game → picker
  $("btn-leave").addEventListener("click", leaveSession);        // table → home
  $("btn-cancel").addEventListener("click", leaveSession);       // lobby → home

  function renderPicker() {
    const el = $("game-picker");
    el.innerHTML = "";
    const canStart = state.role === "host" || state.role === "player";
    // A spectator can't start games — if none is live yet, say so rather than
    // showing a grid of unstartable cards.
    const anyOpenable = Object.values(games).some((m) => {
      const st = m.card().status;
      return st === "live" || st === "lobby";
    });
    if (state.role === "spectator" && !anyOpenable) {
      const hint = document.createElement("p");
      hint.className = "note picker-hint";
      hint.textContent = window.T("table.watchingWaiting","👁 You're watching — waiting for the players to start a game.");
      el.appendChild(hint);
    }
    for (const [id, mod] of Object.entries(games)) {
      el.appendChild(buildGameCard(id, mod, canStart));
    }
  }

  // Split "🔴 Connect Four" into a big icon + the name so the glyph is legible.
  // gameID maps a module object back to its registry key so the title's
  // translation key is stable ("game.chess", "game.gomokup", …).
  function gameID(mod) {
    for (const id of Object.keys(games)) if (games[id] === mod) return id;
    return "";
  }

  function buildGameTitle(mod) {
    const title = document.createElement("div");
    title.className = "game-title";
    const sp = mod.label.indexOf(" ");
    const iconEl = document.createElement("span");
    iconEl.className = "game-icon";
    iconEl.textContent = sp > 0 ? mod.label.slice(0, sp) : mod.label;
    const nameEl = document.createElement("span");
    const rawName = sp > 0 ? mod.label.slice(sp + 1) : "";
    // The label's icon stays as-is; only the game NAME is translated.
    nameEl.textContent = rawName ? window.T("game." + gameID(mod), rawName) : "";
    title.append(iconEl, nameEl);
    return title;
  }

  // Build the status badge and attach any per-status affordances (click-to-open,
  // Rematch/Start buttons, matchup note). Returns the badge element to append.
  function makeCardOpenGame(card, id) {
    card.classList.add("clickable");
    card.setAttribute("role", "button");
    card.setAttribute("tabindex", "0");
    card.addEventListener("click", () => openGame(id));
    card.addEventListener("keydown", (e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        openGame(id);
      }
    });
  }

  // Not-yet-live statuses: battleship's two-player note, or the default
  // "not started" card with its Start button.
  function applyIdleBadge(id, badge, card, canStart) {
    // Gomoku Party vs the computer: pick 2–4 players (you + 1–3 bots).
    if (state.solo && state.vsBot && id === "gomokup") {
      badge.textContent = window.T("table.vsComputer","vs computer");
      card.appendChild(gomokupBotChooser());
      return;
    }
    // Battleship's secret placement doesn't fit pass-and-play (but is fine vs the
    // computer); Gin's dealerless shuffle needs two live players and has no bot,
    // so it's two-player-only in every solo mode.
    const networkedOnly = (state.solo && !state.vsBot && id === "battleship") ||
      (state.solo && (id === "gin" || id === "gomokup"));
    if (networkedOnly) {
      badge.textContent = id === "gomokup" ? window.T("table.twoToFour","2–4 players") : window.T("table.twoPlayers","two players");
      const note = document.createElement("div");
      note.className = "game-matchup";
      note.textContent = id === "gomokup" ? window.T("table.inviteFriends","Invite friends to play (2–4)") : window.T("table.inviteFriend","Invite a friend to play");
      card.appendChild(note);
    } else {
      badge.textContent = window.T("table.notStarted","not started");
      if (canStart) card.appendChild(actionButton(window.T("table.start","+ Start"), id));
    }
  }

  function applyBadgeAndActions(id, info, card, canStart) {
    const badge = document.createElement("div");
    badge.className = "game-badge " + info.status;
    if (info.status === "live") {
      badge.textContent = info.myTurn ? "● your turn" : "○ in play";
      makeCardOpenGame(card, id);
    } else if (info.status === "lobby") {
      // A table is open for seating — anyone (incl. spectators) may open the
      // pane to take a seat, so the card is clickable regardless of canStart.
      badge.textContent = info.detail || "● seating";
      makeCardOpenGame(card, id);
    } else if (info.status === "over") {
      badge.textContent = info.detail || "finished";
      makeCardOpenGame(card, id);
      if (canStart) card.appendChild(actionButton(window.T("table.rematch","Rematch"), id));
    } else {
      applyIdleBadge(id, badge, card, canStart);
    }
    return badge;
  }

  function buildGameCard(id, mod, canStart) {
    const card = document.createElement("div");
    card.className = "game-card";
    card.appendChild(buildGameTitle(mod));

    const info = mod.card();
    const badge = applyBadgeAndActions(id, info, card, canStart);
    card.appendChild(badge);
    // Who's playing (live/finished games) — same pair across the table.
    if (info.status === "live" || info.status === "over") {
      const vs = matchupText();
      if (vs) {
        const m = document.createElement("div");
        m.className = "game-matchup";
        m.textContent = vs;
        card.appendChild(m);
      }
    }
    return card;
  }

  // gomokupBotChooser: a "Players: 2 / 3 / 4" row that starts a solo Gomoku
  // Party against (n-1) bots at the current difficulty.
  function gomokupBotChooser() {
    const wrap = document.createElement("div");
    wrap.className = "game-matchup gp-bot-choose";
    const label = document.createElement("span");
    label.textContent = window.T("table.players","Players:");
    wrap.appendChild(label);
    for (const n of [2, 3, 4]) {
      const b = document.createElement("button");
      b.type = "button";
      b.className = "start-btn";
      b.textContent = String(n);
      b.addEventListener("click", (ev) => {
        ev.stopPropagation();
        send({ type: "solo", mode: "bot", game: "gomokup", seats: n, level: difficulty, name: myName() });
      });
      wrap.appendChild(b);
    }
    return wrap;
  }

  function actionButton(label, gameID) {
    const b = document.createElement("button");
    b.className = "start-btn";
    b.textContent = label;
    b.addEventListener("click", (ev) => {
      ev.stopPropagation();
      send({ type: "game.start", game: gameID });
    });
    return b;
  }

  // Detect a host handoff (migration) to nudge the other survivors. Only the
  // NEW host toasts itself (via session.promoted); here we toast the rest once
  // the roster shows a different host than before.
  function announceHostChange(e) {
    const newHost = Number(Object.entries(e.members || {}).find(([, r]) => r === "host")?.[0] || 0);
    if (prevHost && newHost && newHost !== prevHost && newHost !== state.self) {
      toast(window.T("table.hostingNow","{name} is hosting now.").replace("{name}", (e.names && e.names[newHost]) || ("#" + newHost)));
    }
    if (newHost) prevHost = newHost;
  }

  // ---- core → UI ------------------------------------------------------------

  const handlers = {
    "core.ready"() {
      $("btn-create").disabled = false;
      $("btn-join").disabled = false;
      $("join-phrase").disabled = false;
      $("btn-solo").disabled = false;
      $("btn-hotseat").disabled = false;
      $("home-status").textContent = "";
      // Arriving via a share link: switch the home screen into "invited"
      // mode — a prominent invite banner, name field, and a big Join — rather
      // than auto-joining (which robbed link-openers of a name) or leaving the
      // join buried under "Start a table".
      let hash = location.hash.slice(1);
      const watch = hash.startsWith("watch=");
      if (watch) hash = hash.slice("watch=".length);
      const phrase = decodeURIComponent(hash);
      if (phrase) {
        $("join-phrase").value = phrase;
        $("invite-phrase").textContent = phrase;
        $("watch-toggle").checked = watch;
        const kicker = $("invite-banner").querySelector(".invite-kicker");
        if (kicker) kicker.textContent = watch ? "You've been invited to watch a game" : "You've been invited to a game";
        $("invite-banner").classList.remove("hidden");
        $("view-home").classList.add("invited");
        $("btn-join").textContent = watch ? "👁 Watch game" : "Join game";
        $("btn-create").textContent = window.T("home.createOwn","or start your own table");
        $("display-name").focus();
      }
    },
    "session.created"(e) {
      state.self = e.self;
      state.role = "host";
      $("lobby-phrase").textContent = e.phrase;
      $("lobby-url").value = e.url;
      // A watch link is the same share URL with the "watch" intent, so openers
      // land as spectators without taking the player seat.
      watchLink = e.url.includes("#") ? e.url.replace("#", "#watch=") : e.url + "#watch=" + encodeURIComponent(e.phrase);
      if (e.qr) $("lobby-qr").src = "data:image/png;base64," + e.qr;
      renderLobbyName();
      pushSession();
      show("lobby");
      push.generateIfHost(); // mint the session VAPID key so peers can subscribe
    },
    "session.joined"(e) {
      state.self = e.self;
      state.role = e.role;
      state.solo = !!e.solo;
      state.vsBot = !!e.bot;
      views.table.classList.toggle("solo", state.solo);
      if (state.solo) {
        $("solo-banner").textContent = state.vsBot
          ? "Solo · playing the computer"
          : "Solo · you play both sides";
      }
      pushSession();
      show("table");
      renderPicker();
      renderWatching();
      syncNotifyButton();
    },
    // A transient drop: the core is silently re-establishing the same session
    // (same seat, same game). Show a non-destructive banner and keep the board.
    "session.reconnecting"() {
      $("reconnect-banner").classList.remove("hidden");
    },
    // Resumed transparently — the game continued underneath.
    "session.resumed"() {
      $("reconnect-banner").classList.add("hidden");
      toast(window.T("table.reconnected","Reconnected."));
    },
    "session.closed"(e) {
      $("reconnect-banner").classList.add("hidden");
      toast(e.reason === "host left"
      ? window.T("table.hostClosed","The host closed the table.")
      : window.T("table.sessionEnded","Session ended: {reason}").replace("{reason}", e.reason || window.T("table.connectionLost","connection lost")));
      setTimeout(() => location.replace(location.pathname), 1500);
    },
    roster(e) {
      announceHostChange(e);
      state.members = e.members;
      state.names = {};
      for (const [id, n] of Object.entries(e.names || {})) state.names[Number(id)] = n;
      state.endpoints = {};
      for (const [id, ep] of Object.entries(e.endpoints || {})) state.endpoints[Number(id)] = ep;
      push.ingestKey(e.pushKey || "");
      push.generateIfHost(); // host mints the session VAPID key if not yet set
      syncNotifyButton();
      renderWatching();
      renderMembers();
      renderLobbyName(); // no-op visually unless the lobby is showing
      // Names may have just arrived — refresh the open game's labels.
      if (state.activeGame && games[state.activeGame]) {
        games[state.activeGame].setVisible(true);
      }
      // The host's lobby → table transition: someone arrived.
      if (state.role === "host" && Object.keys(e.members).length > 1) {
        show("table");
        renderPicker();
      }
    },
    // Host migration: this end was promoted to host (the previous host left).
    "session.promoted"() {
      state.role = "host";
      toast(window.T("table.youHostingNow","You're hosting now."));
      renderPicker();       // host affordances (Start/Rematch)
      renderWatching();
      syncNotifyButton();
      push.generateIfHost(); // mint the session VAPID key if not already set
    },
    "chat.msg"(e) {
      appendChat(e.from, e.text);
    },
    error(e) {
      toast(e.message);
    },
  };

  window.kibitzOnEvent = (json) => {
    let e;
    try { e = JSON.parse(json); } catch { return; }
    const h = handlers[e.type];
    if (h) { h(e); return; }
    // "<gameId>.xyz" events route to that game's module.
    const dot = e.type.indexOf(".");
    if (dot > 0) {
      const gid = e.type.slice(0, dot);
      const mod = games[gid];
      if (mod) {
        mod.onEvent(e.type, e);
        renderPicker();
        updateTurnCue();
        if (e.type.endsWith(".state")) {
          lastState[gid] = e;
          if (gid === state.activeGame) updateTakebackBar(e);
          maybeNotifyTurn(gid, e.turnId);
        }
      }
    }
  };

  // ---- mute toggle + background turn cue -------------------------------------

  function syncMuteIcon() {
    const b = $("btn-mute");
    const muted = window.fx && window.fx.sound.isMuted();
    b.textContent = muted ? "🔇" : "🔊";
    b.title = muted ? "Unmute sound" : "Mute sound";
    b.setAttribute("aria-label", muted ? "Unmute sound" : "Mute sound");
    b.setAttribute("aria-pressed", String(!!muted));
  }
  $("btn-mute").addEventListener("click", () => {
    if (window.fx) window.fx.sound.toggleMute();
    syncMuteIcon();
  });
  syncMuteIcon();

  // When a game needs the local player's action and the tab is hidden, flag
  // it in the title bar (restored on return). A gentle "it's your move" nudge.
  let baseTitle = document.title;
  function updateTurnCue() {
    const myMove = Object.values(games).some((m) => {
      const c = m.card();
      return c.status === "live" && c.myTurn;
    });
    if (document.hidden && myMove) document.title = "● your turn · kibitz";
    else document.title = baseTitle;
  }
  document.addEventListener("visibilitychange", updateTurnCue);

  // A locale change must re-render anything built in JS: the picker cards carry
  // translated titles/badges, and the open board's status line is translated
  // too. i18n has already handled the static markup by the time this fires.
  window.addEventListener("kibitz:locale", () => {
    try {
      renderPicker();
      if (state.activeGame && games[state.activeGame]) {
        games[state.activeGame].setVisible(true);
      }
      renderMembers();
      renderWatching();
      renderLobbyName();
    } catch (_) {
      /* a view that is not mounted is not an error */
    }
  });

  // ---- turn notifications (Web Push) -----------------------------------------
  // Opt-in "your turn" pushes for networked games. The host mints a per-session
  // VAPID keypair and each client shares its push endpoint — all over the
  // encrypted ctl channel, so the relay never sees them. When a move hands the
  // turn to the opponent, the mover signs an EMPTY push in-browser (browsers
  // can't post to push services directly) and hands it to the relay's keyless
  // /push forwarder. No game content ever leaves the device — the service
  // worker shows a generic "your turn".

  const pushSupported =
    "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
  const lastTurn = {}; // gameId -> last observed turnId; fire only on transition

  const b64u = {
    fromBytes(bytes) {
      let s = "";
      for (const b of bytes) s += String.fromCharCode(b);
      return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    },
    fromStr(str) { return b64u.fromBytes(new TextEncoder().encode(str)); },
    toBytes(s) {
      s = s.replace(/-/g, "+").replace(/_/g, "/");
      while (s.length % 4) s += "=";
      const bin = atob(s);
      const out = new Uint8Array(bin.length);
      for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
      return out;
    },
  };

  const push = {
    key: "",        // shared keypair blob as distributed over ctl
    priv: null,     // imported ECDSA private CryptoKey (for signing)
    pubB64: "",     // raw public key base64url (VAPID 'k=' + applicationServerKey)
    enabled: false, // this client has an active subscription
    _gen: false,

    // ingestKey imports a newly-seen shared keypair; re-subscribes if already on
    // (the key changes each session, so an existing subscription must rebind).
    async ingestKey(blob) {
      if (!blob || blob === push.key || !pushSupported) return;
      try {
        const { pub, priv } = JSON.parse(blob);
        push.priv = await crypto.subtle.importKey(
          "jwk", priv, { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
        push.pubB64 = pub;
        push.key = blob;
      } catch { return; }
      if (push.enabled) push.subscribe().catch(() => {});
    },

    // generateIfHost mints the session VAPID keypair once (host only) and shares
    // it so peers can subscribe and any player can sign.
    async generateIfHost() {
      if (state.role !== "host" || push.key || push._gen || !pushSupported) return;
      push._gen = true;
      try {
        const kp = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign"]);
        const pub = b64u.fromBytes(new Uint8Array(await crypto.subtle.exportKey("raw", kp.publicKey)));
        const jwk = await crypto.subtle.exportKey("jwk", kp.privateKey);
        const blob = JSON.stringify({ pub, priv: jwk });
        await push.ingestKey(blob);      // import locally for our own sends
        send({ type: "push.key", pushKey: blob });
      } catch { /* keygen unsupported — notifications just stay off */ }
      push._gen = false;
    },

    // enable requests permission and subscribes (needs the shared pub first).
    async enable() {
      if (!pushSupported || !push.pubB64) {
        toast(window.T("toast.notifyNotReady","Notifications aren't ready yet — try again in a moment."));
        return;
      }
      let perm = Notification.permission;
      if (perm === "default") perm = await Notification.requestPermission();
      if (perm !== "granted") {
        toast(perm === "denied" ? window.T("toast.notifyBlocked","Notifications are blocked in your browser settings.")
                              : window.T("toast.notifyNotEnabled","Notifications not enabled."));
        syncNotifyButton();
        return;
      }
      try {
        await push.subscribe();
        toast(window.T("toast.notifyOn","You'll be notified when it's your turn."));
      } catch {
        toast(window.T("toast.notifyFailed","Couldn't enable notifications (on iOS, install to the Home Screen first)."));
      }
      syncNotifyButton();
    },

    async subscribe() {
      const reg = await navigator.serviceWorker.ready;
      let sub = await reg.pushManager.getSubscription();
      if (sub) {
        const cur = sub.options && sub.options.applicationServerKey;
        const same = cur && b64u.fromBytes(new Uint8Array(cur)) === push.pubB64;
        if (!same) { await sub.unsubscribe().catch(() => {}); sub = null; }
      }
      if (!sub) {
        sub = await reg.pushManager.subscribe({
          userVisibleOnly: true,
          applicationServerKey: b64u.toBytes(push.pubB64),
        });
      }
      push.enabled = true;
      send({ type: "push.endpoint", endpoint: sub.endpoint });
    },

    // notify sends peerId an empty "your turn" push, signed in-browser and
    // forwarded by the relay (which holds no keys and sees only the endpoint).
    async notify(peerId) {
      const endpoint = state.endpoints[peerId];
      if (!endpoint || !push.priv || !push.pubB64) return;
      try {
        const aud = new URL(endpoint).origin;
        const header = b64u.fromStr(JSON.stringify({ typ: "JWT", alg: "ES256" }));
        const payload = b64u.fromStr(JSON.stringify({
          aud, exp: Math.floor(Date.now() / 1000) + 12 * 3600,
          sub: "mailto:kibitz@users.noreply.github.com",
        }));
        const input = header + "." + payload;
        const sig = await crypto.subtle.sign(
          { name: "ECDSA", hash: "SHA-256" }, push.priv, new TextEncoder().encode(input));
        const jwt = input + "." + b64u.fromBytes(new Uint8Array(sig));
        const res = await fetch("/push", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ endpoint, authorization: `vapid t=${jwt}, k=${push.pubB64}`, ttl: 60 }),
        });
        if (res.status === 404 || res.status === 410) delete state.endpoints[peerId]; // gone
      } catch { /* best-effort */ }
    },
  };

  function seated(id) {
    const r = state.members[id];
    return r === "host" || r === "player";
  }

  // maybeNotifyTurn fires a push to the opponent when a move hands them the turn.
  // Only the mover sends (turn is now the OTHER seated player); the recipient's
  // own client sees turn===self and stays quiet. First sighting just records.
  function maybeNotifyTurn(gameId, turnId) {
    const prev = lastTurn[gameId];
    lastTurn[gameId] = turnId;
    if (prev === undefined || turnId === prev || !turnId) return; // no transition / pending
    if (state.solo || !seated(state.self) || turnId === state.self || !seated(turnId)) return;
    push.notify(turnId);
  }

  // renderWatching surfaces spectator presence: players see "👁 N watching";
  // a spectator sees "👁 Watching" (the namesake, made visible to everyone).
  function renderWatching() {
    const el = $("watching");
    if (!el) return;
    const n = Object.values(state.members).filter((r) => r === "spectator").length;
    if (state.role === "spectator") {
      el.textContent = n > 1
      ? window.T("table.watchingN","👁 Watching · {n} here").replace("{n}", n)
      : window.T("table.watching","👁 Watching");
      el.classList.remove("hidden");
    } else if (n > 0) {
      el.textContent = window.T("table.watchersN","👁 {n} watching").replace("{n}", n);
      el.classList.remove("hidden");
    } else {
      el.classList.add("hidden");
    }
  }

  function syncNotifyButton() {
    const b = $("btn-notify");
    if (!b) return;
    const show = pushSupported && !state.solo && state.inSession &&
      !push.enabled && Notification.permission !== "denied";
    b.classList.toggle("hidden", !show);
  }
  if ($("btn-notify")) $("btn-notify").addEventListener("click", () => push.enable());

  // ---- roster + chat --------------------------------------------------------

  function renderMembers() {
    const el = $("members");
    el.innerHTML = "";
    const roleLabel = { host: "♔", player: "♟", spectator: "👁" };
    const roleName = { host: "host", player: "player", spectator: "spectator" };
    for (const [idStr, role] of Object.entries(state.members)) {
      const id = Number(idStr);
      const label = state.names[id] || ("#" + id);
      const div = document.createElement("div");
      div.className = "member";
      const icon = document.createElement("span");
      icon.className = "role-icon";
      icon.textContent = roleLabel[role] || "";
      icon.title = roleName[role] || role;
      div.append(icon, document.createTextNode(label + (id === state.self ? " (you)" : "")));
      el.appendChild(div);
    }
  }

  function appendChat(from, text) {
    const log = $("chat-log");
    const div = document.createElement("div");
    div.className = "chat-msg" + (from === state.self ? " own" : "");
    const who = document.createElement("span");
    who.className = "who";
    who.textContent = displayName(from);
    div.appendChild(who);
    div.appendChild(document.createTextNode(" " + text));
    log.appendChild(div);
    log.scrollTop = log.scrollHeight;
  }

  // ---- user input -----------------------------------------------------------

  // Screen name: remembered across visits, sent with create/join.
  const nameInput = $("display-name");
  nameInput.value = localStorage.getItem("kibitz.name") || "";
  const myName = () => {
    const n = nameInput.value.trim().slice(0, 24);
    localStorage.setItem("kibitz.name", n);
    return n;
  };

  $("btn-create").addEventListener("click", () => {
    $("btn-create").disabled = true;
    $("home-status").textContent = window.T("home.openingTable","opening a table…");
    send({ type: "create", name: myName() });
  });

  // Computer difficulty (remembered): Easy = random, Medium = mostly best with
  // occasional slips, Hard = full heuristics.
  const DIFFICULTIES = ["easy", "medium", "hard"];
  let difficulty = localStorage.getItem("kibitz.difficulty");
  if (!DIFFICULTIES.includes(difficulty)) difficulty = "easy";
  function setDifficulty(d) {
    difficulty = d;
    localStorage.setItem("kibitz.difficulty", d);
    for (const x of DIFFICULTIES) $("diff-" + x).setAttribute("aria-pressed", String(d === x));
  }
  for (const x of DIFFICULTIES) $("diff-" + x).addEventListener("click", () => setDifficulty(x));
  setDifficulty(difficulty); // reflect the stored choice on load

  $("btn-solo").addEventListener("click", () => {
    $("btn-solo").disabled = true;
    $("home-status").textContent = window.T("home.settingUpBot","setting up a game vs the computer…");
    send({ type: "solo", mode: "bot", level: difficulty, name: myName() });
  });
  $("btn-hotseat").addEventListener("click", () => {
    $("btn-hotseat").disabled = true;
    $("home-status").textContent = window.T("home.settingUpHotseat","setting up a pass-and-play game…");
    send({ type: "solo", mode: "hotseat", name: myName() });
  });

  $("btn-join").addEventListener("click", joinFromInput);
  $("join-phrase").addEventListener("keydown", (e) => {
    if (e.key === "Enter") joinFromInput();
  });
  // Enter in the name field joins too, once a phrase is present (e.g. after
  // arriving via a share link with the phrase pre-filled).
  $("display-name").addEventListener("keydown", (e) => {
    if (e.key === "Enter" && $("join-phrase").value.trim()) joinFromInput();
  });
  function joinFromInput() {
    const phrase = $("join-phrase").value.trim();
    if (phrase) {
      const spectate = $("watch-toggle").checked;
      $("home-status").textContent = spectate
      ? window.T("home.watchingPhrase","watching {phrase}…").replace("{phrase}", phrase)
      : window.T("home.joiningPhrase","joining {phrase}…").replace("{phrase}", phrase);
      send({ type: "join", phrase, name: myName(), spectate });
    }
  }

  let watchLink = "";
  let prevHost = 0; // last-seen host id, to detect a migration handoff
  $("btn-copy").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText($("lobby-url").value);
      toast(window.T("toast.playLinkCopied","Play link copied."));
    } catch {
      $("lobby-url").select();
      toast(window.T("toast.pressToCopy","Press ⌘C / Ctrl-C to copy."));
    }
  });
  $("btn-copy-watch").addEventListener("click", async () => {
    if (!watchLink) return;
    try {
      await navigator.clipboard.writeText(watchLink);
      toast(window.T("toast.watchLinkCopied","Watch link copied — send it to anyone who wants to kibitz."));
    } catch {
      toast(window.T("toast.watchLinkFailed","Couldn't copy — the watch link is your table link with #watch=."));
    }
  });

  $("chat-form").addEventListener("submit", (e) => {
    e.preventDefault();
    const input = $("chat-input");
    const text = input.value.trim();
    if (text) {
      send({ type: "chat.say", text });
      input.value = "";
    }
  });

  // ---- version badge (all screens) -----------------------------------------

  fetch("version")
    .then((r) => (r.ok ? r.text() : Promise.reject()))
    .then((v) => {
      v = v.trim();
      if (!v) return;
      const el = $("version-badge");
      el.textContent = v;
      el.classList.remove("hidden");
    })
    .catch(() => {}); // no relay (e.g. opened as a file) → leave hidden

  // ---- boot the core --------------------------------------------------------

  (async () => {
    if (typeof Go === "undefined") {
      $("home-status").textContent = window.T("home.wasmMissing","wasm_exec.js missing — run `make wasm`");
      return;
    }
    try {
      const go = new Go();
      const result = await WebAssembly.instantiateStreaming(fetch("kibitz.wasm"), go.importObject);
      go.run(result.instance);
    } catch (err) {
      $("home-status").textContent = window.T("home.coreLoadFailed","couldn't load the core: {err}").replace("{err}", err);
    }
  })();

  // Register the service worker so the app is installable and its shell loads
  // offline. Fire-and-forget after load so it never competes with the WASM
  // fetch; failure (unsupported, insecure context) is non-fatal.
  if ("serviceWorker" in navigator) {
    window.addEventListener("load", () => {
      navigator.serviceWorker.register("service-worker.js").catch(() => {});
    });
  }
})();
