// Kibitz i18n — display-layer only.
//
// DESIGN CONSTRAINT, and the reason this file is small: several game outcome
// strings are PROTOCOL VALUES, not prose. Go sets outcome to exactly
// "player 1 wins", "draw", "black wins", "abandoned", … and the boards compare
// against those literals (g.outcome === "draw", g.outcome === "player 1 wins").
// Translating them at the source, or storing a translated copy in state, would
// desync the boards from the engine. So:
//
//   * State is never translated. g.outcome keeps its English protocol value.
//   * Only what is RENDERED goes through t(): labels, status lines, toasts.
//   * Protocol values are mapped at the display boundary via tOutcome().
//
// Default locale is Simplified Chinese; English stays available and is the
// fallback for any key a dictionary is missing.
(function () {
  "use strict";

  const STORAGE_KEY = "kibitz.locale";
  const LANGS = ["zh", "en"];
  const NAMES = { zh: "简体中文", en: "English" };

  let locale = "zh";

  const dict = Object.create(null);   // { zh: {...}, en: {...} }

  // register merges one language table. Called by each board module so a
  // game strings live next to the game, not in one unmaintainable blob.
  function register(lang, table) {
    if (!dict[lang]) dict[lang] = Object.create(null);
    Object.assign(dict[lang], table);
  }

  function detect() {
    let saved = null;
    try { saved = localStorage.getItem(STORAGE_KEY); } catch (_) {}
    if (saved && LANGS.indexOf(saved) >= 0) return saved;
    // Default to Simplified Chinese. A zh-* browser always gets zh.
    return "zh";
  }

  // t looks up a key in the active locale, then English, then the caller English
  // default, so a missing entry degrades to readable text, never an empty string.
  function t(key, fallback) {
    const d = dict[locale];
    if (d && d[key] != null) return d[key];
    const en = dict.en;
    if (en && en[key] != null) return en[key];
    return fallback != null ? fallback : key;
  }

  // tOutcome maps a PROTOCOL outcome value to display text. The input is never
  // modified; this is a read-only projection used only for rendering.
  function tOutcome(outcome) {
    if (!outcome) return "";
    const key = "outcome." + outcome;
    const d = dict[locale];
    if (d && d[key] != null) return d[key];
    const en = dict.en;
    if (en && en[key] != null) return en[key];
    return outcome;
  }

  // apply walks the DOM for data-i18n* hooks so index.html static copy is
  // translated without hard-coding text in this file.
  function apply(root) {
    const scope = root || document;
    scope.querySelectorAll("[data-i18n]").forEach(function (el) {
      el.textContent = t(el.getAttribute("data-i18n"), el.textContent);
    });
    scope.querySelectorAll("[data-i18n-html]").forEach(function (el) {
      el.innerHTML = t(el.getAttribute("data-i18n-html"), el.innerHTML);
    });
    scope.querySelectorAll("[data-i18n-placeholder]").forEach(function (el) {
      el.setAttribute("placeholder", t(el.getAttribute("data-i18n-placeholder"), el.getAttribute("placeholder")));
    });
    scope.querySelectorAll("[data-i18n-title]").forEach(function (el) {
      el.setAttribute("title", t(el.getAttribute("data-i18n-title"), el.getAttribute("title")));
    });
    scope.querySelectorAll("[data-i18n-aria]").forEach(function (el) {
      el.setAttribute("aria-label", t(el.getAttribute("data-i18n-aria"), el.getAttribute("aria-label")));
    });
    document.documentElement.lang = locale === "zh" ? "zh-CN" : "en";
  }

  function set(next) {
    if (LANGS.indexOf(next) < 0) return;
    locale = next;
    try { localStorage.setItem(STORAGE_KEY, next); } catch (_) {}
    apply();
    try {
      window.dispatchEvent(new CustomEvent("kibitz:locale", { detail: { locale: next } }));
    } catch (_) {}
    updateSwitcher();
  }

  function updateSwitcher() {
    const el = document.getElementById("locale-select");
    if (el && el.value !== locale) el.value = locale;
  }

  function init() {
    locale = detect();
    const sel = document.getElementById("locale-select");
    if (sel) {
      sel.value = locale;
      sel.addEventListener("change", function () { set(sel.value); });
    }
    document.documentElement.lang = locale === "zh" ? "zh-CN" : "en";
    // Translate the static markup. Safe here even at DOMContentLoaded: no board
    // has rendered yet, so nothing is overwritten mid-flight.
    apply();
  }

  window.kibitzI18n = {
    LANGS: LANGS,
    NAMES: NAMES,
    register: register,
    t: t,
    tOutcome: tOutcome,
    apply: apply,
    set: set,
    init: init,
  };
  Object.defineProperty(window.kibitzI18n, "locale", { get: function () { return locale; } });

  // Shorthands the board modules use.
  window.T = function (key, fallback) { return window.kibitzI18n.t(key, fallback); };
  window.TO = function (outcome) { return window.kibitzI18n.tOutcome(outcome); };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
