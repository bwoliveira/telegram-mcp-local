// Progressive enhancement only: every page works without this file.
// Rules (site/DESIGN.md §2.1): no HTML-string sinks, no eval, no network.
// The DOM is changed only through hidden, classList, textContent and dataset.
(function () {
  "use strict";

  var doc = document;
  var root = doc.documentElement;

  function storageGet(key) {
    try {
      return window.localStorage.getItem(key);
    } catch (e) {
      return null;
    }
  }

  function storageSet(key, value) {
    try {
      window.localStorage.setItem(key, value);
    } catch (e) {
      // Storage can be disabled; the hint simply comes back next time.
    }
  }

  // ---- Language hint: suggest, never redirect ----------------------------
  // The hint texts for the other languages are already in the HTML.
  function siteLang(tag) {
    var t = String(tag || "").toLowerCase();
    if (t.indexOf("pt") === 0) return "pt-BR";
    if (t.indexOf("es") === 0) return "es";
    if (t.indexOf("en") === 0) return "en";
    return "";
  }

  function languageHint() {
    var key = "tgmcp-lang-hint";
    if (storageGet(key) === "dismissed") return;
    var langs = navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language];
    var wanted = siteLang(langs[0]);
    if (!wanted || wanted === root.lang) return;
    var hint = doc.querySelector('[data-lang-hint="' + wanted + '"]');
    if (!hint) return;
    hint.hidden = false;
    var close = hint.querySelector("[data-lang-hint-close]");
    if (close) {
      close.addEventListener("click", function () {
        hint.hidden = true;
        storageSet(key, "dismissed");
      });
    }
  }

  // ---- Download page: highlight the visitor's system ---------------------
  // Only a suggestion: no card is hidden and the architecture is not guessed.
  function detectOS() {
    var uaData = navigator.userAgentData;
    var platform = (uaData && uaData.platform) || navigator.platform || "";
    var ua = navigator.userAgent || "";
    var mobile = (uaData && uaData.mobile) || /Android|iPhone|iPad|iPod/i.test(ua) ||
      (/Mac/i.test(platform) && navigator.maxTouchPoints > 1);
    if (mobile) return "";
    if (/Win/i.test(platform) || /Windows/i.test(ua)) return "windows";
    if (/Mac/i.test(platform) || /Mac OS X/i.test(ua)) return "macos";
    if (/Linux|X11/i.test(platform) || /Linux|X11/i.test(ua)) return "linux";
    return "";
  }

  function suggestDownload() {
    var os = detectOS();
    if (!os) return;
    var card = doc.querySelector('.dl-card[data-os="' + os + '"]');
    if (!card) return;
    card.classList.add("is-suggested");
    var badge = card.querySelector("[data-suggested]");
    if (badge) badge.hidden = false;
  }

  // ---- Copy buttons -------------------------------------------------------
  function announce(text) {
    var live = doc.getElementById("live");
    if (!live) return;
    live.textContent = "";
    window.setTimeout(function () {
      live.textContent = text;
    }, 50);
  }

  function copyButtons() {
    if (!navigator.clipboard || !window.isSecureContext) return;
    var live = doc.getElementById("live");
    var done = live ? live.dataset.done : "";
    var fail = live ? live.dataset.fail : "";
    var buttons = doc.querySelectorAll("[data-copy]");
    Array.prototype.forEach.call(buttons, function (button) {
      var code = button.parentNode.querySelector("code");
      if (!code) return;
      button.hidden = false;
      button.addEventListener("click", function () {
        navigator.clipboard.writeText(code.textContent).then(function () {
          announce(done);
        }, function () {
          announce(fail);
        });
      });
    });
  }

  // ---- FAQ deep links: open the linked question -------------------------
  function openFromHash() {
    var id;
    try {
      id = decodeURIComponent(window.location.hash.slice(1));
    } catch (e) {
      return;
    }
    if (!id) return;
    var target = doc.getElementById(id);
    if (target && target.tagName === "DETAILS") target.open = true;
  }

  languageHint();
  suggestDownload();
  copyButtons();
  openFromHash();
  window.addEventListener("hashchange", openFromHash);
})();
