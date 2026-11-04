// The panel's behaviour. Copy buttons and busy forms are v1's; the rest is what
// a local daemon adds: pairing that starts by itself, a chat preview to compare
// with the phone, and screens that move on when the next thing happens.
(function () {
  "use strict";

  function api(path, body) {
    var options = { headers: { Accept: "application/json" }, credentials: "same-origin" };
    if (body !== undefined) {
      options.method = "POST";
      options.headers["Content-Type"] = "application/json";
      options.body = JSON.stringify(body);
    }
    return window.panelCSRF.fetch(path, options).then(function (response) {
      return response.json().catch(function () { return {}; }).then(function (data) {
        if (!response.ok) throw new Error(data.error || t("erro %d", response.status));
        return data;
      });
    });
  }

  function el(tag, cls, text) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined && text !== null) node.textContent = text;
    return node;
  }

  // ---- copy buttons (v1) ----
  function label(button, text, restoreAfter) {
    button.textContent = text;
    if (restoreAfter) {
      window.setTimeout(function () {
        button.textContent = t("Copiar");
        button.classList.remove("copy--done", "copy--failed");
      }, 2000);
    }
  }
  function copy(text, button) {
    if (!navigator.clipboard) {
      button.classList.add("copy--failed");
      label(button, t("Selecione e copie"), true);
      return;
    }
    navigator.clipboard.writeText(text).then(
      function () { button.classList.add("copy--done"); label(button, t("Copiado"), true); },
      function () { button.classList.add("copy--failed"); label(button, t("Falhou"), true); }
    );
  }
  document.querySelectorAll("[data-copy]").forEach(function (block) {
    var source = block.querySelector("code") || block;
    var button = el("button", "copy", t("Copiar"));
    button.type = "button";
    button.addEventListener("click", function () { copy(source.textContent, button); });
    var snippet = block.closest(".snippet");
    var head = snippet && snippet.querySelector(".snippet__head");
    if (head) { head.appendChild(button); return; }
    if (snippet) { snippet.classList.add("snippet--loose"); snippet.insertBefore(button, block); return; }
    block.parentNode.insertBefore(button, block);
  });

  // ---- busy forms (v1) ----
  document.querySelectorAll("form[data-busy]").forEach(function (form) {
    form.addEventListener("submit", function (event) {
      if (form.dataset.state === "busy") { event.preventDefault(); return; }
      form.dataset.state = "busy";
      var button = form.querySelector("button[type=submit]");
      window.setTimeout(function () {
        if (!button) return;
        button.disabled = true;
        button.textContent = form.getAttribute("data-busy") || t("Aguarde");
        button.insertBefore(el("span", "spinner"), button.firstChild);
      }, 0);
    });
  });

  // ---- one-click client setup ----
  document.querySelectorAll("[data-add-client]").forEach(function (button) {
    button.addEventListener("click", function () {
      var client = button.getAttribute("data-add-client");
      var note = document.querySelector('[data-client-note="' + client + '"]');
      var text = button.textContent;
      button.disabled = true;
      button.textContent = t("Configurando…");
      var add = function (replace) {
        return api("/api/clients/" + client, { replace: replace }).then(function (result) {
          if (result.conflict !== true) return result;
          // Another server already answers to this name, most likely the
          // hosted v1. Its command and arguments may contain credentials, so
          // the API deliberately returns only the conflict flag and client.
          var yes = window.confirm(t("O %s já tem um servidor chamado \"telegram\", que aponta para:", result.client) + "\n\n—\n\n" +
            t("Substituir por este Telegram MCP?") + " " +
            t("A configuração antiga é removida do %s, e uma cópia do arquivo fica guardada ao lado.", result.client));
          if (!yes) throw new Error(t("Nada foi alterado: o servidor que já estava configurado continua lá."));
          return add(true);
        });
      };
      add(false).then(function () {
        button.textContent = t("Configurado ✓");
        if (note) {
          note.hidden = false;
          note.textContent = {
            "claude-desktop": t("Pronto. Agora feche o Claude Desktop e abra de novo."),
            "claude-code": t("Pronto. Agora abra uma sessão nova do Claude Code."),
            "codex": t("Pronto. Agora abra uma conversa nova no Codex."),
            "cursor": t("Pronto. Agora feche o Cursor e abra de novo.")
          }[client];
        }
      }).catch(function (error) {
        button.disabled = false;
        button.textContent = text;
        if (note) { note.hidden = false; note.textContent = error.message; }
      });
    });
  });

  // ---- waiting for a client ----
  // The daemon stamps every client that talks to it, so the page only has to
  // ask. Polling stops after about ten minutes: by then a reload is honest.
  var waiting = document.querySelector("[data-wait-client='true']");
  if (waiting) {
    var polls = 0;
    var watch = window.setInterval(function () {
      if (++polls > 150) { window.clearInterval(watch); return; }
      api("/api/state").then(function (state) {
        if (state.clients_live > 0) { window.clearInterval(watch); window.location.reload(); }
      }).catch(function () {});
    }, 4000);
  }

  // ---- waiting for one tool ----
  // The connect flow's last step: the page turns into "connected" once the
  // tool it is about connects after the page was opened, or, for any other
  // tool, once one that was not here before does.
  var waitTool = document.querySelector("[data-wait-tool]");
  if (waitTool) {
    var toolClient = waitTool.getAttribute("data-wait-tool");
    var knownClients = (waitTool.getAttribute("data-wait-known") || "").split(" ").filter(Boolean);
    var waitSince = Number(waitTool.getAttribute("data-wait-since") || 0) * 1000;
    var toolPolls = 0;
    var toolWatch = window.setInterval(function () {
      if (++toolPolls > 150) { window.clearInterval(toolWatch); return; }
      api("/api/state").then(function (state) {
        var hit = (state.clients || []).some(function (c) {
          if (!toolClient) return knownClients.indexOf(c.name) < 0;
          return c.name.indexOf(toolClient) === 0 && Date.parse(c.last_seen) >= waitSince;
        });
        if (hit) {
          window.clearInterval(toolWatch);
          window.location.replace(window.location.pathname + "?conectado=1");
        }
      }).catch(function () {});
    }, 4000);
  }

  // ---- language ----
  // The first screen's buttons and the choice in Configurações: kept in
  // config.json, and the page comes back in the new language.
  // byPointer is whether the last choice came from a mouse or a finger,
  // rather than the keyboard.
  var byPointer = false;
  document.addEventListener("pointerdown", function () { byPointer = true; }, true);
  document.addEventListener("keydown", function () { byPointer = false; }, true);
  function chooseLanguage(code, controls) {
    var note = document.querySelector("[data-language-note]");
    controls.forEach(function (c) { c.disabled = true; });
    return api("/api/idioma", { language: code }).then(function () {
      // The page comes back where it was; when the choice was made with the
      // keyboard, with the focus on the language chosen. Not after a click,
      // where the focus would keep its tooltip open.
      try {
        if (byPointer) window.sessionStorage.removeItem("language-focus");
        else window.sessionStorage.setItem("language-focus", code);
      } catch (e) {}
      window.location.reload();
    }).catch(function (error) {
      controls.forEach(function (c) { c.disabled = false; });
      if (note) { note.hidden = false; note.textContent = error.message; }
    });
  }
  var picks = Array.prototype.slice.call(document.querySelectorAll("[data-language-pick]"));
  picks.forEach(function (button) {
    button.addEventListener("click", function () {
      if (button.getAttribute("aria-pressed") === "true") return;
      picks.forEach(function (b) { b.setAttribute("aria-pressed", b === button ? "true" : "false"); });
      chooseLanguage(button.getAttribute("data-language-pick"), picks);
    });
  });
  var radios = Array.prototype.slice.call(document.querySelectorAll("[data-language]"));
  radios.forEach(function (input) {
    input.addEventListener("change", function () { chooseLanguage(input.value, radios); });
  });
  var refocus = null;
  try {
    refocus = window.sessionStorage.getItem("language-focus");
    window.sessionStorage.removeItem("language-focus");
  } catch (e) {}
  if (refocus !== null) {
    picks.concat(radios).some(function (control) {
      var code = control.getAttribute("data-language-pick");
      if (code === null) code = control.value;
      if (code !== refocus) return false;
      control.focus({ preventScroll: true });
      return true;
    });
  }

  // ---- login ----
  // Telegram: a QR code that renews by itself, or the phone number, the code
  // Telegram sends and, when the account has one, the 2FA password. The code
  // and the password are sent once and never kept in the page.
  var pairing = document.querySelector("[data-pairing]");
  if (pairing) {
    var parts = {
      qr: pairing.querySelector("[data-pair-qr]"),
      starting: pairing.querySelector("[data-pair-starting]"),
      code: pairing.querySelector("[data-pair-code]"),
      password: pairing.querySelector("[data-pair-password]"),
      sync: pairing.querySelector("[data-pair-sync]"),
      error: pairing.querySelector("[data-pair-error]")
    };
    var img = pairing.querySelector("[data-qr]");
    var wait = pairing.querySelector("[data-qr-wait]");
    var codeInput = pairing.querySelector("[data-code-input]");
    var passwordInput = pairing.querySelector("[data-password-input]");
    var lastQR = -1;
    var refreshingQR = false;
    var usingPhone = false;
    var shown = "";

    function showOnly(name) {
      Object.keys(parts).forEach(function (key) { parts[key].hidden = key !== name; });
      if (name !== shown) {
        shown = name;
        if (name === "code") codeInput.focus();
        if (name === "password") passwordInput.focus();
      }
    }
    function fail(error) {
      showOnly("error");
      pairing.querySelector("[data-error-text]").textContent = error.message;
    }
    function start(phone) {
      return api("/api/pair", phone ? { method: "phone", phone: phone } : { method: "qr" }).catch(fail);
    }
    function note(which, text) {
      var node = pairing.querySelector(which);
      node.hidden = !text;
      node.textContent = text || "";
    }
    function render(state) {
      if (state.account && state.account.authenticated && state.pairing.state !== "syncing") {
        window.location.reload();
        return;
      }
      var p = state.pairing;
      switch (p.state) {
        case "qr":
          if (usingPhone) break;
          showOnly("qr");
          // qr_seq changes whenever there is a new code to draw, whether
          // Telegram renewed it or the panel asked for a new one.
          var seq = state.qr_seq || p.qr_version;
          if (seq !== lastQR) {
            lastQR = seq;
            img.src = "/api/pair/qr.png?v=" + seq;
            img.hidden = false;
            wait.hidden = true;
          }
          // Polling state is read-only. Renewal is an explicit protected POST.
          if (!refreshingQR) {
            refreshingQR = true;
            api("/api/pair/refresh", {}).catch(function () {}).then(function () { refreshingQR = false; });
          }
          break;
        case "code":
          showOnly("code");
          pairing.querySelector("[data-code-phone]").textContent = p.phone ? t("Código enviado para %s", p.phone) : "";
          pairing.querySelector("[data-code-via]").textContent = state.code_via || "";
          if (p.code_length) codeInput.setAttribute("maxlength", String(p.code_length));
          break;
        case "password":
          showOnly("password");
          var hint = pairing.querySelector("[data-password-hint]");
          hint.hidden = !p.password_hint;
          hint.textContent = p.password_hint ? t("Dica da senha: %s", p.password_hint) : "";
          break;
        case "starting":
          showOnly(usingPhone ? "starting" : "qr");
          break;
        case "syncing":
          showOnly("sync");
          if (state.account && state.account.authenticated) window.location.reload();
          break;
        case "error":
          showOnly("error");
          pairing.querySelector("[data-error-text]").textContent = t("Não foi possível conectar: %s", state.pairing_error || p.error);
          break;
        case "done":
          window.location.reload();
          return;
        default:
          // Idle or cancelled: start, so the QR code is simply there.
          if (!usingPhone) start();
      }
    }
    function poll() {
      api("/api/state").then(render).catch(function () {}).then(function () { window.setTimeout(poll, 1500); });
    }

    pairing.querySelector("[data-pair-phone-toggle]").addEventListener("click", function () {
      var form = pairing.querySelector("[data-pair-phone]");
      form.hidden = !form.hidden;
      if (!form.hidden) pairing.querySelector("[data-pair-phone-input]").focus();
    });
    pairing.querySelector("[data-pair-phone]").addEventListener("submit", function (event) {
      event.preventDefault();
      var button = pairing.querySelector("[data-pair-phone-go]");
      var phone = pairing.querySelector("[data-pair-phone-input]").value;
      usingPhone = true;
      button.disabled = true;
      showOnly("starting");
      api("/api/pair/cancel", {}).then(function () { return start(phone); }).then(function () { button.disabled = false; });
    });
    parts.code.addEventListener("submit", function (event) {
      event.preventDefault();
      var button = pairing.querySelector("[data-code-go]");
      var code = codeInput.value.replace(/\s+/g, "");
      if (!code) return;
      button.disabled = true;
      note("[data-code-note]", t("Conferindo…"));
      api("/api/pair/code", { code: code }).then(function (login) {
        codeInput.value = "";
        note("[data-code-note]", "");
        render({ pairing: login });
      }).catch(function (error) {
        note("[data-code-note]", error.message);
        codeInput.select();
      }).then(function () { button.disabled = false; });
    });
    pairing.querySelector("[data-code-resend]").addEventListener("click", function (event) {
      var button = event.currentTarget;
      button.disabled = true;
      api("/api/pair/resend", {}).then(function () {
        note("[data-code-note]", t("Pedimos um novo código ao Telegram."));
      }).catch(function (error) {
        note("[data-code-note]", error.message);
      }).then(function () { button.disabled = false; });
    });
    parts.password.addEventListener("submit", function (event) {
      event.preventDefault();
      var button = pairing.querySelector("[data-password-go]");
      if (!passwordInput.value) return;
      button.disabled = true;
      note("[data-password-note]", t("Conferindo…"));
      var body = { password: passwordInput.value };
      // The password leaves the field as soon as it is sent.
      passwordInput.value = "";
      api("/api/pair/password", body).then(function (login) {
        note("[data-password-note]", "");
        render({ pairing: login });
      }).catch(function (error) {
        note("[data-password-note]", error.message);
        passwordInput.focus();
      }).then(function () { body.password = ""; button.disabled = false; });
    });
    Array.prototype.forEach.call(pairing.querySelectorAll("[data-pair-back]"), function (back) {
      back.addEventListener("click", function () {
        usingPhone = false;
        lastQR = -1;
        img.hidden = true;
        wait.hidden = false;
        codeInput.value = "";
        passwordInput.value = "";
        showOnly("qr");
        api("/api/pair/cancel", {}).then(function () { return start(); });
      });
    });
    pairing.querySelector("[data-pair-retry]").addEventListener("click", function () {
      usingPhone = false;
      lastQR = -1;
      showOnly("qr");
      api("/api/pair/cancel", {}).then(function () { return start(); });
    });
    poll();
  }

  // ---- chat preview ----
  // The latest chats, drawn like the app's list, and the last messages of the
  // one selected: enough to see at a glance that what arrives here is what
  // Telegram shows. On top of that: filters by name, unread and type, more
  // chats and older messages a page at a time, and a search inside the chat,
  // all from what is stored on this computer. A newer request cancels the one
  // it replaces, so a slow answer never draws over a newer one.
  var preview = document.querySelector("[data-chats]");
  if (preview) {
    var palette = ["#3346c4", "#7a5bd8", "#e0654a", "#8e5bd8", "#d6a01b", "#2c9f6a", "#c2477f", "#4a6fd6"];
    var list = preview.querySelector("[data-chat-list]");
    var countPill = preview.querySelector("[data-chats-count]");
    var thread = preview.querySelector("[data-thread]");
    var threadTitle = preview.querySelector("[data-thread-title]");
    var threadMeta = preview.querySelector("[data-thread-meta]");
    var threadAvatar = preview.querySelector("[data-thread-avatar]");
    var historyNote = preview.querySelector("[data-history-note]");
    var filterForm = preview.querySelector("[data-chat-filters]");
    var filterQ = preview.querySelector("[data-chat-q]");
    var filterUnread = preview.querySelector("[data-chat-unread]");
    var filterType = preview.querySelector("[data-chat-type]");
    var chatsStatus = preview.querySelector("[data-chats-status]");
    var moreChats = preview.querySelector("[data-chats-more]");
    var searchForm = preview.querySelector("[data-thread-search]");
    var searchQ = preview.querySelector("[data-thread-q]");
    var searchClear = preview.querySelector("[data-thread-clear]");
    var olderButton = preview.querySelector("[data-thread-older]");
    var threadStatus = preview.querySelector("[data-thread-status]");
    var CHATS_PAGE = 10, MESSAGES_PAGE = 30, WAIT_MS = 15000;
    var chats = [];
    var chatsNext = 0, chatsMore = false, chatsLoaded = false, chatsStale = false;
    // listError is the shown generation's last failure, kept on screen
    // until a read of that generation succeeds.
    var listError = null;
    var selected = null;
    var shown = "";
    var messages = [], threadMore = false, threadQuery = "";
    var listCall = null, threadCall = null;
    // The list and the open chat refresh on their own: a failure of one
    // never stops the other. After a failure a stream waits 1, 3, then 7
    // ticks of 15s before its next try; a success or a manual retry resets it.
    var listBack = { fails: 0, skip: 0 }, threadBack = { fails: 0, skip: 0 };
    function due(b) { if (b.skip > 0) { b.skip--; return false; } return true; }
    function failed(b) { b.fails = Math.min(b.fails + 1, 3); b.skip = Math.pow(2, b.fails) - 1; }
    function recovered(b) { b.fails = 0; b.skip = 0; }

    // read asks the panel for one page, cancelling call when a newer request
    // replaces it and giving up after WAIT_MS. It resolves to null when the
    // answer is no longer wanted.
    function read(path, previous) {
      if (previous) previous.abort();
      var call = { controller: window.AbortController ? new AbortController() : null, late: false, gone: false };
      call.abort = function () { call.gone = true; if (call.controller) call.controller.abort(); };
      var timer = window.setTimeout(function () { call.late = true; if (call.controller) call.controller.abort(); }, WAIT_MS);
      var options = { headers: { Accept: "application/json" }, credentials: "same-origin" };
      if (call.controller) options.signal = call.controller.signal;
      call.done = window.panelCSRF.fetch(path, options).then(function (response) {
        return response.json().catch(function () { return {}; }).then(function (data) {
          if (!response.ok) throw new Error(data.error || t("erro %d", response.status));
          return data;
        });
      }).then(function (data) {
        window.clearTimeout(timer);
        return call.gone ? null : data;
      }, function (error) {
        window.clearTimeout(timer);
        if (call.gone) return null;
        if (call.late) throw new Error(t("O painel demorou demais para responder. Tente de novo."));
        if (error && error.name === "TypeError") throw new Error(t("Não foi possível falar com o Telegram MCP neste computador. Confira se ele está aberto."));
        throw error;
      });
      return call;
    }
    // readWindow reads again, from the newest, everything a list already
    // shows: count rows in pages of at most 100, plus one row for each
    // arrival it meets (fresh tells), up to count more, so a refresh keeps
    // what was loaded without keeping what the store no longer has. Like
    // read, it can be aborted and resolves to null once no longer wanted.
    function readWindow(path, key, count, fresh) {
      var win = { gone: false, page: null };
      win.abort = function () { win.gone = true; if (win.page) win.page.abort(); };
      var rows = [], offset = 0, arrived = 0;
      function next() {
        var page = win.page = read(path(offset, Math.min(100, count + arrived - offset)), null);
        return page.done.then(function (data) {
          if (!data || win.gone) return null;
          var got = data[key] || [];
          got.forEach(function (r) { rows.push(r); if (fresh(r) && arrived < count) arrived++; });
          offset += got.length;
          if (data.has_more && got.length && offset < count + arrived) return next();
          return { rows: rows, has_more: !!data.has_more, next_offset: data.has_more ? (data.next_offset || offset) : offset, data: data };
        });
      }
      win.done = next();
      return win;
    }
    function busy(node, on) {
      if (on) node.setAttribute("aria-busy", "true");
      else node.removeAttribute("aria-busy");
    }
    // say shows a line under the list or the chat: loading with a spinner,
    // an error with a way to try again, or nothing.
    function say(node, text, kind, retry) {
      node.textContent = "";
      node.hidden = !text;
      if (!text) return;
      node.setAttribute("role", kind === "error" ? "alert" : "status");
      if (kind === "loading") {
        var spin = el("span", "spinner");
        spin.setAttribute("aria-hidden", "true");
        node.appendChild(spin);
      }
      node.appendChild(el("span", "", text));
      if (retry) {
        var again = el("button", "btn btn--ghost btn--small", t("Tentar de novo"));
        again.type = "button";
        again.addEventListener("click", retry);
        node.appendChild(again);
      }
    }

    function color(key) {
      var h = 0;
      for (var i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
      return palette[h % palette.length];
    }
    function initial(name) {
      var m = (name || "").match(/[\p{L}\p{N}]/u);
      return m ? m[0].toUpperCase() : "#";
    }
    function paintAvatar(node, name, key) {
      node.textContent = initial(name);
      node.style.background = color(key);
    }
    function when(iso) {
      if (!iso) return "";
      var d = new Date(iso), now = new Date();
      if (d.toDateString() === now.toDateString()) return d.toLocaleTimeString(document.documentElement.lang, { hour: "2-digit", minute: "2-digit" });
      if (new Date(now.getTime() - 86400000).toDateString() === d.toDateString()) return t("Ontem");
      if (now - d < 6 * 86400000) return d.toLocaleDateString(document.documentElement.lang, { weekday: "long" });
      return d.toLocaleDateString(document.documentElement.lang, { day: "2-digit", month: "2-digit", year: "2-digit" });
    }
    var mediaLabels = { image: "📷 " + t("Foto"), video: "🎥 " + t("Vídeo"), audio: "🎤 " + t("Áudio"), document: "📄 " + t("Documento"), sticker: t("Figurinha"), location: "📍 " + t("Localização"), live_location: "📍 " + t("Localização em tempo real"), contact: "👤 " + t("Contato"), poll: "📊 " + t("Enquete") };
    function mediaText(media, text) {
      var lbl = mediaLabels[media] || (media ? t("Mídia") : "");
      if (lbl && text && text.charAt(0) !== "[") return lbl + " · " + text;
      return lbl || text;
    }
    function firstName(name) {
      return /^\+/.test(name) ? name : name.split(" ")[0];
    }
    function chatAt(c) { return c.last_at || c.at; }
    function unreadOf(c) { return c.unread || c.unread_count || 0; }
    // The filters a list was read with. Every request that extends or
    // refreshes the list uses them, never the live controls: only a reset
    // (a new filter) reads the controls, so one list never mixes filters.
    function currentFilter() { return { q: filterQ.value.trim(), unread: !!filterUnread.checked, type: filterType.value || "" }; }
    var listFilter = currentFilter();
    function filtered() { return !!(listFilter.q || listFilter.unread || listFilter.type); }

    function renderChats() {
      list.textContent = "";
      countPill.textContent = chats.length ? (chats.length === 1 ? t("1 conversa") : t("%d conversas", chats.length)) : t("nenhuma ainda");
      moreChats.hidden = !chatsMore;
      // Nothing read yet for the filter in the controls: no rows at all,
      // neither another filter's nor an "empty" that was never read.
      if (!chatsLoaded) { countPill.textContent = "…"; return; }
      if (!chats.length) {
        list.appendChild(el("li", "empty", filtered()
          ? t("Nenhuma conversa guardada combina com esses filtros.")
          : t("Nenhuma conversa guardada ainda. Elas aparecem aqui assim que a sincronização as trouxer.")));
        return;
      }
      chats.forEach(function (c) {
        var li = el("li");
        var on = selected && selected.jid === c.jid;
        var row = el("button", "chat" + (on ? " chat--on" : ""));
        row.type = "button";
        row.setAttribute("data-chat-jid", c.jid);
        if (on) row.setAttribute("aria-current", "true");
        var av = el("span", "avatar");
        paintAvatar(av, c.name, c.jid);
        row.appendChild(av);
        var main = el("span", "chat__main");
        var top = el("span", "chat__top");
        top.appendChild(el("span", "chat__name", c.name));
        top.appendChild(el("span", "chat__time" + (unreadOf(c) ? " chat__time--unread" : ""), when(chatAt(c))));
        var bottom = el("span", "chat__bottom");
        var prefix = c.last_from_me ? t("Você") + ": " : (c.group && c.last_sender ? firstName(c.last_sender) + ": " : "");
        // A failed refresh cannot prove a preview still exists: show the
        // chat, not its last message, until the store answers again.
        bottom.appendChild(el("span", "chat__last", chatsStale ? "" : prefix + mediaText(c.last_media, c.last_text)));
        if (unreadOf(c)) bottom.appendChild(el("span", "chat__badge", String(unreadOf(c))));
        else if (c.pinned) bottom.appendChild(el("span", "chat__pin", "📌"));
        main.appendChild(top);
        main.appendChild(bottom);
        row.appendChild(main);
        row.addEventListener("click", function () { select(c); });
        li.appendChild(row);
        list.appendChild(li);
      });
    }

    function bubble(m, showWho) {
      var b = el("div", "bubble" + (m.from_me ? " bubble--me" : ""));
      b.setAttribute("data-message-id", m.id);
      if (showWho && !m.from_me && m.sender) b.appendChild(el("span", "bubble__who", m.sender));
      if (m.media) b.appendChild(el("div", "bubble__media", mediaLabels[m.media] || t("Mídia")));
      if (m.text && m.text.charAt(0) !== "[") b.appendChild(el("div", "bubble__text", m.text));
      if (m.transcript) {
        var tr = el("div", "bubble__transcript", m.transcript);
        if (m.transcript_corrected) tr.title = t("Corrigida pelo contexto da conversa");
        b.appendChild(tr);
      }
      b.appendChild(el("span", "bubble__time", new Date(m.at).toLocaleTimeString(document.documentElement.lang, { hour: "2-digit", minute: "2-digit" })));
      return b;
    }

    function select(c) {
      if (!selected || selected.jid !== c.jid) {
        historyNote.hidden = true;
        shown = "";
        messages = [];
        recovered(threadBack);
        threadQuery = "";
        searchQ.value = "";
        searchClear.hidden = true;
        // Another chat: what the last one showed goes away at once.
        olderButton.hidden = true;
        threadMeta.textContent = "";
        thread.textContent = "";
        thread.appendChild(el("div", "skeleton"));
      }
      selected = c;
      renderChats();
      threadTitle.textContent = c.name;
      paintAvatar(threadAvatar, c.name, c.jid);
      loadThread("open");
    }

    // Messages follow the store's total order, (date, id): Telegram dates
    // have one-second resolution, so the date alone does not order them.
    // The server gives each message its position as cursor "date:id".
    function keyOf(m) {
      var k = /^(\d+):(\d+)$/.exec(m.cursor || "");
      return k ? [Number(k[1]), Number(k[2])] : [Math.floor(new Date(m.at).getTime() / 1000), 0];
    }
    function byKey(a, b) { var x = keyOf(a), y = keyOf(b); return x[0] - y[0] || x[1] - y[1]; }
    // merge keeps each message once, oldest first, whatever order a page
    // arrives in; the copy in fresh wins, so an edit replaces what was kept.
    function merge(kept, fresh) {
      var seen = {}, out = [];
      fresh.concat(kept).forEach(function (m) {
        if (!seen[m.id]) { seen[m.id] = true; out.push(m); }
      });
      return out.sort(byKey);
    }
    function threadPath(c, query, limit, since, before) {
      return "/api/chats/" + encodeURIComponent(c.jid) + "?limit=" + limit +
        (since ? "&since=" + encodeURIComponent(since) : "") +
        (before ? "&before=" + encodeURIComponent(before) : "") +
        // This field is text, not an FTS5 expression. A quoted phrase keeps
        // punctuation/operators literal; doubled quotes escape embedded quotes.
        // Keep the API's FTS contract unchanged for programmatic callers.
        (query ? "&q=" + encodeURIComponent('"' + query.replace(/"/g, '""') + '"') : "");
    }
    // readThreadWindow reads again, newest first and by cursor, every
    // message from since (the oldest one shown) on, so arrivals and
    // deletions between two pages neither repeat nor skip a message; then,
    // if fewer than count remain, the ones just before, as opening the chat
    // would. A one-row probe tells whether anything older is left. Like
    // read, it can be aborted and resolves to null once no longer wanted.
    function readThreadWindow(c, query, since, count) {
      var win = { gone: false, page: null };
      win.abort = function () { win.gone = true; if (win.page) win.page.abort(); };
      var rows = [], before = "", ranged = !!since;
      function next() {
        var need = count - rows.length, probe = !ranged && need <= 0;
        var limit = ranged ? 100 : (probe ? 1 : Math.min(100, need));
        var page = win.page = read(threadPath(c, query, limit, ranged ? since : "", before), null);
        return page.done.then(function (data) {
          if (!data || win.gone) return null;
          var got = data.messages || [];
          if (probe) return { rows: rows, has_more: got.length > 0, data: data };
          rows = got.concat(rows);
          if (got.length) before = got.reduce(function (a, m) { return byKey(m, a) < 0 ? m : a; }).cursor;
          if (data.has_more && got.length && (ranged || rows.length < count)) return next();
          if (ranged) { ranged = false; return next(); }
          return { rows: rows, has_more: !!data.has_more, data: data };
        });
      }
      win.done = next();
      return win;
    }
    function drawThread(c, keepPlace) {
      // The whole content, not only the IDs: edits, media and transcripts
      // keep their message's ID.
      var key = JSON.stringify([!!c.group, messages]);
      if (key === shown) return;
      shown = key;
      var fromBottom = thread.scrollHeight - thread.scrollTop;
      thread.textContent = "";
      var lastDay = "";
      messages.forEach(function (m) {
        var day = new Date(m.at).toLocaleDateString(document.documentElement.lang, { day: "2-digit", month: "long", year: "numeric" });
        if (day !== lastDay) { thread.appendChild(el("div", "thread__day", day)); lastDay = day; }
        thread.appendChild(bubble(m, c.group));
      });
      if (!messages.length) thread.appendChild(el("div", "empty", threadQuery
        ? t("Nenhuma mensagem guardada desta conversa tem esse texto.")
        : t("Nenhuma mensagem desta conversa ainda.")));
      // Older messages go on top without moving what is being read.
      thread.scrollTop = keepPlace ? thread.scrollHeight - fromBottom : thread.scrollHeight;
    }
    function drawMeta(c, data) {
      if (threadQuery) {
        threadMeta.textContent = (messages.length === 1 ? t("1 mensagem encontrada.") : t("%d mensagens encontradas.", messages.length));
        return;
      }
      var stored = c.messages || 0;
      threadMeta.textContent = (stored === 1 ? t("1 mensagem guardada") : t("%s mensagens guardadas", stored.toLocaleString(document.documentElement.lang))) +
        (data.oldest ? ", " + t("desde %s", new Date(data.oldest).toLocaleDateString(document.documentElement.lang, { day: "2-digit", month: "short", year: "numeric" })) : "") +
        " · " + (messages.length === 1 ? t("a última aqui") : t("as últimas %d aqui", messages.length));
    }
    // loadThread reads the selected chat: "open" draws its latest page,
    // "older" adds the page before the oldest shown, and "poll" reads again,
    // in the background and without a loading line, every message shown.
    // The store's answer replaces what was shown: a message deleted or
    // expired there, or no longer matching the search, leaves the screen.
    function loadThread(mode) {
      if (!selected) return;
      if (mode === "poll" && threadCall) return;
      var c = selected, query = threadQuery;
      if (mode !== "poll") {
        busy(thread, true);
        say(threadStatus, mode === "older" ? t("Carregando mensagens mais antigas…") : (query ? t("Procurando…") : t("Carregando mensagens…")), "loading");
        olderButton.disabled = true;
      }
      var call;
      if (mode === "poll") {
        call = readThreadWindow(c, query, messages.length ? messages[0].cursor : "", Math.max(messages.length, MESSAGES_PAGE));
      } else {
        call = read(threadPath(c, query, MESSAGES_PAGE, "", mode === "older" && messages.length ? messages[0].cursor : ""), threadCall);
      }
      threadCall = call;
      call.done.then(function (data) {
        if (!data || !selected || selected.jid !== c.jid || threadCall !== call || threadQuery !== query) return;
        threadCall = null;
        recovered(threadBack);
        var atBottom = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 40;
        if (mode === "poll") {
          messages = merge([], data.rows);
          threadMore = data.has_more;
          data = data.data;
        } else {
          var page = data.messages || [];
          messages = mode === "open" ? merge([], page) : merge(messages, page);
          threadMore = !!data.has_more;
        }
        busy(thread, false);
        say(threadStatus, "");
        olderButton.disabled = false;
        olderButton.hidden = !threadMore;
        drawThread(c, mode === "older" || (mode === "poll" && !atBottom));
        drawMeta(c, data);
      }).catch(function (error) {
        if (threadCall !== call) return;
        threadCall = null;
        failed(threadBack);
        busy(thread, false);
        olderButton.disabled = false;
        // A failed refresh is not proof that retained text is still visible.
        // Fail closed rather than keeping deleted/expired/unavailable content.
        if (mode === "open" || mode === "poll") {
          messages = [];
          thread.textContent = "";
          shown = "";
          threadMore = false;
          olderButton.hidden = true;
        }
        say(threadStatus, error.message, "error", function () { recovered(threadBack); loadThread("open"); });
      });
    }

    searchForm.addEventListener("submit", function (event) {
      event.preventDefault();
      if (!selected) return;
      threadQuery = searchQ.value.trim();
      searchClear.hidden = !threadQuery;
      shown = "";
      loadThread("open");
    });
    searchClear.addEventListener("click", function () {
      searchQ.value = "";
      threadQuery = "";
      searchClear.hidden = true;
      shown = "";
      loadThread("open");
    });
    olderButton.addEventListener("click", function () { loadThread("older"); });

    function requestHistory(chat, button, note) {
      button.disabled = true;
      api("/api/history", chat ? { chat_jid: chat } : {}).then(function (data) {
        note.hidden = false;
        note.textContent = data.started
          ? t("Pedido enviado ao Telegram. As mensagens mais antigas chegam aos poucos; a tela se atualiza sozinha.")
          : t("Já há um pedido de histórico em andamento. Assim que ele terminar, dá para pedir de novo.");
        watchHistory(button, note);
      }).catch(function (error) {
        button.disabled = false;
        note.hidden = false;
        note.textContent = error.message;
      });
    }
    function watchHistory(button, note) {
      var tries = 0;
      var timer = window.setInterval(function () {
        if (++tries > 90) { window.clearInterval(timer); button.disabled = false; return; }
        api("/api/chats?limit=1").then(function (data) {
          var job = data.history;
          if (job && job.finished_at) {
            window.clearInterval(timer);
            button.disabled = false;
            var failed = job.failures ? Object.keys(job.failures).length : 0;
            note.textContent = job.messages_added
              ? (job.messages_added === 1 ? t("Chegou 1 mensagem mais antiga.") : t("Chegaram %s mensagens mais antigas.", job.messages_added.toLocaleString(document.documentElement.lang)))
              : failed
                ? t("O Telegram não entregou o histórico agora. Confira a conexão e tente de novo mais tarde.")
                : t("O Telegram não tinha mensagens mais antigas para mandar.");
            refresh();
          } else if (job) {
            note.textContent = t("Buscando… %d de %s, %s até agora.", job.chats_done,
              job.chats.length === 1 ? t("1 conversa") : t("%d conversas", job.chats.length),
              job.messages_added === 1 ? t("1 mensagem nova") : t("%s mensagens novas", job.messages_added.toLocaleString(document.documentElement.lang)));
          }
        }).catch(function () {});
      }, 3000);
    }

    var oneChat = preview.querySelector("[data-history-chat]");
    oneChat.addEventListener("click", function () { if (selected) requestHistory(selected.jid, oneChat, historyNote); });
    var allChats = document.querySelector("[data-history-all]");
    if (allChats) allChats.addEventListener("click", function () { requestHistory("", allChats, document.querySelector("[data-history-all-note]")); });

    function chatsPath(f) {
      return function (offset, limit) {
        var path = "/api/chats?limit=" + limit + "&offset=" + offset;
        if (f.q) path += "&q=" + encodeURIComponent(f.q);
        if (f.unread) path += "&unread=true";
        if (f.type) path += "&type=" + encodeURIComponent(f.type);
        return path;
      };
    }
    // once keeps the first row of each chat, in order.
    function once(rows) {
      var have = {};
      return rows.filter(function (c) { return have[c.jid] ? false : (have[c.jid] = true); });
    }
    function sameFilter(a, b) { return a.q === b.q && a.unread === b.unread && a.type === b.type; }
    // showListError keeps the list's last failure on screen, with its retry,
    // until a read of the generation shown succeeds.
    function showListError() {
      if (listError) say(chatsStatus, listError.message, "error", listError.retry);
      else say(chatsStatus, "");
    }
    // loadChats reads the list. The list shows one generation: the rows read
    // with listFilter, fresh (chatsStale false) only after a read of its
    // whole window from the top succeeded. "reset" reads the first page of
    // a filter (from the controls, or want when retrying the generation
    // shown), "more" the next page, and "poll" all the pages already shown
    // again, in the background, to follow what arrives. "more" on a stale
    // list reads the whole window plus the next page, since rows kept from a
    // failed refresh were not read again. A reset that fails for another
    // filter drops the rows of the old one: nothing shown can disagree with
    // the controls, and every retry, manual or automatic, reads the filter
    // that failed.
    function loadChats(mode, want) {
      if (mode === "poll" && listCall) return;
      if (mode === "more" && !chatsLoaded) return;
      var f = mode === "reset" ? (want || currentFilter()) : listFilter;
      var whole = mode === "poll" || (mode === "more" && chatsStale);
      var offset = mode === "more" ? chatsNext : 0;
      if (mode !== "poll") {
        busy(list, true);
        say(chatsStatus, mode === "more" ? t("Carregando mais conversas…") : t("Carregando conversas…"), "loading");
        moreChats.disabled = true;
      }
      var call;
      if (whole) {
        var known = {};
        chats.forEach(function (c) { known[c.jid] = true; });
        // A poll keeps the window and makes room for arrivals; a stale
        // "more" reads the window and one page after it, from the top, so
        // nothing is skipped and every row shown was just read.
        var more = mode === "more";
        if (listCall) listCall.abort();
        call = readWindow(chatsPath(f), "chats", more ? chats.length + CHATS_PAGE : Math.max(chats.length, CHATS_PAGE),
          function (c) { return !more && !known[c.jid]; });
      } else {
        call = read(chatsPath(f)(offset, CHATS_PAGE), listCall);
      }
      listCall = call;
      call.done.then(function (data) {
        if (!data || listCall !== call) return;
        listCall = null;
        recovered(listBack);
        listFilter = f;
        chatsStale = false;
        listError = null;
        if (whole) {
          chats = once(data.rows);
          chatsMore = data.has_more;
          chatsNext = data.next_offset;
          data = data.data;
        } else {
          var page = data.chats || [];
          chats = mode === "more" ? once(chats.concat(page)) : page;
          chatsMore = !!data.has_more;
          chatsNext = data.next_offset || offset + page.length;
        }
        chatsLoaded = true;
        if (selected) {
          var still = chats.filter(function (c) { return c.jid === selected.jid; })[0];
          selected = still || selected;
        }
        busy(list, false);
        say(chatsStatus, "");
        moreChats.disabled = false;
        renderChats();
        if (!selected && chats.length) select(chats[0]);
        else if (!selected) { threadTitle.textContent = ""; thread.textContent = ""; busy(thread, false); }
      }).catch(function (error) {
        if (listCall !== call) return;
        listCall = null;
        busy(list, false);
        moreChats.disabled = false;
        if (mode !== "more") failed(listBack);
        if (!chatsLoaded || !sameFilter(f, listFilter)) {
          // Nothing read for this filter: show no rows rather than another's.
          listFilter = f;
          chats = [];
          chatsLoaded = false;
          chatsStale = false;
          chatsMore = false;
          chatsNext = 0;
        } else if (mode !== "more") {
          // A failed refresh cannot prove a preview still exists.
          chatsStale = true;
        }
        listError = { message: error.message, retry: function () { recovered(listBack); loadChats(mode, f); } };
        renderChats();
        showListError();
      });
    }
    // Every 15s, each stream that is due reads again what it shows: the
    // list always its own generation, never the live controls.
    function refresh() {
      if (due(listBack)) loadChats(chatsLoaded ? "poll" : "reset", listFilter);
      if (selected && due(threadBack)) loadThread("poll");
    }
    // A changed control makes the list being read obsolete at once, before
    // the debounce: its pages must not land under the new filter. The list
    // shown keeps its own state, including a failure.
    function invalidateList() {
      if (!listCall) return;
      listCall.abort();
      listCall = null;
      busy(list, false);
      moreChats.disabled = false;
      showListError();
    }

    var typing = null;
    filterForm.addEventListener("submit", function (event) { event.preventDefault(); window.clearTimeout(typing); loadChats("reset"); });
    filterQ.addEventListener("input", function () {
      invalidateList();
      window.clearTimeout(typing);
      typing = window.setTimeout(function () { loadChats("reset"); }, 300);
    });
    filterUnread.addEventListener("change", function () { loadChats("reset"); });
    filterType.addEventListener("change", function () { loadChats("reset"); });
    moreChats.addEventListener("click", function () { loadChats("more"); });

    refresh();
    window.setInterval(refresh, 15000);
  }

  // The connection, followed in the background: every two seconds while it
  // is on its way, every ten otherwise. The Status tab's alert changes in
  // place; a page that shows the connection refreshes when it moves on,
  // unless the person is in the middle of something there.
  var live = document.querySelector("[data-live]");
  if (live) {
    var liveKey = live.getAttribute("data-live");
    var liveTone = live.getAttribute("data-live-tone");
    var liveBusy = live.hasAttribute("data-live-busy");
    var liveRefreshing = false;
    var showsSync = !!document.querySelector("[data-sync]") && !document.querySelector("[data-status-snapshot]");
    var reloading = false;

    var showTone = function (tone) {
      var tab = live.querySelector("[data-nav-status]");
      if (!tab) return;
      var mark = tab.querySelector(".nav__alert");
      var said = tab.querySelector(".sr-only");
      if (tone === "ok") {
        if (mark) mark.remove();
        if (said) said.remove();
        return;
      }
      if (!mark) {
        said = el("span", "sr-only", t("Atenção:") + " ");
        mark = el("span", "nav__alert", "!");
        mark.setAttribute("aria-hidden", "true");
        tab.insertBefore(said, tab.querySelector(".nav__label"));
        tab.appendChild(mark);
      }
      mark.classList.toggle("nav__alert--warn", tone === "warn");
    };

    var undisturbed = function () {
      var active = document.activeElement;
      if (active && /^(INPUT|TEXTAREA|SELECT)$/.test(active.tagName)) return false;
      if (document.querySelector("form[data-state=busy]")) return false;
      var open = window.location.hash.length > 1 && document.getElementById(window.location.hash.slice(1));
      return !(open && open.classList.contains("overlay"));
    };

    var follow = function () {
      window.setTimeout(function () {
        if (document.hidden || reloading) { follow(); return; }
        api("/api/pulse").then(function (pulse) {
          liveBusy = pulse.busy;
          liveRefreshing = !pulse.fresh;
          if (showsSync && pulse.key !== liveKey && undisturbed()) {
            reloading = true;
            window.location.reload();
            return;
          }
          if (pulse.fresh && pulse.tone !== liveTone) {
            liveTone = pulse.tone;
            showTone(liveTone);
          }
        }).catch(function () {}).then(follow);
      }, liveRefreshing ? 350 : (liveBusy ? 2000 : 10000));
    };
    follow();
  }

  // The first history sync may still be arriving after pairing.
  var syncing = document.querySelector("[data-sync-note]");
  if (syncing) {
    var syncWatch = window.setInterval(function () {
      api("/api/state").then(function (state) {
        if (state.arriving) {
          syncing.querySelector("[data-sync-count]").textContent = (state.arriving_count || 0).toLocaleString(document.documentElement.lang);
        } else {
          window.clearInterval(syncWatch);
          syncing.textContent = t("Histórico recebido.");
          syncing.classList.remove("busy");
        }
      }).catch(function () {});
    }, 3000);
  }
})();

// Local transcription install, with its progress.
(function () {
  var button = document.querySelector("[data-asr-install]");
  if (!button) return;
  var progress = document.querySelector("[data-asr-progress]");
  var step = document.querySelector("[data-asr-step]");
  function poll() {
    fetch("/api/transcription", { credentials: "same-origin" }).then(function (r) { return r.json(); }).then(function (d) {
      var i = d.install || {};
      if (d.ready) { window.location.reload(); return; }
      if (i.state === "error") { window.location.reload(); return; }
      if (i.state === "running") {
        progress.hidden = false;
        step.textContent = (i.step || t("Preparando")) + (i.total ? " · " + Math.floor(100 * i.downloaded / i.total) + "%" : "");
      }
      window.setTimeout(poll, 1500);
    }).catch(function () { window.setTimeout(poll, 3000); });
  }
  button.addEventListener("click", function () {
    button.disabled = true;
    progress.hidden = false;
    step.textContent = t("Preparando…");
    window.panelCSRF.fetch("/api/asr/install", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: "{}" })
      .then(function () { poll(); });
  });
  if (!progress.hidden) poll();
})();

// Inside the desktop app: links leave for the browser, settings save as they
// change, and updates are looked for and installed from the Configurações page.
(function () {
  "use strict";
  if (!document.documentElement.hasAttribute("data-app")) return;

  function el(tag, cls) {
    var node = document.createElement(tag);
    if (cls) node.className = cls;
    return node;
  }

  function post(path, body) {
    return window.panelCSRF.fetch(path, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/json" }, body: JSON.stringify(body || {}) })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (data) {
          if (!r.ok) throw new Error(data.error || t("erro %d", r.status));
          return data;
        });
      });
  }

  // The window shows only the panel: anything on the web opens in the
  // browser, the way a link in any other app does.
  document.addEventListener("click", function (event) {
    var link = event.target.closest && event.target.closest("a[href]");
    if (!link) return;
    var href = link.getAttribute("href");
    if (!/^https?:\/\//i.test(href)) return;
    event.preventDefault();
    post("/api/abrir", { url: href }).catch(function () {});
  });

  document.querySelectorAll("[data-setting]").forEach(function (input) {
    input.addEventListener("change", function () {
      var body = {};
      body[input.getAttribute("data-setting")] = input.checked;
      var note = input.closest(".card__body, .wizard-shell, body").querySelector("[data-setting-note]");
      input.disabled = true;
      post("/api/configuracoes", body).then(function () {
        if (note) note.hidden = true;
      }).catch(function (error) {
        input.checked = !input.checked;
        if (note) { note.hidden = false; note.textContent = error.message; }
      }).then(function () { input.disabled = false; });
    });
  });

  var folder = document.querySelector("[data-open-folder]");
  if (folder) folder.addEventListener("click", function () { post("/api/abrir-pasta").catch(function () {}); });

  // Updates: the card in Configurações and the banner on every page show the
  // same state, asked for every minute so a version found in the background
  // appears without a reload.
  var card = document.querySelector("[data-update]");
  var banner = document.querySelector("[data-update-banner]");
  if (!card && !banner) return;
  var timer = null;
  var DISMISSED = "tgmcp-update-dismissed";

  function today() { return new Date().toISOString().slice(0, 10); }
  function dismissed(version) {
    try { return window.localStorage.getItem(DISMISSED) === version + "@" + today(); } catch (e) { return false; }
  }

  function describe(u) {
    switch (u.state) {
      case "checking": return t("Procurando uma versão nova…");
      case "current": return t("Você já tem a versão mais recente.");
      case "available": return t("A versão %s está disponível. O aplicativo será reiniciado após a instalação.", u.latest);
      case "downloading": return t("Baixando a versão %s…", u.latest) + " " + (u.progress || 0) + "%";
      case "ready": return t("Versão %s pronta. O app está reiniciando.", u.latest);
      case "manual": return t("A versão %s está disponível. Neste sistema, ela é instalada pelo pacote: baixe e instale como da primeira vez.", u.latest);
      case "unsupported": return t("Esta é uma versão de desenvolvimento (%s), que não se atualiza sozinha.", u.current);
      case "error": return t("Não foi possível atualizar agora: %s", u.error);
    }
    return "";
  }

  // The card shows the installed version only; a badge beside it says
  // whether it is the latest, and a new version gets the loud one.
  function badge(u) {
    switch (u.state) {
      case "current": return { tone: "pill--ok", text: t("Mais recente") };
      case "available": case "manual": return { tone: "pill--new", text: t("Nova versão %s", u.latest) };
      case "checking": return { tone: "pill--off pill--busy", text: t("Procurando…"), spin: true };
      case "downloading": return { tone: "pill--new pill--busy", text: t("Baixando %s…", u.latest) + " " + (u.progress || 0) + "%", spin: true };
      case "ready": return { tone: "pill--new pill--busy", text: t("Reiniciando…"), spin: true };
      case "unsupported": return { tone: "pill--off", text: t("Versão de desenvolvimento") };
    }
    return null;
  }

  function note(u) {
    switch (u.state) {
      case "available": case "downloading": return t("O aplicativo será reiniciado após a instalação.");
      case "manual": return t("Neste sistema, ela é instalada pelo pacote: baixe e instale como da primeira vez.");
      case "unsupported": return t("Ela não se atualiza sozinha.");
      case "error": return t("Não foi possível atualizar agora: %s", u.error);
    }
    return "";
  }

  function renderCard(u) {
    if (!card) return;
    var install = card.querySelector("[data-update-install]");
    var page = card.querySelector("[data-update-page]");
    var pill = card.querySelector("[data-update-badge]");
    var b = badge(u);
    pill.hidden = !b;
    if (b) {
      pill.className = "pill " + b.tone;
      pill.textContent = b.text;
      if (b.spin) {
        var spin = el("span", "spinner");
        spin.setAttribute("aria-hidden", "true");
        pill.insertBefore(spin, pill.firstChild);
      }
    }
    var text = card.querySelector("[data-update-text]");
    text.textContent = note(u);
    text.hidden = !text.textContent;
    install.hidden = u.state !== "available";
    install.disabled = false;
    page.hidden = !(u.state === "manual" && u.page);
    if (u.page) page.href = u.page;
  }

  function renderBanner(u) {
    if (!banner) return;
    var shown = (u.state === "available" || u.state === "manual") ? !dismissed(u.latest) : (u.state === "downloading" || u.state === "ready");
    banner.hidden = !shown;
    if (!shown) return;
    var text = banner.querySelector("[data-update-banner-text]");
    var install = banner.querySelector("[data-update-banner-install]");
    var page = banner.querySelector("[data-update-banner-page]");
    install.hidden = u.state === "manual";
    page.hidden = u.state !== "manual";
    if (u.page) page.href = u.page;
    if (u.state === "available") {
      text.textContent = t("O Telegram MCP %s está disponível. O aplicativo será reiniciado após a instalação.", u.latest);
      install.disabled = false;
      install.textContent = t("Atualizar agora");
    } else if (u.state === "manual") {
      text.textContent = t("O Telegram MCP %s está disponível. Baixe e instale o pacote novo.", u.latest);
    } else {
      text.textContent = describe(u);
      install.disabled = true;
    }
  }

  function render(u) {
    renderCard(u);
    renderBanner(u);
    window.clearTimeout(timer);
    // Follow a download closely; otherwise look again in a minute.
    timer = window.setTimeout(poll, (u.state === "checking" || u.state === "downloading" || u.state === "ready") ? 1000 : 60000);
  }
  function poll() {
    fetch("/api/atualizacao", { credentials: "same-origin" }).then(function (r) { return r.json(); }).then(render).catch(function () {
      timer = window.setTimeout(poll, 60000);
    });
  }

  function busy(button, label) {
    button.disabled = true;
    button.textContent = label;
    var spin = el("span", "spinner");
    spin.setAttribute("aria-hidden", "true");
    button.insertBefore(spin, button.firstChild);
  }
  function install(button) {
    busy(button, t("Atualizando…"));
    post("/api/atualizacao/instalar").then(render).catch(function (e) {
      button.disabled = false;
      button.textContent = t("Tentar de novo");
      var text = (banner && !banner.hidden) ? banner.querySelector("[data-update-banner-text]") : card && card.querySelector("[data-update-text]");
      if (text) { text.textContent = e.message; text.hidden = false; }
    });
  }

  if (card) {
    var check = card.querySelector("[data-update-check]");
    check.addEventListener("click", function () {
      busy(check, t("Procurando…"));
      // The answer comes back once GitHub has replied: a new version, or
      // none, or why it could not be asked.
      post("/api/atualizacao/verificar").then(render).catch(function (e) {
        var text = card.querySelector("[data-update-text]");
        text.textContent = t("Não foi possível procurar agora: %s", e.message);
        text.hidden = false;
      }).then(function () {
        check.disabled = false;
        check.textContent = t("Procurar atualização");
      });
    });
    var cardInstall = card.querySelector("[data-update-install]");
    cardInstall.addEventListener("click", function () { install(cardInstall); });
  }
  if (banner) {
    var bannerInstall = banner.querySelector("[data-update-banner-install]");
    bannerInstall.addEventListener("click", function () { install(bannerInstall); });
    banner.querySelector("[data-update-banner-close]").addEventListener("click", function () {
      banner.hidden = true;
      fetch("/api/atualizacao", { credentials: "same-origin" }).then(function (r) { return r.json(); }).then(function (u) {
        try { window.localStorage.setItem(DISMISSED, u.latest + "@" + today()); } catch (e) {}
      }).catch(function () {});
    });
  }
  poll();
})();

// The write mode, on the Conta tab: always a person's choice, confirmed in a
// dialog (allowing writes also needs the box ticked), and sent as exactly
// {"mode": …, "confirm": true} with the session's CSRF token (SEC-R04).
(function () {
  "use strict";
  document.querySelectorAll("[data-mode-form]").forEach(function (form) {
    var box = form.querySelector("[data-mode-confirm]");
    var submit = form.querySelector("[data-mode-submit]");
    var error = form.querySelector("[data-mode-error]");
    var label = submit.textContent;
    var target = form.getAttribute("data-mode-target");
    function ready() { submit.disabled = form.dataset.state === "busy" || (!!box && !box.checked); }
    if (box) {
      box.checked = false;
      box.addEventListener("change", ready);
    }
    ready();
    form.addEventListener("submit", function (event) {
      event.preventDefault();
      if (form.dataset.state === "busy" || (box && !box.checked)) return;
      if (target !== "read_write" && target !== "read_only") return;
      form.dataset.state = "busy";
      ready();
      submit.textContent = form.getAttribute("data-mode-busy") || t("Aguarde");
      error.hidden = true;
      window.panelCSRF.fetch("/api/mode", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ mode: target, confirm: true })
      }).then(function (response) {
        return response.json().catch(function () { return {}; }).then(function (data) {
          if (!response.ok) throw new Error(data.error || t("erro %d", response.status));
          var done = target === "read_write" ? t("Escrita permitida. Cada ação ainda mostra a prévia antes.") : t("Somente leitura de novo: nada muda no Telegram.");
          window.location.assign("/conta?ok=" + encodeURIComponent(done));
        });
      }).catch(function (err) {
        form.dataset.state = "";
        submit.textContent = label;
        if (box) box.checked = false;
        ready();
        error.textContent = err.message;
        error.hidden = false;
      });
    });
  });
})();

// Feedback for a click on a link to another page of the panel. Some pages
// take seconds to build (Status reads the whole local health report), and
// until the answer arrives the browser shows nothing: a bar along the top
// says at once that the click was taken, a note says so again when the page
// is slow, and an alert offers to try again when it does not come. Nothing is
// intercepted: the browser navigates as it would, and a click another script
// already took (the desktop app's links to the web) is left alone.
(function () {
  "use strict";
  var SLOW_MS = 4000;
  var STUCK_MS = 25000;
  var bar = null, note = null, timers = [], busyLink = null;

  function clear() {
    timers.forEach(function (id) { window.clearTimeout(id); });
    timers = [];
    if (bar) { bar.remove(); bar = null; }
    if (note) { note.remove(); note = null; }
    if (busyLink) { busyLink.removeAttribute("aria-busy"); busyLink = null; }
  }

  function say(text, late, href) {
    if (!note) {
      note = document.createElement("div");
      note.className = "navnote";
      document.body.appendChild(note);
    }
    note.textContent = "";
    note.classList.toggle("navnote--late", !!late);
    note.setAttribute("role", late ? "alert" : "status");
    note.appendChild(document.createElement("span")).textContent = text;
    if (late) {
      var again = document.createElement("button");
      again.type = "button";
      again.className = "btn btn--small";
      again.textContent = t("Tentar de novo");
      again.addEventListener("click", function () { window.location.assign(href); });
      var stay = document.createElement("button");
      stay.type = "button";
      stay.className = "btn btn--quiet btn--small";
      stay.textContent = t("Ficar nesta página");
      stay.addEventListener("click", function () { if (window.stop) window.stop(); clear(); });
      note.appendChild(again);
      note.appendChild(stay);
    }
  }

  function start(link, href) {
    clear();
    busyLink = link;
    link.setAttribute("aria-busy", "true");
    bar = document.createElement("div");
    bar.className = "navload";
    bar.setAttribute("aria-hidden", "true");
    document.body.appendChild(bar);
    timers.push(window.setTimeout(function () { say(t("Carregando a página…"), false, href); }, SLOW_MS));
    timers.push(window.setTimeout(function () {
      if (bar) { bar.remove(); bar = null; }
      say(t("A página está demorando para responder. Confira se o Telegram MCP continua aberto e tente de novo."), true, href);
    }, STUCK_MS));
  }

  document.addEventListener("click", function (event) {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    var link = event.target.closest && event.target.closest("a[href]");
    if (!link || link.hasAttribute("download") || (link.target && link.target !== "_self")) return;
    var url;
    try { url = new URL(link.getAttribute("href"), window.location.href); } catch (e) { return; }
    if (url.origin !== window.location.origin) return;
    // Files and API answers (a log, an image) are no page: nothing to wait for.
    if (/^\/(api|assets)\//.test(url.pathname) || /\.[a-z0-9]+$/i.test(url.pathname)) return;
    // A link to a part of this same page (a dialog, a section) is no navigation.
    if (url.pathname === window.location.pathname && url.search === window.location.search) return;
    // Decide after every other listener has had the click.
    window.setTimeout(function () { if (!event.defaultPrevented) start(link, url.href); }, 0);
  });
  // Back to a page kept in the browser's memory: nothing is loading any more.
  window.addEventListener("pageshow", clear);
})();
