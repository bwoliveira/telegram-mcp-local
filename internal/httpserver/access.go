package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// The /access page is what a browser without a panel session sees. It is
// public, so it is the same static text for everyone: it never reads the
// store, the engine, the query, cookies or headers other than
// Accept-Language, and it has no field where a token could be typed. The
// only value filled in is the loopback port this server listens on, taken
// from the connection rather than from the request, so the SSH tunnel
// command can be copied as it is.

// accessCSS is the panel's palette and card layout (internal/panel/style.go),
// trimmed to what this page uses. Its hash is the page's only style
// allowance; changing a byte changes the hash, which is computed below.
const accessCSS = `*,*::before,*::after{box-sizing:border-box}
:root{color-scheme:light dark;--bg:#eff1f8;--surface:#fff;--surface-sunken:#e8ebf5;--border:#dadff0;--text:#121733;--text-soft:#2e3556;--muted:#545c7e;--brand:#3346c4;--brand-soft:#e6e9fb;--warn:#7a5200;--warn-bg:#fbf0d6;--radius:14px;--radius-sm:10px;--shadow:0 1px 2px rgba(16,36,28,.06),0 8px 24px rgba(16,36,28,.06)}
@media (prefers-color-scheme:dark){:root{--bg:#0b0f1e;--surface:#121831;--surface-sunken:#0e1328;--border:#262f57;--text:#e6e9f7;--text-soft:#cdd2ec;--muted:#a3abd0;--brand:#8c9cff;--brand-soft:#1b2350;--warn:#f2ce85;--warn-bg:#352a10;--shadow:0 1px 2px rgba(0,0,0,.4),0 8px 24px rgba(0,0,0,.3)}}
html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--text);font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
.page{max-width:760px;margin:0 auto;padding:0 clamp(16px,4vw,24px) 56px}
.head{display:flex;align-items:center;gap:12px;padding:20px 0 14px}
.mark{width:38px;height:38px;flex:none}
.name{font-weight:700;color:var(--brand);line-height:1.15;display:flex;flex-direction:column}
.tag{font-weight:500;font-size:.72rem;color:var(--muted)}
h1,h2{line-height:1.25;margin:0;color:var(--text)}
h1{font-size:clamp(1.35rem,1.1rem + 1.2vw,1.7rem);margin:12px 0 6px}
h2{font-size:1.08rem;margin-bottom:8px}
p{margin:.6em 0;color:var(--text-soft)}
.lead{font-size:1.02rem;max-width:62ch}
.card{border:1px solid var(--border);border-radius:var(--radius);background:var(--surface);box-shadow:var(--shadow);padding:clamp(16px,3vw,22px);margin-top:16px}
.card ol{margin:.4em 0 0;padding-left:1.3em;color:var(--text-soft)}
.card li{margin:.45em 0}
code{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:.88em;background:var(--surface-sunken);border:1px solid var(--border);padding:1px 5px;border-radius:5px;overflow-wrap:anywhere}
pre{overflow:auto;background:#0d1230;color:#e6e9ff;padding:12px 14px;border-radius:var(--radius-sm);font-size:.85rem;line-height:1.55;margin:.5em 0}
pre code{background:none;border:0;padding:0;color:inherit;font-size:1em}
.note{border-left:4px solid var(--warn);background:var(--warn-bg);border-radius:var(--radius-sm);padding:10px 14px;margin-top:16px;color:var(--text)}
.note p{color:inherit;margin:0}
`

var accessStyleHash = func() string {
	sum := sha256.Sum256([]byte(accessCSS))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// accessPolicy allows exactly the stylesheet above: no script, image, font,
// form, frame or other source.
var accessPolicy = "default-src 'none'; style-src " + accessStyleHash + "; frame-ancestors 'none'; form-action 'none'; base-uri 'none'"

type accessText struct {
	Title, Tagline, Heading, Lead             string
	LocalHeading, LocalStep, LocalAfter       string
	RemoteHeading, RemoteServer, RemoteTunnel string
	RemoteOpen, RemoteExpiry                  string
	WhyHeading, Why, WhyLink                  string
	Never                                     string
}

// accessTexts uses the shared catalogues without changing the process-wide
// panel language: every unauthenticated browser negotiates independently.
func accessTexts(lang string) accessText {
	t := func(source string) string { return i18n.In(lang, source) }
	return accessText{
		Title:         t(i18n.N("Acesso ao painel")),
		Tagline:       t(i18n.N("Painel local")),
		Heading:       t(i18n.N("Abra o painel pelo computador do serviço")),
		Lead:          t(i18n.N("Este painel só abre com uma sessão do navegador, criada por um link de uso único que o próprio computador onde o Telegram MCP roda gera para você.")),
		LocalHeading:  t(i18n.N("No computador do serviço")),
		LocalStep:     t(i18n.N("Abra um terminal nesse computador e execute:")),
		LocalAfter:    t(i18n.N("O navegador abre o painel já autorizado.")),
		RemoteHeading: t(i18n.N("Em outro computador, por SSH")),
		RemoteServer:  t(i18n.N("No servidor, pelo SSH, peça o link sem abrir um navegador:")),
		RemoteTunnel:  t(i18n.N("No seu computador, abra um túnel SSH para a mesma porta local:")),
		RemoteOpen:    t(i18n.N("Abra no seu navegador o link impresso no passo 1, com o túnel aberto.")),
		RemoteExpiry:  t(i18n.N("O link vale por um minuto e uma única vez. Se expirar, execute o passo 1 de novo.")),
		WhyHeading:    t(i18n.N("Por que é preciso autorizar?")),
		Why:           t(i18n.N("O painel mostra suas conversas e pode agir na sua conta do Telegram. Qualquer site aberto neste computador consegue chamar endereços locais, então o painel só confia em quem já tem acesso ao computador do serviço: quem consegue executar o comando ali.")),
		WhyLink:       t(i18n.N("O link de uso único troca-se por um cookie de sessão e some da barra de endereços. Ele nunca aparece nesta página nem em logs.")),
		Never:         t(i18n.N("Esta página não pede senha nem token: não cole o token do MCP em sites nem no navegador.")),
	}
}

// accessMark is the panel's own mark (internal/panel/brandmark.go), inline
// and without the XML namespace, which HTML does not need.
const accessMark = `<svg class="mark" viewBox="0 0 64 64" aria-hidden="true" focusable="false"><defs><linearGradient id="tgmcp-mark" x1="6" y1="6" x2="58" y2="58" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="#5b7cfa"/><stop offset="1" stop-color="#2a3fb0"/></linearGradient></defs><rect x="4" y="4" width="56" height="56" rx="16" fill="url(#tgmcp-mark)"/><path d="M19 17h26a7 7 0 0 1 7 7v13a7 7 0 0 1-7 7H29l-9 7v-7h-1a7 7 0 0 1-7-7V24a7 7 0 0 1 7-7Z" fill="none" stroke="#fff" stroke-width="3.4" stroke-linejoin="round"/><path d="M23 34l9-7 9 7" fill="none" stroke="#fff" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/><circle cx="23" cy="34" r="3.2" fill="#fff"/><circle cx="32" cy="27" r="3.2" fill="#fff"/><circle cx="41" cy="34" r="3.2" fill="#fff"/></svg>`

var accessTemplate = template.Must(template.New("access").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>{{.T.Title}} · Telegram MCP</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="page">
<header class="head">{{.Mark}}<span class="name">Telegram MCP<span class="tag">{{.T.Tagline}}</span></span></header>
<h1>{{.T.Heading}}</h1>
<p class="lead">{{.T.Lead}}</p>
<section class="card">
<h2>{{.T.LocalHeading}}</h2>
<p>{{.T.LocalStep}}</p>
<pre><code>telegram-mcp open</code></pre>
<p>{{.T.LocalAfter}}</p>
</section>
<section class="card">
<h2>{{.T.RemoteHeading}}</h2>
<ol>
<li>{{.T.RemoteServer}}<pre><code>telegram-mcp open --print</code></pre></li>
<li>{{.T.RemoteTunnel}}<pre><code>ssh -L {{.Port}}:127.0.0.1:{{.Port}} user@server</code></pre></li>
<li>{{.T.RemoteOpen}}</li>
</ol>
<p>{{.T.RemoteExpiry}}</p>
</section>
<section class="card">
<h2>{{.T.WhyHeading}}</h2>
<p>{{.T.Why}}</p>
<p>{{.T.WhyLink}}</p>
</section>
<div class="note"><p>{{.T.Never}}</p></div>
</main>
</body>
</html>
`))

// writeAccessPage answers an unauthenticated GET or HEAD of /access. The
// caller has already passed the Host/Origin guard and set the common
// security headers.
func writeAccessPage(w http.ResponseWriter, r *http.Request) {
	lang := accessLanguage(r.Header.Values("Accept-Language"))
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Language", lang)
	h.Add("Vary", "Accept-Language")
	h.Set("Content-Security-Policy", accessPolicy)
	if r.Method != http.MethodGet {
		return
	}
	var body bytes.Buffer
	if err := accessTemplate.Execute(&body, struct {
		Lang string
		T    accessText
		CSS  template.CSS
		Mark template.HTML
		Port int
	}{lang, accessTexts(lang), template.CSS(accessCSS), template.HTML(accessMark), listeningPort(r)}); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body.Bytes())
}

// listeningPort is the local port of the connection, never the Host header.
// The tunnel command falls back to the default only without a TCP address.
func listeningPort(r *http.Request) int {
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok && addr.Port > 0 {
		return addr.Port
	}
	return 47831
}

// accessLanguage picks pt-BR, es or en from Accept-Language by quality,
// first listed on a tie, and English when none of them is accepted. Only the
// first 32 entries are read, so a huge header costs nothing.
func accessLanguage(values []string) string {
	best, bestQ := "en", 0.0
	seen := 0
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if seen++; seen > 32 {
				return best
			}
			tag, params, _ := strings.Cut(part, ";")
			lang := accessMatch(strings.TrimSpace(tag))
			if lang == "" {
				continue
			}
			q := 1.0
			for _, p := range strings.Split(params, ";") {
				k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
				if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
					parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
					if err != nil || parsed < 0 || parsed > 1 {
						parsed = 0
					}
					q = parsed
				}
			}
			if q > bestQ {
				best, bestQ = lang, q
			}
		}
	}
	return best
}

func accessMatch(tag string) string {
	primary, _, _ := strings.Cut(strings.ToLower(tag), "-")
	switch primary {
	case "pt":
		return "pt-BR"
	case "es":
		return "es"
	case "en":
		return "en"
	}
	return ""
}
