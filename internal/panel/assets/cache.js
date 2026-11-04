// Async panel telemetry is confined to the small display region that owns it.
// It never replaces the document, registers repeated listeners, or treats stale
// health as proof of a healthy connection. A navigation aborts outstanding work.
(function () {
  "use strict";
  var t = window.t || function (s) { return s; };
  var controller = new AbortController();
  var resume = function () {};
  window.addEventListener("pageshow", function (event) {
    if (!event.persisted) return;
    stopped = false;
    controller = new AbortController();
    resume();
  });
  var stopped = false;
  var timer;
  window.addEventListener("pagehide", function () {
    stopped = true;
    window.clearTimeout(timer);
    controller.abort();
  });

  function get(path) {
    return window.panelCSRF.fetch(path, { credentials: "same-origin", cache: "no-store", signal: controller.signal }).then(function (response) {
      if (!response.ok) throw new Error("panel snapshot unavailable");
      return response.json();
    });
  }
  function freshness(note, snapshot) {
    note.textContent = t("Status atualizado.") + " " + new Date(snapshot.checked_at).toLocaleTimeString(document.documentElement.lang);
  }

  var region = document.querySelector("[data-status-snapshot]");
  var account = document.querySelector("[data-account-snapshot-note]");
  if (region || account) {
    var content = region && region.querySelector("[data-status-content]");
    var note = region ? region.querySelector("[data-snapshot-note]") : account;
    var retry = document.querySelector(region ? "[data-snapshot-retry]" : "[data-account-snapshot-retry]");
    var pending = false;
    function refresh() {
      if (pending || stopped) return;
      window.clearTimeout(timer);
      if (document.hidden) { timer = window.setTimeout(refresh, 2000); return; }
      pending = true;
      retry.hidden = true;
      if (region) region.setAttribute("aria-busy", "true");
      get(region ? "/api/panel/status" : "/api/panel/health").then(function (snapshot) {
        if (stopped || !snapshot.fresh) return;
        if (content) {
          // HTML is exclusively the server's html/template-escaped display-only
          // fragment, never Telegram markup or a script supplied by messages.
          var template = document.createElement("template");
          template.innerHTML = snapshot.html;
          content.replaceChildren(template.content);
        } else {
          document.querySelector("[data-account-messages]").textContent = Number(snapshot.messages).toLocaleString(document.documentElement.lang);
          document.querySelector("[data-account-conversations]").textContent = Number(snapshot.conversations).toLocaleString(document.documentElement.lang);
        }
        freshness(note, snapshot);
      }).catch(function () {
        if (stopped) return;
        note.textContent = t("Não foi possível atualizar o status agora.") + " " + t("Os valores exibidos podem estar desatualizados.");
        retry.hidden = false;
      }).finally(function () {
        pending = false;
        if (region) region.setAttribute("aria-busy", "false");
        if (!stopped) timer = window.setTimeout(refresh, region ? 3000 : 10000);
      });
    }
    retry.addEventListener("click", refresh);
    resume = refresh;
    refresh();
  }

  var banner = document.querySelector("[data-reconfigure]");
  var discovery = document.querySelector("[data-client-discovery-note]");
  var cliButtons = document.querySelectorAll('[data-add-client="claude-code"], [data-add-client="codex"]');
  if (banner || cliButtons.length) {
    if (!discovery) {
      discovery = document.createElement("p");
      discovery.className = "busy";
      discovery.setAttribute("role", "status");
      discovery.textContent = t("Verificando ferramentas instaladas…");
      cliButtons[0].parentNode.insertBefore(discovery, cliButtons[0]);
    }
    get("/api/panel/clients").then(function (clients) {
      if (stopped) return;
      cliButtons.forEach(function (button) {
        var key = button.getAttribute("data-add-client");
        var info = clients[key];
        button.disabled = !info.found;
        button.removeAttribute("title");
        if (banner) button.hidden = !info.found || info.configured;
        var missing = document.querySelector('[data-discovery-missing="' + key + '"]');
        if (missing) missing.hidden = info.found;
        var detail = document.querySelector('[data-discovery-detail="' + key + '"]');
        if (detail) detail.textContent = info.configured ? t("Já usa o endereço novo.") : info.found ? t("Ele guarda o endereço com a porta: atualize com um clique.") : t("Comando não encontrado neste computador.");
      });
      var codex = document.querySelector("[data-codex-discovery]");
      if (codex) codex.hidden = !clients.codex.configured && !clients.codex.stale;
      if (banner && (clients["claude-code"].stale || clients.codex.stale)) banner.hidden = false;
      discovery.hidden = true;
    }).catch(function () {
      if (!stopped) discovery.textContent = t("Não foi possível verificar as ferramentas agora. As instruções manuais continuam disponíveis.");
    });
  }
})();
