// The panel's CSRF proof (docs/contratos/security.md §3–4). In the browser the
// page holds a session cookie; every request that changes something must also
// carry the session's CSRF token, which the page reads from GET /api/session:
// fetches send it as X-CSRF-Token, plain form posts as the csrf field. In the
// desktop window there is no browser session (the window reaches the panel in
// memory), so /api/session answers 401 and the requests go out without it.
// Loaded first, in the head of every page, so the other scripts can use
// window.panelCSRF.fetch.
(function () {
  "use strict";

  var pending = null;

  // token is the session's CSRF token, or "" without a session. A failure is
  // not remembered, so the next request asks again.
  function token() {
    if (!pending) {
      pending = fetch("/api/session", { credentials: "same-origin", headers: { Accept: "application/json" } })
        .then(function (response) {
          if (!response.ok) return "";
          return response.json().then(function (data) {
            return data && typeof data.csrf_token === "string" ? data.csrf_token : "";
          });
        })
        .catch(function () { return ""; });
      pending.then(function (value) { if (!value) pending = null; });
    }
    return pending;
  }

  function unsafe(method) {
    method = String(method || "GET").toUpperCase();
    return method !== "GET" && method !== "HEAD";
  }

  // guardedFetch is fetch, with the token on every request that is not a read.
  function guardedFetch(path, options) {
    options = options || {};
    if (!unsafe(options.method)) return fetch(path, options);
    return token().then(function (value) {
      var copy = {};
      for (var key in options) {
        if (Object.prototype.hasOwnProperty.call(options, key)) copy[key] = options[key];
      }
      copy.method = String(options.method).toUpperCase();
      copy.credentials = "same-origin";
      copy.headers = new Headers(options.headers || {});
      if (value) copy.headers.set("X-CSRF-Token", value);
      return fetch(path, copy);
    });
  }

  function isPost(form) {
    return !!form && form.tagName === "FORM" && String(form.getAttribute("method") || "").toLowerCase() === "post";
  }

  function stamp(form, value) {
    if (value) {
      var input = form.querySelector("input[name=csrf]");
      if (!input) {
        input = document.createElement("input");
        input.type = "hidden";
        input.name = "csrf";
        form.appendChild(input);
      }
      input.value = value;
      form.dataset.csrf = "ready";
    }
  }

  // send posts a form the way the browser would, but as a same-origin fetch:
  // the HTTP guard sends Referrer-Policy: no-referrer, under which a browser
  // posts a plain form with "Origin: null", and the guard refuses that. A
  // fetch keeps the page's real Origin. The answer is a redirect, followed
  // here and then shown by navigating to it.
  function send(form, submitter, value) {
    var body = new URLSearchParams(submitter ? new FormData(form, submitter) : new FormData(form));
    body.set("csrf", value);
    return fetch(form.getAttribute("action") || window.location.pathname, {
      method: "POST",
      credentials: "same-origin",
      redirect: "follow",
      headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": value },
      body: body.toString()
    }).then(function (response) {
      if (response.redirected && response.ok) {
        window.location.assign(response.url);
        return;
      }
      // A page in answer (Encerrando, Dados apagados) replaces this one, as
      // a form post would; anything else is an error shown as text. The
      // markup is the panel's own answer to its own same-origin post (only a
      // successful text/html one), exactly what the browser would have shown.
      var html = /^text\/html\b/i.test(response.headers.get("Content-Type") || "");
      return response.text().then(function (text) {
        delete form.dataset.state;
        if (response.ok && html) {
          document.open();
          document.write(text);
          document.close();
          return;
        }
        document.body.textContent = text || String(response.status);
      });
    });
  }

  // Every post form goes with the token. Without a session (the desktop
  // window, trusted in memory) it is posted as it is.
  document.addEventListener("submit", function (event) {
    var form = event.target;
    if (!isPost(form) || form.dataset.csrf === "native") return;
    event.preventDefault();
    var submitter = event.submitter || null;
    token().then(function (value) {
      if (!value) {
        form.dataset.csrf = "native";
        form.submit();
        return;
      }
      stamp(form, value);
      return send(form, submitter, value);
    }).catch(function () {
      document.body.textContent = "error";
    });
  }, true);

  // Stamp the page's forms as soon as it is there, so a click never waits.
  document.addEventListener("DOMContentLoaded", function () {
    var forms = Array.prototype.filter.call(document.querySelectorAll("form"), isPost);
    if (!forms.length) return;
    token().then(function (value) {
      forms.forEach(function (form) { stamp(form, value); });
    });
  });

  window.panelCSRF = { token: token, fetch: guardedFetch };
})();
