package panel

// pageSource holds every template the panel renders. The shell, the masthead,
// the tabs, the cards, the dialogs and the colophon are v1's markup; what
// differs is what a local daemon has instead of a server: one Telegram account
// rather than instances, and its login by QR code or phone number and 2FA.
const pageSource = `
{{define "head"}}<!doctype html>
<html lang="{{lang}}"{{if .App}} data-app{{end}}><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="/favicon.svg" type="image/svg+xml"><link rel="alternate icon" href="/favicon.ico" sizes="16x16 32x32 48x48"><link rel="apple-touch-icon" href="/apple-touch-icon.png"><script src="/assets/i18n.js"></script><script src="/assets/csrf.js"></script><script src="/assets/theme.js"></script><title>{{t .Title}} · {{shortName}}</title><style>{{css}}</style></head><body><div class="shell">{{end}}

{{define "foot"}}
<footer class="colophon">
<p class="colophon__notice" data-disclaimer>{{t "Projeto independente; não é uma ferramenta do Telegram nem tem endosso dele. Telegram é marca dos seus donos."}} {{t "Secret Chats ficam de fora: eles só existem nos aparelhos que os criaram."}}</p>
<p class="colophon__line">
<a class="colophon__link" href="{{repositoryURL}}" rel="noopener noreferrer" target="_blank">{{template "githubmark"}}<span>{{t "Código-fonte"}}</span></a>
<span class="colophon__sep">&middot;</span>
<span class="colophon__version" title="{{t "Versão em execução"}}">v{{version}}</span>
<span class="colophon__sep">&middot;</span>
<a class="colophon__link" href="{{upstreamURL}}" rel="noopener noreferrer" target="_blank" data-credits>{{t "Baseado no WhatsApp MCP Local, de Bruno Orlandi (licença MIT)"}}</a>
</p>
</footer>
{{if .App}}{{template "updatebanner" .}}{{end}}
<script src="/assets/app.js" defer></script><script src="/assets/cache.js" defer></script></div></body></html>{{end}}

{{/* A new version, on every page of the app, in the bottom left corner. */}}
{{define "updatebanner"}}<aside class="update-banner" data-update-banner aria-live="polite"{{if not (eq .Update.State "available" "downloading" "ready" "manual")}} hidden{{end}}>
<button class="update-banner__close" type="button" aria-label="{{t "Agora não"}}" data-update-banner-close>&times;</button>
<p class="update-banner__title">{{t "Nova versão disponível"}}</p>
<p class="update-banner__text" data-update-banner-text>{{tf "O Telegram MCP %s está disponível. O aplicativo será reiniciado após a instalação." .Update.Latest}}</p>
<div class="actions"><button class="btn btn--small" type="button" data-update-banner-install>{{t "Atualizar agora"}}</button><a class="btn btn--ghost btn--small" data-update-banner-page href="{{.Update.Page}}" target="_blank" rel="noopener" hidden>{{t "Baixar a versão nova"}}</a></div>
</aside>{{end}}

{{define "githubmark"}}<svg class="colophon__icon" viewBox="0 0 16 16" width="15" height="15" aria-hidden="true" focusable="false"><path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8a8 8 0 0 0 5.47 7.59c.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.4 7.4 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"/></svg>{{end}}

{{/* The way into Configurações, in the top right corner of every page. */}}
{{define "settingslink"}}<a class="gear" href="/configuracoes" aria-label="{{t "Configurações"}}" title="{{t "Configurações"}}"{{if eq .Active "configuracoes"}} aria-current="page"{{end}}>{{icon "gear"}}</a>{{end}}

{{define "brandmark"}}<span class="brand__mark">{{logo}}</span><span class="brand__name" title="{{t productName}}">{{shortName}}</span>{{end}}

{{define "nav"}}
<header class="masthead"><a class="brand" href="/">{{template "brandmark"}}</a>
<div class="masthead__tools">{{template "settingslink" .}}</div></header>
<nav class="nav" aria-label="{{t "Seções"}}"{{if .LiveKey}} data-live="{{.LiveKey}}" data-live-tone="{{.HealthTone}}"{{if .LiveBusy}} data-live-busy{{end}}{{end}}>
<a href="/"{{if eq .Active "conectar"}} aria-current="page"{{end}}>{{icon "plug"}}<span>{{t "Conectar MCP"}}</span></a>
<a href="/conta"{{if eq .Active "conta"}} aria-current="page"{{end}}>{{icon "chat"}}<span>{{t "Conta"}}</span></a>
<a href="/status" data-nav-status{{if eq .Active "status"}} aria-current="page"{{end}}>{{icon "activity"}}{{if and (ne .HealthTone "ok") (ne .HealthTone "")}}<span class="sr-only">{{t "Atenção:"}} </span>{{end}}<span class="nav__label">{{t "Status"}}</span>{{if and (ne .HealthTone "ok") (ne .HealthTone "")}}<span class="nav__alert{{if eq .HealthTone "warn"}} nav__alert--warn{{end}}" aria-hidden="true">!</span>{{end}}</a>
<a href="/funcoes"{{if eq .Active "funcoes"}} aria-current="page"{{end}}>{{icon "list"}}<span>{{t "Funções"}}</span></a>
<a href="/receitas"{{if eq .Active "receitas"}} aria-current="page"{{end}}>{{icon "book"}}<span>{{t "Receitas"}}</span></a>
<a href="/ajuda"{{if eq .Active "ajuda"}} aria-current="page"{{end}}>{{icon "help"}}<span>{{t "Ajuda"}}</span></a>
</nav>
{{template "portbanner" .}}
{{with .Error}}<p class="alert" role="alert">{{.}}</p>{{end}}
{{end}}

{{/* The MCP's port taken by another program: Telegram keeps running, and the
way out is one click away in the app, or a variable on the command line. */}}
{{define "portbanner"}}{{with .MCPProblem}}<div class="alert" role="alert">
<p style="margin:0"><strong>{{t "As ferramentas de IA não alcançam o Telegram agora."}}</strong> {{if .OtherGateway}}{{tf "Outra cópia do Telegram MCP, provavelmente a versão de linha de comando, já usa a porta %d." .Port}}{{else}}{{tf "A porta %d já está em uso por outro programa." .Port}}{{end}} {{t "O Telegram continua conectado e recebendo; só o MCP espera uma porta livre."}}</p>
{{if not $.App}}<p class="muted" style="margin:6px 0 0">{{t "Na linha de comando, escolha outra porta com a variável <code>TELEGRAM_MCP_PORT</code> e reinicie o serviço."}} <a href="/configuracoes#porta">{{t "Como trocar"}}</a></p>{{else if .Suggest}}<form method="post" action="/configuracoes/porta" class="actions" style="margin-top:10px"><input type="hidden" name="port" value="{{.Suggest}}"><button class="btn btn--small" type="submit">{{tf "Usar a porta %d" .Suggest}}</button><a class="btn btn--quiet btn--small" href="/configuracoes#porta">{{t "Escolher outra"}}</a></form>{{end}}
</div>{{end}}{{end}}

{{/* The connection's state; one that passes on its own spins, and the page
refreshes when it has passed. */}}
{{define "syncpill"}}<span class="pill pill--{{.SyncTone}}{{if .SyncBusy}} pill--busy{{end}}" data-sync>{{if .SyncBusy}}<span class="spinner" aria-hidden="true"></span>{{end}}{{t .SyncLabel}}</span>{{end}}

{{define "autostartcheck"}}{{if .App}}<div class="wizard__escape"><label class="check check--inline"><input type="checkbox" data-setting="autostart"{{if .Settings.Autostart}} checked{{end}}><span>{{t "Abrir o Telegram MCP quando o computador ligar"}}</span></label></div>
<p class="busy" data-setting-note role="status" hidden></p>{{end}}{{end}}

{{/* Connecting an AI tool is a flow of its own: the tool first, then the
steps for that one, which end when it connects. No tabs on the way. */}}
{{/* The language, on the first screen, before anything else is read: one
button per language, each in its own language, pressed for the one shown. */}}
{{/* A language's name in the panel's language, over its button on hover and
focus. Hidden from screen readers, which read the name in itself, with lang. */}}
{{define "hint"}}<span class="hint__body" aria-hidden="true" lang="{{lang}}">{{languageHint .}}</span>{{end}}
{{define "langpick"}}<div class="langpick" role="group" aria-label="{{t "Idioma"}}">
{{range languages}}<button class="langpick__btn hint" type="button" lang="{{.Code}}" data-language-pick="{{.Code}}" aria-pressed="{{if eq lang .Code}}true{{else}}false{{end}}">{{flag .Code}}<span>{{.Name}}</span>{{template "hint" .Code}}</button>
{{end}}</div>
<p class="busy langpick__note" data-language-note role="status" hidden></p>{{end}}

{{define "flowhead"}}
<header class="masthead"><a class="brand" href="/">{{template "brandmark"}}</a>
<div class="masthead__tools">{{template "settingslink" .}}</div></header>
<div class="flow">
{{template "portbanner" .}}
{{with .Error}}<p class="alert" role="alert">{{.}}</p>{{end}}
{{end}}

{{define "toolpicks"}}<ul class="toolpicks">
{{range .}}<li><a class="toolpick" href="/conectar/{{.Key}}"><span class="toolpick__icon">{{toolmark .Mark}}</span>
<span class="toolpick__text"><span class="toolpick__name">{{t .Name}}</span><span class="toolpick__hint">{{t .Hint}}</span></span>
{{if eq .State "live"}}<span class="pill pill--ok">{{t "Conectado"}}</span>{{else if eq .State "configured"}}<span class="pill pill--warn">{{t "Aguardando"}}</span>{{end}}
<span class="toolpick__go" aria-hidden="true">{{icon "chevron"}}</span></a></li>
{{end}}</ul>{{end}}

{{define "conectarescolha"}}{{template "head" .}}{{template "flowhead" .}}
<a class="flow__back" href="{{if .InSetup}}/instalacao{{else}}/{{end}}">{{icon "back"}}{{t "Voltar"}}</a>
<h1>{{t "Conectar ferramenta de IA ao MCP"}}</h1>
<p class="lead">{{t "Onde você vai usar o seu Telegram?"}}</p>
{{template "toolpicks" .Tools}}
<p class="muted flow__note">{{t "Não funciona no Claude pelo navegador ou pelo celular, nem no ChatGPT na web: só em programas instalados neste computador."}}</p>
</div>
{{template "foot" .}}{{end}}

{{define "conectarferramenta"}}{{template "head" .}}{{template "flowhead" .}}
{{if not .Done}}<a class="flow__back" href="/conectar">{{icon "back"}}{{t "Escolher outra ferramenta"}}</a>{{end}}
<div class="flow__title"><span class="toolpick__icon">{{toolmark .Tool.Mark}}</span><h1>{{t .Tool.Title}}</h1></div>

{{if .Done}}
<section class="card card--accent">
<div class="card__head"><h2>{{t .Tool.Connected}}</h2><span class="pill pill--ok">{{t "Conectado"}}</span></div>
<div class="card__body stack">
<p class="muted">{{t "Já pode pedir coisas do seu Telegram para a sua IA. Para testar, mande isto no chat:"}}</p>
<div class="snippet"><pre class="plain" data-copy><code>{{t .Verification}}</code></pre></div>
{{if .InSetup}}<form method="post" action="/instalacao/avancar"><input type="hidden" name="to" value="done"><div class="actions"><button class="btn" type="submit">{{t "Ir para o painel"}}</button></div></form>
{{else}}<div class="actions"><a class="btn" href="/">{{t "Voltar ao painel"}}</a></div>{{end}}
</div></section>
{{else}}
{{if .Live}}<p class="alert alert--ok">{{tf "%s. Siga os passos abaixo só se quiser configurar de novo." (t .Tool.Connected)}}</p>{{end}}
<section class="card">
<div class="card__body">
<ol class="flowsteps">
{{if eq .Tool.Key "claude-desktop"}}
<li class="flowstep"><h2 class="flowstep__title">{{t "Adicione o Telegram ao Claude Desktop"}}</h2>
{{if not .Setup.Desktop.Found}}<p class="note">{{t "O Claude Desktop não parece estar instalado neste computador."}} <a href="https://claude.ai/download" target="_blank" rel="noopener">{{t "Baixar o Claude Desktop"}}</a></p>{{end}}
<div class="actions"><button class="btn" type="button" data-add-client="claude-desktop">{{if .Setup.Desktop.Configured}}{{t "Configurar de novo"}}{{else}}{{t "Adicionar ao Claude Desktop"}}{{end}}</button></div>
<p class="busy" data-client-note="claude-desktop" role="status" hidden></p>
<p class="muted" data-backup-note>{{t "Antes de mudar o arquivo de configuração do Claude Desktop, o painel guarda uma cópia dele ao lado, com a data no nome (.bak-…)."}}</p>
<details class="resend"><summary>{{t "Prefiro fazer à mão"}}</summary>
<p class="muted">{{tf "No Claude Desktop, vá em <strong>Configurações → Desenvolvedor → Editar configuração</strong>, cole o texto abaixo e reinicie o app. Se já houver outros servidores no arquivo, acrescente só o trecho <code>\"%s\"</code> dentro de <code>mcpServers</code>, sem apagar o resto." serverName}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{.Setup.DesktopPath}}</span></div>
<pre data-copy><code>{{.Setup.JSON}}</code></pre></div>
</details></li>
<li class="flowstep"><h2 class="flowstep__title">{{t "Feche o Claude Desktop e abra de novo"}}</h2>
<p class="muted">{{t "Só assim ele carrega o Telegram."}}</p></li>
{{else if eq .Tool.Key "claude-code"}}
<li class="flowstep"><h2 class="flowstep__title">{{t "Adicione o Telegram ao Claude Code"}}</h2>
<div class="actions"><button class="btn" type="button" data-add-client="claude-code"{{if not .Setup.Code.Found}} disabled title="{{t "O comando claude não foi encontrado neste computador"}}"{{end}}>{{if .Setup.Code.Configured}}{{t "Configurar de novo"}}{{else}}{{t "Adicionar ao Claude Code"}}{{end}}</button></div>
<p class="busy" data-client-note="claude-code" role="status" hidden></p>
{{if not .Setup.Code.Found}}<p class="muted" data-discovery-missing="claude-code" hidden>{{t "O comando <code>claude</code> não foi encontrado. Instale o Claude Code ou rode o comando abaixo onde ele estiver."}}</p>{{else}}<p class="muted" data-backup-note>{{t "Antes de pedir ao Claude Code para mudar a configuração, o painel guarda uma cópia do arquivo dele (~/.claude.json) ao lado, com a data no nome."}}</p>{{end}}
<details class="resend"{{if not .Setup.Code.Found}} open{{end}}><summary>{{t "Prefiro rodar o comando"}}</summary>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Comando"}}</span></div>
<pre data-copy><code>{{.Setup.Command}}</code></pre></div>
</details></li>
<li class="flowstep"><h2 class="flowstep__title">{{t "Abra uma sessão nova do Claude Code"}}</h2>
<p class="muted">{{t "Uma sessão que já estava aberta não vê o Telegram."}}</p></li>
{{else if eq .Tool.Key "codex"}}
<li class="flowstep"><h2 class="flowstep__title">{{t "Adicione o Telegram ao Codex"}}</h2>
<div class="actions"><button class="btn" type="button" data-add-client="codex"{{if not .Setup.Codex.Found}} disabled title="{{t "O comando codex não foi encontrado neste computador"}}"{{end}}>{{if .Setup.Codex.Configured}}{{t "Configurar de novo"}}{{else}}{{t "Adicionar ao Codex"}}{{end}}</button></div>
<p class="busy" data-client-note="codex" role="status" hidden></p>
{{if not .Setup.Codex.Found}}<p class="muted" data-discovery-missing="codex" hidden>{{t "O comando <code>codex</code> não foi encontrado. Configure à mão, logo abaixo."}}</p>{{end}}
<details class="resend"{{if not .Setup.Codex.Found}} open{{end}}><summary>{{t "Prefiro fazer à mão"}}</summary>
<p class="muted">{{t "Acrescente este trecho ao arquivo de configuração do Codex, que vale para o app do ChatGPT, o app do Codex, o terminal e o editor de código."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{.Setup.CodexPath}}</span></div>
<pre data-copy><code>{{.Setup.CodexTOML}}</code></pre></div>
<p class="muted">{{t "Ou rode este comando:"}}</p>
<div class="snippet"><pre data-copy><code>{{.Setup.CodexCommand}}</code></pre></div>
</details></li>
<li class="flowstep"><h2 class="flowstep__title">{{t "Abra uma conversa nova no Codex"}}</h2>
<p class="muted">{{t "Pode ser no app do ChatGPT, no app do Codex, no terminal ou no editor. Se o app estiver aberto, feche e abra de novo."}}</p></li>
{{else if eq .Tool.Key "cursor"}}
<li class="flowstep"><h2 class="flowstep__title">{{t "Adicione o Telegram ao Cursor"}}</h2>
{{if not .Setup.Cursor.Found}}<p class="note">{{t "O Cursor não parece estar instalado neste computador."}} <a href="https://cursor.com/download" target="_blank" rel="noopener">{{t "Baixar o Cursor"}}</a></p>{{end}}
<div class="actions"><button class="btn" type="button" data-add-client="cursor">{{if .Setup.Cursor.Configured}}{{t "Configurar de novo"}}{{else}}{{t "Adicionar ao Cursor"}}{{end}}</button></div>
<p class="busy" data-client-note="cursor" role="status" hidden></p>
<details class="resend"><summary>{{t "Prefiro fazer à mão"}}</summary>
<p class="muted">{{tf "Abra o arquivo abaixo, que o Cursor também abre pelas configurações de MCP dele, e cole o texto. Se já houver outros servidores no arquivo, acrescente só o trecho <code>\"%s\"</code> dentro de <code>mcpServers</code>, sem apagar o resto." serverName}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{.Setup.CursorPath}}</span></div>
<pre data-copy><code>{{.Setup.CursorJSON}}</code></pre></div>
</details></li>
<li class="flowstep"><h2 class="flowstep__title">{{t "Feche o Cursor e abra de novo"}}</h2>
<p class="muted">{{t "Depois, peça ao agente do Cursor o que quiser do seu Telegram."}}</p></li>
{{else if eq .Tool.Key "hermes"}}
<li class="flowstep"><h2 class="flowstep__title">{{t "Adicione o Telegram ao Hermes Agent"}}</h2>
<p class="muted">{{tf "Acrescente este trecho em <code>mcp_servers</code> no arquivo de configuração do Hermes. Se já houver outros servidores, acrescente só o trecho <code>%s</code>, sem apagar o resto." serverName}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{.Setup.HermesPath}}</span></div>
<pre data-copy data-hermes-yaml><code>{{.Setup.HermesYAML}}</code></pre></div>
{{with .Setup.HermesEnv}}<p class="muted">{{t "O token fica fora do arquivo de configuração: ponha esta linha no arquivo <code>.env</code> do Hermes, na mesma pasta. Não cole o token em conversa nenhuma."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">.env</span></div>
<pre data-copy><code>{{.}}</code></pre></div>{{end}}
<details class="resend"><summary>{{t "Prefiro pedir ao próprio Hermes"}}</summary>
<p class="muted">{{t "Copie o texto abaixo e mande no chat do Hermes: ele mesmo se configura e conta o que achou."}}</p>
<div class="snippet"><pre class="plain" data-copy><code>{{.Setup.AgentPrompt}}</code></pre></div>
</details></li>
<li class="flowstep"><h2 class="flowstep__title">{{t "Recarregue o Hermes"}}</h2>
<p class="muted">{{t "Rode /reload-mcp numa conversa do Hermes, ou abra uma sessão nova."}}</p></li>
{{else}}
<li class="flowstep"><h2 class="flowstep__title">{{t "Peça para a sua ferramenta se configurar"}}</h2>
<p class="muted">{{t "Serve para o Hermes, o Windsurf e qualquer outra que aceite MCP. Copie o texto abaixo e mande no chat dela."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Copie e mande para a sua ferramenta"}}</span></div>
<pre class="plain" data-copy><code>{{.Setup.AgentPrompt}}</code></pre></div></li>
<li class="flowstep"><h2 class="flowstep__title">{{t "Reinicie a ferramenta"}}</h2>
<p class="muted">{{t "Quando ela terminar a configuração, feche e abra de novo, ou comece uma conversa nova."}}</p></li>
{{end}}
<li class="flowstep"><h2 class="flowstep__title">{{t "Espere a conexão"}}</h2><p class="busy" role="status" data-wait-tool="{{.Tool.Client}}" data-wait-known="{{.Known}}" data-wait-since="{{.Since}}"><span class="spinner" aria-hidden="true"></span>{{tf "Esta tela avisa quando %s se conectar." (t .Tool.Who)}}</p></li>
</ol>
</div></section>
{{end}}
</div>
{{template "foot" .}}{{end}}

{{define "conectar"}}{{template "head" .}}{{template "nav" .}}
<h1>{{t "Seu Telegram nas suas ferramentas de IA"}}</h1>
<p class="lead">{{t "Aqui você vê se está tudo funcionando e liga o seu Telegram a uma ferramenta de inteligência artificial que aceite MCP, como o Claude, o Codex, o Hermes ou a que você usar."}}</p>

<section class="overview" data-wait-client="{{if .LiveCount}}false{{else}}true{{end}}">
<div class="overview__item overview__item--{{if eq .SyncTone "ok"}}ok{{else}}wait{{end}}" data-sync>
<span class="overview__icon" aria-hidden="true">{{if .SyncBusy}}<span class="spinner"></span>{{else if eq .SyncTone "ok"}}&#10003;{{else}}!{{end}}</span>
<div class="overview__body">
<p class="overview__title">{{if eq .SyncTone "ok"}}{{t "Telegram conectado"}}{{else}}{{t .SyncLabel}}{{end}}</p>
<p class="overview__detail">{{with .Account.Username}}<strong class="overview__phone">@{{.}}</strong> {{end}}{{if .Phone}}<strong class="overview__phone">{{.Phone}}</strong>{{end}}{{.Name}}</p>
</div></div>

{{if .LiveCount}}
<div class="overview__item overview__item--ok">
<span class="overview__icon" aria-hidden="true">&#10003;</span>
<div class="overview__body">
<p class="overview__title">{{if eq .LiveCount 1}}{{t "1 ferramenta de IA conectada ao MCP"}}{{else}}{{tf "%d ferramentas de IA conectadas ao MCP" .LiveCount}}{{end}}</p>
<p class="overview__detail">{{tf "Já pode pedir coisas do seu Telegram para a sua IA. Última vez em uso: %s." (relativeSince .LastUse)}}</p>
</div></div>
{{else if .Connections}}
<div class="overview__item overview__item--wait">
<span class="overview__icon" aria-hidden="true">&hellip;</span>
<div class="overview__body">
<p class="overview__title">{{t "Esperando a sua ferramenta de IA"}}</p>
<p class="overview__detail">{{t "A configuração já está feita. Falta reiniciar a ferramenta. Esta tela avisa sozinha quando ela aparecer."}}</p>
</div></div>
{{else}}
<div class="overview__item">
<span class="overview__icon" aria-hidden="true">+</span>
<div class="overview__body">
<p class="overview__title">{{t "Nenhuma ferramenta de IA conectada"}}</p>
<p class="overview__detail">{{t "Falta um passo: ligar o seu assistente de IA a esta conta do Telegram."}}</p>
</div></div>
{{end}}
</section>

<div class="hero"><a class="btn btn--big" href="/conectar">{{t "Conectar ferramenta de IA ao MCP"}}</a></div>

<section class="card">
<div class="card__head"><h2>{{t "Suas conexões"}}</h2>{{if .Connections}}<a class="btn btn--ghost btn--small" href="/conectar">{{t "Nova conexão"}}</a>{{end}}</div>
<div class="card__body">
{{if .Connections}}
<ul class="rows">
{{range $i, $c := .Connections}}<li class="row{{if not .Live}} row--waiting{{end}}">
{{if .Mark}}<span class="tool-mark tool-mark--logo" aria-hidden="true">{{toolmark .Mark}}</span>{{else}}<span class="tool-mark" aria-hidden="true">{{initial .Tool}}</span>{{end}}
<span class="row__main"><span class="row__title">{{.Tool}}</span>
<span class="row__meta">{{if .Live}}{{tf "Funcionando · usada %s" (relativeSince .LastUsed)}}{{else if .Configured}}{{t "Configurada · ainda não se conectou; reinicie a ferramenta"}}{{else}}{{tf "Usada %s" (relativeSince .LastUsed)}}{{end}}{{with .Version}} <span class="tip tip--quiet" tabindex="0" aria-describedby="tip-versao-{{$i}}"><span aria-hidden="true">?</span><span class="tip__body" role="tooltip" id="tip-versao-{{$i}}">{{$c.Tool}} {{.}}</span></span>{{end}}</span></span>
{{if .Live}}<span class="pill pill--ok">{{t "Conectada"}}</span>{{else}}<span class="pill pill--warn">{{t "Aguardando"}}</span>{{end}}
{{if not .Mark}}<a class="btn btn--quiet btn--small" href="#renomear-{{$i}}">{{t "Renomear"}}</a>{{end}}
<a class="btn btn--danger btn--small" href="#desconectar-{{$i}}">{{t "Desconectar"}}</a>
</li>{{end}}
</ul>
<p class="muted">{{t "Todas usam a mesma sessão do Telegram neste computador. Desconectar uma não mexe nas outras."}}</p>
{{else}}
<div class="empty"><p class="empty__title">{{t "Nenhuma ferramenta de IA conectada ainda"}}</p>
<p class="muted">{{t "Clique no botão acima, escolha onde você vai usar e siga o passo a passo. Leva menos de um minuto."}}</p></div>
{{end}}
</div></section>

{{/* The first days only: after that the examples live in Ajuda. */}}
{{if and .LiveCount .Newcomer}}
<section class="card">
<div class="card__head"><h2>{{t "Experimente pedir"}}</h2></div>
<div class="card__body">
<p class="muted">{{t "Escreva isso no chat da sua ferramenta de IA. Ler e procurar é seguro: mandar mensagem só acontece quando você pede."}}</p>
<ul class="prompts">
{{range .Prompts}}<li class="prompt"><div class="snippet"><pre data-copy><code>{{t .}}</code></pre></div></li>{{end}}
</ul>
<p class="muted" style="margin-bottom:0">{{t "Mais exemplos e respostas para dúvidas na <a href='/ajuda'>Ajuda</a>."}}</p>
</div></section>
{{end}}

{{range $i, $c := .Connections}}
{{if not .Mark}}<div class="overlay" id="renomear-{{$i}}" role="dialog" aria-modal="true" aria-labelledby="renomear-{{$i}}-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="renomear-{{$i}}-titulo">{{t "Renomear esta conexão"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<form method="post" action="/conexoes/renomear"><input type="hidden" name="client" value="{{.Key}}">
<label class="field" for="nome-{{$i}}"><span class="field__label">{{t "Nome"}}</span><span class="field__hint">{{t "Como esta ferramenta aparece aqui. Deixe em branco para voltar ao nome original."}}</span></label>
<input id="nome-{{$i}}" type="text" name="name" value="{{.Tool}}" maxlength="40" autocomplete="off" spellcheck="false">
<div class="actions actions--end" style="margin-top:14px"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn" type="submit">{{t "Salvar"}}</button></div>
</form>
</div></div></div>{{end}}
<div class="overlay" id="desconectar-{{$i}}" role="dialog" aria-modal="true" aria-labelledby="desconectar-{{$i}}-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="desconectar-{{$i}}-titulo">{{t "Desconectar esta ferramenta?"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<div class="target"><span class="target__name">{{.Tool}}</span></div>
<p>{{if .Configured}}{{if eq .Key "claude-desktop"}}{{tf "A configuração do Telegram é removida do <strong>%s</strong> (com uma cópia do arquivo guardada ao lado). Ele perde o acesso quando for reiniciado." .Tool}}{{else}}{{tf "A configuração do Telegram é removida do <strong>%s</strong>. Ele perde o acesso quando for reiniciado." .Tool}}{{end}}{{else}}{{tf "O <strong>%s</strong> sai desta lista. Se ele ainda tiver a configuração, volta a aparecer quando usar o Telegram de novo: remova-a na própria ferramenta." .Tool}}{{end}}</p>
<p class="muted">{{t "As suas outras conexões continuam funcionando normalmente."}}</p>
<form method="post" action="/conexoes/remover"><input type="hidden" name="client" value="{{.Key}}">
<div class="actions actions--end"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn btn--danger" type="submit">{{t "Desconectar"}}</button></div>
</form>
</div></div></div>
{{end}}
{{template "foot" .}}{{end}}

{{/* The chat preview of upstream's account page, on Telegram: the latest
chats kept here, drawn like the app's list, and the last messages of the one
selected. On top of upstream: filters by name, unread and type, more chats
and older messages page by page from the local store, and a search inside
the chat. Read-only: no send box, and reading here marks nothing as read. */}}
{{define "chatpreview"}}
<section class="card" data-chats>
<div class="card__head"><h2>{{t "Conversas recentes"}}</h2><span class="pill pill--off pill--plain" data-chats-count>…</span></div>
<div class="card__body">
<p class="muted">{{t "As conversas guardadas neste computador, das mais recentes para as mais antigas. Escolha uma para ver as mensagens dela e compare com o Telegram."}}</p>
<form class="chatfilters" role="search" data-chat-filters>
<label class="sr-only" for="chat-q">{{t "Procurar conversa pelo nome"}}</label>
<input id="chat-q" type="search" name="q" maxlength="100" autocomplete="off" placeholder="{{t "Nome da conversa"}}" data-chat-q>
<label class="check check--inline"><input type="checkbox" name="unread" data-chat-unread><span>{{t "Só não lidas"}}</span></label>
<label class="sr-only" for="chat-type">{{t "Tipo de conversa"}}</label>
<select id="chat-type" name="type" data-chat-type><option value="">{{t "Todos os tipos"}}</option><option value="user">{{t "Pessoas"}}</option><option value="chat">{{t "Grupos"}}</option><option value="channel">{{t "Canais e supergrupos"}}</option></select>
</form>
<p class="busy" role="status" aria-live="polite" data-chats-status hidden></p>
<div class="preview-grid">
<div class="preview-list">
<ul class="chats" data-chat-list aria-busy="true"><li><div class="skeleton"></div></li><li><div class="skeleton"></div></li><li><div class="skeleton"></div></li></ul>
<div class="actions" style="margin-top:12px"><button class="btn btn--ghost btn--small" type="button" data-chats-more hidden>{{t "Mostrar mais conversas"}}</button></div>
</div>
<div class="pane">
<div class="pane__head"><span class="avatar" data-thread-avatar></span><div class="pane__who"><strong data-thread-title>…</strong><span class="pane__meta" data-thread-meta></span></div></div>
<form class="chatsearch" role="search" data-thread-search>
<label class="sr-only" for="thread-q">{{t "Procurar nesta conversa"}}</label>
<input id="thread-q" type="search" name="q" maxlength="200" autocomplete="off" placeholder="{{t "Procurar nesta conversa"}}" data-thread-q>
<button class="btn btn--small" type="submit">{{t "Procurar"}}</button>
<button class="btn btn--quiet btn--small" type="button" data-thread-clear hidden>{{t "Limpar a busca"}}</button>
</form>
<div class="actions" style="margin-bottom:8px"><button class="btn btn--ghost btn--small" type="button" data-thread-older hidden>{{t "Mostrar mensagens mais antigas"}}</button></div>
<div class="thread" data-thread aria-busy="true"><div class="skeleton"></div></div>
<p class="busy" role="status" aria-live="polite" data-thread-status hidden></p>
<div class="actions" style="margin-top:12px"><button class="btn btn--ghost btn--small" type="button" data-history-chat>{{t "Buscar mensagens mais antigas desta conversa"}}</button></div>
<p class="busy" data-history-note role="status" hidden></p>
</div>
</div>
<p class="note" style="margin:16px 0 0">{{t "Aqui aparece só o que já está guardado neste computador, e ler por aqui não marca nada como lido. Se alguma conversa ou mensagem recente estiver faltando, peça o histórico ao Telegram: pela conversa, no botão acima, ou por todas as recentes, logo abaixo."}}</p>
</div></section>
{{end}}

{{define "historycard"}}
<section class="card">
<div class="card__head"><h2>{{t "Histórico"}}</h2>{{with .History}}{{if .FinishedAt}}<span class="pill pill--ok">{{t "Último pedido concluído"}}</span>{{else}}<span class="pill pill--warn">{{t "Buscando…"}}</span>{{end}}{{end}}</div>
<div class="card__body stack">
<p class="muted">{{t "Cada pedido pode trazer mais histórico do Telegram para este computador, e ele fica disponível para a sua ferramenta de IA consultar pelo MCP. Pedidos grandes podem esperar o limite do Telegram."}}</p>
{{with .History}}<p class="muted">{{if .FinishedAt}}{{tf "Último pedido: %s em %s." (newMessages .Added) (chatCount (len .Chats))}}{{else}}{{tf "Buscando: %d de %s, %s até agora." .Done (chatCount (len .Chats)) (newMessages .Added)}}{{end}}</p>{{end}}
<div class="actions"><button class="btn btn--ghost btn--small" type="button" data-history-all>{{t "Buscar mensagens mais antigas das conversas recentes"}}</button></div>
<p class="busy" data-history-all-note role="status" hidden></p>
</div></section>
{{end}}

{{define "conta"}}{{template "head" .}}{{template "nav" .}}
<h1>{{t "Conta"}}</h1>
<p class="lead">{{t "A conta do Telegram conectada a este computador e como ela está."}}</p>
{{with .OK}}<p class="alert alert--ok" role="status">{{.}}</p>{{end}}

<section class="card" data-account>
<div class="card__head"><h2>{{t "Conta do Telegram"}}</h2>{{template "syncpill" .}}</div>
<div class="card__body stack">
<div class="hello"><span class="avatar avatar--brand">{{initial .Name}}</span><div class="hello__who"><p class="hello__name">{{with .Name}}{{.}}{{else}}{{t "Sua conta"}}{{end}}{{with .Label}} <span class="pill pill--plain pill--label">{{.}}</span>{{end}}{{if .Account.Premium}} <span class="pill pill--plain">{{"Premium"}}</span>{{end}}</p><p class="hello__phone">{{with .Account.Username}}{{"@"}}{{.}}{{end}}{{if and .Account.Username .Phone}}{{" · "}}{{end}}{{.Phone}}</p></div>
<span class="hello__action"><a class="btn btn--quiet btn--small" href="#nome-conta">{{if .Label}}{{t "Mudar o nome"}}{{else}}{{t "Dar um nome"}}{{end}}</a> <span class="tip" tabindex="0" aria-describedby="tip-nome-conta"><span aria-hidden="true">?</span><span class="tip__body" role="tooltip" id="tip-nome-conta">{{t "Um nome curto para esta conta, como Pessoal ou Trabalho. A sua ferramenta de IA vê esse nome ao usar o Telegram."}}</span></span></span></div>
<dl class="facts">
<div class="fact"><dt>{{t "Conexão"}}</dt><dd>{{t .SyncLabel}}<span class="fact__detail">{{tf "desde %s" (moment .Sync.Since)}}</span></dd></div>
<div class="fact"><dt>{{t "Modo"}}</dt><dd data-mode="{{.Sync.Mode}}">{{if .ReadOnly}}{{t "Somente leitura"}}{{else}}{{t "Leitura e escrita"}}{{end}}</dd></div>
<div class="fact"><dt>{{t "Mensagens guardadas"}}</dt><dd data-account-messages>{{if .SnapshotReady}}{{count .Sync.History.Messages}}{{else}}…{{end}}</dd></div>
<div class="fact"><dt>{{t "Conversas"}}</dt><dd data-account-conversations>{{if .SnapshotReady}}{{count .Sync.History.Conversations}}{{else}}…{{end}}</dd></div>
</dl>
{{if .ReadOnly}}<p class="note">{{t "Modo somente leitura: a sua ferramenta de IA lê e procura, e as funções que mudariam algo no Telegram só mostram a prévia. Permitir escrita é sempre um ato seu neste computador, nunca da ferramenta de IA."}}</p>
{{else}}<p class="note">{{t "Leitura e escrita: toda ação que muda algo no Telegram mostra uma prévia antes e só acontece quando você confirma."}}</p>{{end}}
<div class="actions" data-mode-actions>{{if .ReadOnly}}<a class="btn btn--small" href="#modo-escrita">{{t "Permitir escrita…"}}</a>{{else}}<a class="btn btn--ghost btn--small" href="#modo-leitura">{{t "Voltar para somente leitura…"}}</a>{{end}}</div>
<p class="muted">{{t "Ler uma conversa por aqui não a marca como lida. Secret Chats não aparecem: eles só existem nos aparelhos que os criaram."}}</p>
</div></section>

<p class="busy" role="status" data-account-snapshot-note>{{if .SnapshotReady}}{{t "Status atualizado."}}{{else}}{{t "Carregando status local…"}}{{end}}</p>
<button class="btn btn--ghost btn--small" type="button" data-account-snapshot-retry hidden>{{t "Tentar novamente"}}</button>
{{template "chatpreview" .}}
{{template "historycard" .}}

<div class="overlay" id="modo-escrita" role="dialog" aria-modal="true" aria-labelledby="modo-escrita-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="modo-escrita-titulo">{{t "Permitir escrita no Telegram?"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<form class="stack" data-mode-form data-mode-target="read_write" data-mode-busy="{{t "Permitindo…"}}">
<p>{{t "Com a escrita permitida, a sua ferramenta de IA pode pedir para enviar mensagens e marcar conversas como lidas em seu nome. Cada ação ainda mostra uma prévia e só acontece quando você confirma."}}</p>
<p class="muted">{{t "Uma mensagem recebida nunca troca este modo: só você, aqui, pode permitir ou bloquear a escrita."}}</p>
<label class="check"><input type="checkbox" data-mode-confirm required><span>{{t "Entendo e quero permitir a escrita neste computador."}}</span></label>
<p class="alert" role="alert" data-mode-error hidden></p>
<div class="actions actions--end"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn btn--danger" type="submit" data-mode-submit disabled>{{t "Permitir escrita"}}</button></div>
</form>
</div></div></div>

<div class="overlay" id="modo-leitura" role="dialog" aria-modal="true" aria-labelledby="modo-leitura-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="modo-leitura-titulo">{{t "Voltar para somente leitura?"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<form class="stack" data-mode-form data-mode-target="read_only" data-mode-busy="{{t "Bloqueando…"}}">
<p>{{t "A sua ferramenta de IA volta a só ler e procurar; as funções que mudariam algo no Telegram passam a mostrar só a prévia."}}</p>
<p class="alert" role="alert" data-mode-error hidden></p>
<div class="actions actions--end"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn" type="submit" data-mode-submit>{{t "Voltar para somente leitura"}}</button></div>
</form>
</div></div></div>

<section class="card" id="sair">
<div class="card__head"><h2>{{t "Desconectar, sair ou apagar"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "São três coisas diferentes, cada uma com a sua confirmação."}}</p>
<ul class="rows">
{{if .Paused}}<li class="row"><span class="row__main"><span class="row__title">{{t "Reconectar"}}</span><span class="row__meta">{{t "Desconectado por você. A sessão e o histórico continuam guardados; reconecte quando quiser."}}</span></span>
<form method="post" action="/conta/reconectar" data-busy="{{t "Reconectando…"}}"><button class="btn btn--small" type="submit">{{t "Reconectar"}}</button></form></li>
{{else}}<li class="row"><span class="row__main"><span class="row__title">{{t "Desconectar"}}</span><span class="row__meta">{{t "Desconectar para de receber e de sincronizar, sem sair da conta. A sessão e as mensagens guardadas ficam neste computador."}}</span></span><a class="btn btn--ghost btn--small" href="#desconectar">{{t "Desconectar…"}}</a></li>{{end}}
<li class="row"><span class="row__main"><span class="row__title">{{t "Sair da conta"}}</span><span class="row__meta">{{t "Encerra a sessão deste computador, que sai de Configurações → Dispositivos no Telegram. As mensagens já guardadas continuam aqui."}}</span></span><a class="btn btn--danger btn--small" href="#encerrar-sessao">{{t "Sair…"}}</a></li>
<li class="row"><span class="row__main"><span class="row__title">{{t "Apagar tudo"}}</span><span class="row__meta">{{t "Sai da conta e apaga deste computador as mensagens, as transcrições, o modelo e os arquivos. Não tem volta."}}</span></span><a class="btn btn--danger btn--small" href="/configuracoes#apagar-tudo">{{t "Apagar tudo…"}}</a></li>
</ul>
</div></section>

<div class="overlay" id="desconectar" role="dialog" aria-modal="true" aria-labelledby="desconectar-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="desconectar-titulo">{{t "Desconectar do Telegram?"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<p>{{t "Este computador para de receber e de sincronizar mensagens até você reconectar. As ferramentas de IA continuam lendo o que já está guardado."}}</p>
<p class="muted">{{t "A sessão não é encerrada: para voltar, basta Reconectar, sem QR code nem código."}}</p>
<form method="post" action="/conta/desconectar" data-busy="{{t "Desconectando…"}}"><div class="actions actions--end"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn btn--danger" type="submit">{{t "Desconectar"}}</button></div></form>
</div></div></div>

<div class="overlay" id="nome-conta" role="dialog" aria-modal="true" aria-labelledby="nome-conta-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="nome-conta-titulo">{{t "Nome desta conta"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<form method="post" action="/conta/nome">
<label class="field" for="nome-conta-campo"><span class="field__label">{{t "Nome"}}</span><span class="field__hint">{{t "Por exemplo Pessoal ou Trabalho. Deixe em branco para tirar o nome."}}</span></label>
<input id="nome-conta-campo" type="text" name="label" value="{{.Label}}" maxlength="30" autocomplete="off" placeholder="{{t "Pessoal, Trabalho…"}}">
<div class="actions actions--end" style="margin-top:14px"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn" type="submit">{{t "Salvar"}}</button></div>
</form>
</div></div></div>

<div class="overlay" id="encerrar-sessao" role="dialog" aria-modal="true" aria-labelledby="encerrar-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="encerrar-titulo">{{t "Sair da conta do Telegram?"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">×</a></div>
<div class="dialog__body">
<p>{{t "A sessão deste computador é encerrada. Para voltar a usar, será preciso entrar de novo pelo QR code ou pelo número de telefone."}}</p>
<p class="muted">{{t "As mensagens já guardadas continuam neste computador."}}</p>
<form method="post" action="/conta/sair" data-busy="{{t "Saindo…"}}"><div class="actions actions--end"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn btn--danger" type="submit">{{t "Sair da conta"}}</button></div></form>
</div></div></div>
{{template "foot" .}}{{end}}

{{define "status"}}{{template "head" .}}{{template "nav" .}}
<h1>{{t "Status"}}</h1>
<p class="lead">{{t "Se o seu Telegram está conectado neste computador, quanto do histórico já chegou e o que o motor está fazendo."}}</p>

<div data-status-snapshot aria-busy="{{if .DetailsReady}}false{{else}}true{{end}}">
<p class="busy" role="status" data-snapshot-note>{{if .DetailsReady}}{{t "Status atualizado."}}{{else}}<span class="spinner" aria-hidden="true"></span>{{t "Carregando status local…"}}{{end}}</p>
<button class="btn btn--ghost btn--small" type="button" data-snapshot-retry hidden>{{t "Tentar novamente"}}</button>
<div data-status-content>{{if .DetailsReady}}{{template "statuscontent" .}}{{end}}</div>
</div>
{{template "foot" .}}{{end}}

{{define "statuscontent"}}
<section class="card" data-status-connection>
<div class="card__head"><h2>{{t "Conexão"}}</h2>{{template "syncpill" .}}</div>
<div class="card__body stack">
{{if eq .Tone "ok"}}<p class="alert alert--ok">{{t "Nenhum problema detectado."}}</p>
{{else}}<ul class="problems">{{range .Checks}}{{if or (eq .Status "warn") (eq .Status "fail")}}<li{{if eq .Status "warn"}} class="problems__warn"{{end}}><strong>{{.Title}}:</strong> {{.Text}}</li>{{end}}{{end}}</ul>{{end}}
<dl class="facts">
<div class="fact"><dt>{{t "Estado"}}</dt><dd>{{t .SyncLabel}}<span class="fact__detail">{{tf "desde %s" (moment .Sync.Since)}}</span></dd></div>
<div class="fact"><dt>{{t "Reconexões"}}</dt><dd data-restarts>{{count .Sync.Restarts}}</dd></div>
<div class="fact"><dt>{{t "Modo"}}</dt><dd data-mode="{{.Sync.Mode}}">{{if .ReadOnly}}{{t "Somente leitura"}}{{else}}{{t "Leitura e escrita"}}{{end}}</dd></div>
{{with .Name}}<div class="fact"><dt>{{t "Conta"}}</dt><dd>{{.}}</dd></div>{{end}}
</dl>
{{with .Sync.LastError}}<p class="note"><strong>{{t "Último erro:"}}</strong> {{.}}</p>{{end}}
{{with .FloodWait}}<p class="note">{{tf "O Telegram pediu uma pausa (FLOOD_WAIT) até %s. As chamadas voltam sozinhas depois disso." (moment .)}}</p>{{end}}
{{if .ReadOnly}}<p class="muted">{{t "Modo somente leitura: as funções que mudariam algo no Telegram só mostram a prévia."}}</p>{{end}}
</div></section>

<section class="card" data-status-history>
<div class="card__head"><h2>{{t "Histórico"}}</h2>{{if .Sync.History.Running}}<span class="pill pill--warn pill--busy"><span class="spinner" aria-hidden="true"></span>{{t "Buscando…"}}</span>{{end}}</div>
<div class="card__body stack">
<dl class="facts">
<div class="fact"><dt>{{t "Mensagens guardadas"}}</dt><dd data-account-messages>{{if .SnapshotReady}}{{count .Sync.History.Messages}}{{else}}…{{end}}</dd></div>
<div class="fact"><dt>{{t "Conversas"}}</dt><dd data-account-conversations>{{if .SnapshotReady}}{{count .Sync.History.Conversations}}{{else}}…{{end}}</dd></div>
<div class="fact"><dt>{{t "Última chegada"}}</dt><dd>{{relativeSince .Sync.History.LastAt}}<span class="fact__detail">{{moment .Sync.History.LastAt}}</span></dd></div>
<div class="fact"><dt>{{t "Em andamento"}}</dt><dd>{{with .Sync.History.Running}}{{peerLabel .}}{{else}}{{t "nada agora"}}{{end}}</dd></div>
</dl>
<p class="{{if or (eq .CoverageTone "ok") (eq .CoverageTone "info")}}muted{{else}}note{{end}}" data-coverage="{{.CoverageTone}}">{{if eq .CoverageTone "sync"}}<span class="spinner" aria-hidden="true"></span>{{end}}<strong>{{t "Cobertura:"}}</strong> {{.CoverageText}}</p>
{{with .Activity.Oldest}}<p class="muted" data-local-history-depth>{{tf "Histórico local guardado desde %s." (moment .)}} {{t "Esse limite não comprova histórico completo no Telegram."}}</p>{{end}}
{{if .CoverageUnavailable}}<p class="note">{{t "Cobertura local indisponível."}}</p>{{end}}
{{with .ConversationCoverage}}<ul class="rows" data-conversation-coverage>{{range .}}<li class="row"><span class="row__main"><strong>{{.Name}}</strong> <code>{{.ID}}</code><span class="row__meta">{{if .Oldest}}{{tf "desde %s" (moment .Oldest)}}{{else}}{{t "Profundidade desconhecida"}}{{end}} · {{if .Complete}}{{t "Histórico completo"}}{{else}}{{t "Histórico parcial"}}{{end}}</span></span></li>{{end}}</ul>{{end}}
</div></section>

<section class="card" data-status-updates>
<div class="card__head"><h2>{{t "Atualizações do Telegram"}}</h2></div>
<div class="card__body stack">
<dl class="facts">
<div class="fact"><dt>{{t "Última atualização"}}</dt><dd>{{relativeSince .Sync.Updates.LastUpdateAt}}<span class="fact__detail">{{moment .Sync.Updates.LastUpdateAt}}</span></dd></div>
<div class="fact"><dt>{{t "Canais acompanhados"}}</dt><dd>{{count .Sync.Updates.Channels}}</dd></div>
<div class="fact"><dt>{{t "Lacunas em recuperação"}}</dt><dd data-open-gaps>{{count .Sync.Updates.OpenGaps}}</dd></div>
<div class="fact"><dt>{{t "Lacunas sem recuperação"}}</dt><dd><span data-terminal-gaps>{{count .Sync.Updates.TerminalGaps}}</span>{{with gapCauses .Sync.Updates}}<span class="fact__detail">{{range $i, $c := .}}{{if $i}} · {{end}}<span data-gap-cause="{{$c.Cause}}">{{t $c.Label}}: {{count $c.Count}}</span>{{end}}</span>{{end}}</dd></div>
<div class="fact"><dt>{{t "Ponto de controle"}}</dt><dd>{{if .Sync.Updates.Held}}{{t "Retido, recuperando"}}{{else}}{{t "Em dia"}}{{end}}<span class="fact__detail"><code>pts {{.Sync.Updates.Pts}} · qts {{.Sync.Updates.Qts}} · seq {{.Sync.Updates.Seq}}</code></span></dd></div>
</dl>
{{if .Sync.Updates.TerminalGaps}}<p class="muted" data-terminal-note>{{t "Lacunas sem recuperação são informação: as atualizações não podem preenchê-las e elas não impedem o uso."}}</p>{{end}}
</div></section>

<section class="card">
<div class="card__head"><h2>{{t "Verificações"}}</h2></div>
<div class="card__body">
<ul class="rows">
{{range .Checks}}<li class="row" data-check="{{.Name}}" data-check-status="{{.Status}}">
<span class="row__main"><span class="row__title">{{.Title}}</span><span class="row__meta">{{.Text}}</span></span>
<span class="pill pill--{{if eq .Status "ok"}}ok{{else if eq .Status "busy"}}warn pill--busy{{else if eq .Status "warn"}}warn{{else}}off{{end}}">{{if eq .Status "ok"}}{{t "Ok"}}{{else if eq .Status "busy"}}<span class="spinner" aria-hidden="true"></span>{{t "Carregando"}}{{else if eq .Status "warn"}}{{t "Atenção"}}{{else if eq .Status "info"}}{{t "Informação"}}{{else}}{{t "Problema"}}{{end}}</span>
</li>{{end}}
</ul>
</div></section>

<section class="card">
<div class="card__head"><h2>{{t "Motor"}}</h2></div>
<div class="card__body stack">
{{if .DiagnosticsErr}}<p class="note">{{t "Não foi possível ler o diagnóstico do motor agora."}}</p>
{{else}}<dl class="facts">
<div class="fact"><dt>{{t "Biblioteca"}}</dt><dd>{{.Diagnostics.Library}}</dd></div>
<div class="fact"><dt>{{t "Servidor (DC)"}}</dt><dd>{{if .Diagnostics.DC}}{{.Diagnostics.DC}}{{else}}—{{end}}</dd></div>
<div class="fact"><dt>{{t "Sessão guardada"}}</dt><dd>{{if .Diagnostics.SessionPresent}}{{t "Sim"}}{{else}}{{t "Não"}}{{end}}</dd></div>
<div class="fact"><dt>{{t "Verificado"}}</dt><dd>{{relativeSince .Diagnostics.CheckedAt}}</dd></div>
</dl>{{end}}
{{if .Recent}}<h3 class="card__sub">{{t "Eventos recentes"}}</h3>
<ul class="rows" data-recent-events>{{range .Recent}}<li class="row"><span class="row__main"><span class="row__title">{{eventLabel .}}</span><span class="row__meta">{{moment .At}}</span></span></li>{{end}}</ul>{{end}}
</div></section>

{{end}}

{{define "instalacao"}}{{template "head" .}}
<header class="masthead"><a class="brand" href="/instalacao">{{template "brandmark"}}</a>
<div class="masthead__tools">{{template "settingslink" .}}</div></header>
<div class="wizard-shell"{{if eq .Step 2}} style="max-width:620px"{{end}}>
{{template "langpick" .}}
{{template "portbanner" .}}
<ol class="wizard" aria-label="{{t "Etapas da instalação"}}" style="max-width:520px;margin-left:auto;margin-right:auto">
{{range .Steps}}<li class="wizard__step wizard__step--{{.State}}"{{if eq .State "now"}} aria-current="step"{{end}}>
<span class="wizard__n" aria-hidden="true">{{if eq .State "done"}}✓{{else}}{{.Number}}{{end}}</span><span class="wizard__label">{{t .Label}}</span></li>{{end}}
</ol>

{{if eq .Step 1}}
<section class="card" data-pairing>
<div class="card__head"><h2>{{t "Conecte o seu Telegram"}}</h2></div>
<div class="card__body">
<div data-pair-qr>
<ol class="guide">
<li>{{t "Abra o <strong>Telegram</strong> no celular."}}</li>
<li>{{t "Vá em <strong>Configurações</strong> → <strong>Dispositivos</strong>."}}</li>
<li>{{t "Toque em <strong>Conectar dispositivo</strong>."}}</li>
<li>{{t "Aponte a câmera para o código abaixo."}}</li>
</ol>
<div class="qrframe"><img class="qrcode" data-qr alt="{{t "QR code para entrar no Telegram"}}" width="260" height="260" hidden><div class="qrframe__wait" data-qr-wait><span class="progressline"><span class="spinner" aria-hidden="true"></span>{{t "Gerando o código…"}}</span></div></div>
<p class="muted" style="text-align:center">{{t "O código se renova sozinho a cada poucos segundos."}}</p>
<div class="actions" style="justify-content:center"><button class="linkbtn" type="button" data-pair-phone-toggle>{{t "Prefiro entrar com o número de telefone"}}</button></div>
<form data-pair-phone hidden>
<label class="field" for="phone"><span class="field__label">{{t "Número da sua conta do Telegram"}}</span>
<span class="field__hint">{{t "Com DDI e DDD. O Telegram manda um código, de preferência no app de outro aparelho seu."}}</span></label>
<input id="phone" type="tel" inputmode="tel" autocomplete="tel" placeholder="{{t "+55 11 91234-5678"}}" data-pair-phone-input>
<div class="actions"><button class="btn" type="submit" data-pair-phone-go>{{t "Pedir o código"}}</button></div>
</form>
</div>
<div data-pair-starting hidden>
<p class="muted"><span class="progressline"><span class="spinner" aria-hidden="true"></span>{{t "Falando com o Telegram…"}}</span></p>
</div>
<form data-pair-code hidden>
<p class="lead" data-code-phone></p>
<p class="muted" data-code-via></p>
<label class="field" for="login-code"><span class="field__label">{{t "Código"}}</span></label>
<input id="login-code" class="input--code" type="text" inputmode="numeric" autocomplete="one-time-code" spellcheck="false" maxlength="12" data-code-input>
<div class="actions"><button class="btn" type="submit" data-code-go>{{t "Continuar"}}</button><button class="btn btn--ghost btn--small" type="button" data-code-resend>{{t "Mandar o código de novo"}}</button></div>
<p class="busy" data-code-note role="status" hidden></p>
<div class="actions" style="justify-content:center"><button class="linkbtn" type="button" data-pair-back>{{t "Voltar para o QR code"}}</button></div>
</form>
<form data-pair-password hidden>
<p class="lead">{{t "Verificação em duas etapas"}}</p>
<p class="muted">{{t "A sua conta tem uma senha além do código. Digite a senha do Telegram (não é a do celular)."}}</p>
<p class="muted" data-password-hint hidden></p>
<label class="field" for="login-password"><span class="field__label">{{t "Senha"}}</span></label>
<input id="login-password" type="password" autocomplete="current-password" spellcheck="false" data-password-input>
<div class="actions"><button class="btn" type="submit" data-password-go>{{t "Entrar"}}</button></div>
<p class="busy" data-password-note role="status" hidden></p>
<p class="muted">{{t "A senha vai direto ao Telegram: não fica guardada neste computador nem aparece em nenhum registro."}}</p>
<div class="actions" style="justify-content:center"><button class="linkbtn" type="button" data-pair-back>{{t "Cancelar e voltar para o QR code"}}</button></div>
</form>
<div data-pair-sync hidden>
<p class="lead"><span class="progressline"><span class="spinner" aria-hidden="true"></span><strong>{{t "Conectado!"}}</strong></span></p>
<p class="muted">{{t "Só um instante…"}}</p>
</div>
<div data-pair-error hidden>
<p class="alert" role="alert" data-error-text></p>
<div class="actions"><button class="btn" type="button" data-pair-retry>{{t "Tentar de novo"}}</button></div>
</div>
</div></section>
<p class="muted" style="text-align:center">{{t "Tudo roda neste computador: as mensagens ficam aqui, e nada passa por servidor de terceiros além do próprio Telegram."}}</p>
<p class="muted" style="text-align:center">{{t "Secret Chats ficam de fora: eles só existem nos aparelhos que os criaram."}}</p>
{{template "autostartcheck" .}}
{{end}}

{{if eq .Step 2}}
<section class="overview" style="grid-template-columns:1fr">
<div class="overview__item overview__item--ok">
<span class="overview__icon" aria-hidden="true">&#10003;</span>
<div class="overview__body">
<p class="overview__title">{{with .Name}}{{tf "Telegram conectado: %s" .}}{{else}}{{t "Telegram conectado"}}{{end}}</p>
<p class="overview__detail"><strong class="overview__phone">{{with .Account.Username}}{{"@"}}{{.}} {{end}}{{.Phone}}</strong>{{t "Confira se é esta a conta que você queria conectar."}}</p>
{{if .Arriving}}<p class="busy" data-sync-note style="margin-top:6px"><span class="spinner" aria-hidden="true"></span><span>{{tf "O histórico recente está chegando: <span data-sync-count>%s</span> mensagens até agora. Pode seguir enquanto isso." (count .ArrivingCount)}}</span></p>{{end}}
</div></div>
</section>
{{if .LiveCount}}
<section class="card card--accent">
<div class="card__head"><h2>{{t "Tudo pronto"}}</h2><span class="pill pill--ok">{{if eq .LiveCount 1}}{{t "1 ferramenta conectada"}}{{else}}{{tf "%d ferramentas conectadas" .LiveCount}}{{end}}</span></div>
<div class="card__body">
<p class="lead">{{if eq .LiveCount 1}}{{tf "%s já está usando o seu Telegram." (liveTools .Connections)}}{{else}}{{tf "%s já estão usando o seu Telegram." (liveTools .Connections)}}{{end}}</p>
<p class="muted">{{t "Para testar, mande isto no chat:"}}</p>
<div class="snippet"><pre class="plain" data-copy><code>{{t .Verification}}</code></pre></div>
<form method="post" action="/instalacao/avancar"><input type="hidden" name="to" value="done">
<div class="actions" style="margin-top:14px"><button class="btn btn--block" type="submit">{{t "Ir para o painel"}}</button></div></form>
</div></section>
{{else}}
<section class="card" data-wait-client="true">
<div class="card__head"><h2>{{t "Conecte a sua ferramenta de IA"}}</h2></div>
<div class="card__body">
<p class="muted">{{t "Onde você vai usar o seu Telegram? Escolha e siga o passo a passo."}}</p>
{{template "toolpicks" .Tools}}
</div></section>
<form method="post" action="/instalacao/avancar"><input type="hidden" name="to" value="done">
<div class="wizard__escape"><button class="btn btn--quiet" type="submit">{{t "Pular por enquanto"}}</button></div></form>
{{end}}
{{template "autostartcheck" .}}
{{end}}
</div>
{{template "foot" .}}{{end}}

{{define "funcoes"}}{{template "head" .}}{{template "nav" .}}
<h1>{{t "O que o MCP sabe fazer"}}</h1>
<p class="lead">{{tf "As %d funções que a sua ferramenta de IA pode usar no seu Telegram." .Count}}</p>
{{if .ReadOnly}}<p class="note" data-functions-readonly>{{tf "Modo somente leitura: as %d funções marcadas com <strong>muda o Telegram</strong> só mostram a prévia do que fariam. Nenhuma delas age sem você confirmar, mesmo com a escrita permitida." .Writes}}</p>
{{else}}<p class="note">{{tf "As %d funções marcadas com <strong>muda o Telegram</strong> mostram uma prévia antes e só agem quando você confirma." .Writes}}</p>{{end}}
<p class="muted">{{t "Ler não marca nada como lido: só a função mark_chat_read, quando você pede, avisa que você leu. Secret Chats ficam de fora."}}</p>
<div class="tools">
{{range .Tools}}
<article class="tool" data-tool="{{.Name}}">
<div class="tool__head"><span class="tool__name">{{.Name}}</span>{{if .Label}}<span class="pill pill--plain tool__cat tool__cat--{{.Category}}">{{t .Label}}</span>{{end}}{{if .Changes}}<span class="pill pill--warn tool__cat" data-changes>{{t "muda o Telegram"}}</span>{{end}}</div>
<p class="tool__desc">{{.Description}}</p>
{{if .Arguments}}<ul class="tool__args">
{{range .Arguments}}<li class="tool__arg"><span class="tool__argname">{{.Name}}</span><span class="tool__type">{{.Type}}</span>{{if .Required}}<span class="tool__req">{{t "obrigatório"}}</span>{{end}}<span class="tool__argdesc">{{.Description}}{{if .Choices}} {{t "Valores:"}} {{range $i, $c := .Choices}}{{if $i}}, {{end}}{{$c}}{{end}}.{{end}}</span></li>{{end}}
</ul>{{else}}<p class="tool__args muted">{{t "Sem argumentos."}}</p>{{end}}
</article>
{{end}}
</div>
{{template "foot" .}}{{end}}

{{define "ajuda"}}{{template "head" .}}{{template "nav" .}}
<h1>{{t "Ajuda"}}</h1>
<p class="lead">{{t "Respostas para as dúvidas mais comuns e exemplos para começar."}}</p>

<section class="card" id="perguntas">
<div class="card__head"><h2 class="card__title">{{icon "help"}}{{t "Perguntas frequentes"}}</h2></div>
<div class="card__body">
<div class="faq">
<details class="faq__item"><summary>{{t "Funciona no Claude pelo navegador ou pelo celular?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Não. O Telegram MCP fica neste computador, e só programas instalados nele conseguem usá-lo, como o Claude Desktop, o Claude Code, o Codex, o Cursor e o Hermes. O Claude no navegador, o app do celular e o ChatGPT na web rodam na nuvem e não alcançam o seu computador."}}</p></div></details>
<details class="faq__item"><summary>{{t "A IA pode mandar mensagens sem eu pedir?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Não. A instalação começa em somente leitura. Mesmo com a escrita permitida, mandar, reagir, editar ou apagar uma mensagem mostra uma prévia antes e só acontece quando você confirma. Uma mensagem recebida nunca vira ordem."}}</p></div></details>
<details class="faq__item"><summary>{{t "As minhas mensagens saem deste computador?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "O Telegram MCP guarda as mensagens só aqui e não manda nada para servidores de terceiros além do próprio Telegram. Quando você pede algo à sua ferramenta de IA, ela lê as mensagens de que precisa para responder, e o que ela faz com elas segue as regras de privacidade dela."}}</p></div></details>
<details class="faq__item"><summary>{{t "O computador precisa ficar ligado?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Sim, para receber as mensagens na hora. Quando ele volta, o Telegram MCP pede ao Telegram o que chegou enquanto estava desligado; a aba <a href='/status'>Status</a> mostra se ficou alguma lacuna. O celular não precisa estar ligado."}}</p></div></details>
<details class="faq__item"><summary>{{t "Como trazer mensagens mais antigas?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Peça à sua ferramenta de IA, por exemplo: \"traga as mensagens do último mês da conversa com a Ana\". Ela usa a função sync_history, que busca o histórico direto no Telegram, respeitando os limites de velocidade dele. O que chega fica guardado neste computador."}}</p></div></details>
<details class="faq__item"><summary>{{t "A IA consegue ouvir os meus áudios?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Sim, depois de instalar a <a href='/configuracoes#transcricao'>Transcrição de áudio</a> nas Configurações. Quando você pede, o áudio vira texto aqui mesmo, sem sair do computador. O nome da conversa e as últimas mensagens ajudam a acertar nomes e termos, e cada áudio é transcrito uma vez só."}}</p></div></details>
<details class="faq__item"><summary>{{t "Posso conectar mais de uma ferramenta de IA?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Pode. Todas usam a mesma conta do Telegram deste computador, e desconectar uma não mexe nas outras. Para adicionar outra, use <a href='/conectar'>Conectar ferramenta de IA ao MCP</a>."}}</p></div></details>
<details class="faq__item"><summary>{{t "A minha ferramenta não aparece conectada. E agora?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Feche a ferramenta e abra de novo, ou comece uma conversa nova: ela só carrega o Telegram ao iniciar. Se continuar assim, refaça o passo a passo em <a href='/conectar'>Conectar ferramenta de IA ao MCP</a> e confira a aba <a href='/status'>Status</a>."}}</p></div></details>
<details class="faq__item"><summary>{{t "O que a IA consegue fazer no meu Telegram?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Ler, procurar e resumir conversas, grupos e canais, transcrever áudios, mandar mensagens, fotos, enquetes e localização, reagir, editar e apagar mensagens e organizar conversas. A lista completa está em <a href='/funcoes'>Funções</a>."}}</p></div></details>
<details class="faq__item"><summary>{{t "Desconectar, sair da conta ou apagar tudo: qual a diferença?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Desconectar só pausa: a sessão e as mensagens ficam, e você reconecta quando quiser. Sair da conta encerra a sessão deste computador no Telegram, que some de Configurações → Dispositivos; as mensagens guardadas ficam. Apagar tudo sai da conta e apaga do computador as mensagens, as transcrições e os arquivos. As três ficam na aba <a href='/conta'>Conta</a>, cada uma com a sua confirmação."}}</p></div></details>
<details class="faq__item"><summary>{{t "Onde ficam os meus dados?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Numa pasta deste computador, que aparece em <a href='/configuracoes#dados'>Configurações › Dados</a>. Lá também dá para ver como apagar tudo."}}</p></div></details>
<details class="faq__item"><summary>{{t "Este app é do Telegram?"}}{{icon "chevron"}}</summary>
<div class="faq__answer"><p>{{t "Projeto independente; não é uma ferramenta do Telegram nem tem endosso dele. Telegram é marca dos seus donos."}}</p></div></details>
</div>
</div></section>

<section class="card" id="como-usar">
<div class="card__head"><h2 class="card__title">{{icon "chat"}}{{t "Como usar"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "Escreva isso no chat da sua ferramenta de IA. Ler e procurar é seguro: mandar mensagem só acontece quando você pede."}}</p>
<ul class="prompts">
{{range .Prompts}}<li class="prompt"><div class="snippet"><pre data-copy><code>{{t .}}</code></pre></div></li>{{end}}
</ul>
<div class="note"><p style="margin:0">{{t "Quer ir além? As <a href='/receitas'>Receitas</a> trazem pedidos prontos para agendar mensagens, vigiar assuntos, resumir grupos e mais."}}</p></div>
</div></section>

<section class="card" id="documentacao">
<div class="card__head"><h2 class="card__title">{{icon "book"}}{{t "Documentação"}}</h2></div>
<div class="card__body stack">
<ul class="rows">
<li class="row"><span class="row__main"><a class="row__title" href="/funcoes">{{t "Funções"}}</a><span class="row__meta">{{t "Cada função do MCP, com os argumentos e o que muda no Telegram."}}</span></span></li>
<li class="row"><span class="row__main"><a class="row__title" href="/receitas">{{t "Receitas"}}</a><span class="row__meta">{{t "Pedidos prontos para colar na sua ferramenta de IA."}}</span></span></li>
<li class="row"><span class="row__main"><a class="row__title" href="/webhooks/documentacao">{{t "Webhooks"}}</a><span class="row__meta">{{t "O formato de cada aviso, a assinatura e exemplos de receptor."}}</span></span></li>
<li class="row"><span class="row__main"><a class="row__title" href="/status">{{t "Status"}}</a><span class="row__meta">{{t "A conexão, a cobertura do histórico e os últimos eventos do motor."}}</span></span></li>
<li class="row"><span class="row__main"><a class="row__title" href="{{repositoryURL}}" target="_blank" rel="noopener noreferrer">{{t "Código-fonte e documentação técnica"}}</a><span class="row__meta">{{t "O README, a arquitetura e as notas de segurança do projeto."}}</span></span></li>
</ul>
</div></section>

<section class="card" id="transcrever">
<div class="card__head"><h2 class="card__title">{{icon "mic"}}{{t "Transcrever áudios"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "Com a <a href='/configuracoes#transcricao'>transcrição de áudio</a> instalada, os áudios viram texto aqui mesmo, com o Whisper: de graça, e o áudio não sai do computador. Nada é transcrito sem você pedir. O nome da conversa, as pessoas e as últimas mensagens entram como contexto, e a sua ferramenta de IA confere a transcrição contra a conversa e corrige o que soou estranho."}}</p>
<p class="muted">{{t "Peça algo como a mensagem abaixo. Os áudios já transcritos vêm junto das mensagens; os outros ela transcreve na hora, confere com a conversa e guarda a versão corrigida."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Exemplo"}}</span></div>
<pre class="plain" data-copy><code>{{t "Transcreva os áudios que recebi hoje no Telegram."}}</code></pre></div>
<p class="muted" style="margin:0">{{t "Cada áudio é transcrito uma vez: pedir de novo devolve o texto guardado."}}</p>
</div></section>
{{template "foot" .}}{{end}}

{{define "receitas"}}{{template "head" .}}{{template "nav" .}}
<h1>{{t "Receitas"}}</h1>
<p class="lead">{{t "O Telegram MCP só responde pelo Telegram quando perguntado: esperar a hora, vigiar um termo e montar o relatório são trabalho da sua ferramenta de IA, escrito como instrução. Cada receita é um texto para colar, e tudo que mudaria algo no Telegram ainda mostra a prévia e espera a sua confirmação."}}</p>
<div class="recipes">
{{range .Recipes}}
<article class="recipe">
<div class="recipe__head">
<h2 class="recipe__title">{{.Title}}</h2>
<p class="recipe__summary">{{.Summary}}</p>
</div>
<div class="recipe__meta">
{{range .Uses}}<span class="recipe__tool">{{.}}</span>{{end}}
{{with .Schedule}}<span class="recipe__tool recipe__tool--when">⏱ {{.}}</span>{{end}}
</div>
<div class="recipe__prompt">
<div class="snippet"><div class="snippet__head"><span class="snippet__title">Prompt</span></div>
<pre data-copy><code>{{.Prompt}}</code></pre></div>
</div>
{{with .Caveat}}<p class="recipe__caveat"><strong>{{t "Atenção:"}}</strong> {{.}}</p>{{end}}
</article>
{{end}}
</div>
{{template "foot" .}}{{end}}

{{define "configuracoes"}}{{template "head" .}}{{template "nav" .}}
<h1>{{t "Configurações"}}</h1>
<p class="lead">{{t "Como o Telegram MCP roda neste computador."}}</p>
{{with .OK}}<p class="alert alert--ok" role="status">{{.}}</p>{{end}}
<nav class="subnav" aria-label="{{t "Nesta página"}}" data-settings-tabs>
<a href="#porta">{{t "Porta"}}</a>
<a href="#webhooks">Webhooks</a>
<a href="#transcricao">{{t "Transcrição"}}</a>
<a href="#retencao">{{t "Retenção e limpeza"}}</a>
<a href="#idioma">{{t "Idioma e tema"}}</a>
<a href="#atualizacoes">{{t "Versão"}}</a>
<a href="#sair">{{t "Desconectar e apagar"}}</a>
</nav>

<section class="card card--accent" data-reconfigure{{if not (or .PortChanged .Stale)}} hidden{{end}} data-port-changed="{{.PortChanged}}">
<div class="card__head"><h2 class="card__title">{{icon "bell"}}{{t "Avise as suas ferramentas de IA"}}</h2></div>
<div class="card__body stack">
{{if not .PortChanged}}<p class="muted">{{t "Uma ou mais ferramentas ainda usam um endereço antigo deste MCP, de outra porta. Atualize cada uma com um clique."}}</p>{{end}}
<ul class="rows">
<li class="row"><span class="row__main"><span class="row__title">{{t "Claude Desktop e Cowork"}}</span><span class="row__meta">{{t "Nada a fazer: ele encontra a porta nova sozinho. Uma conversa já aberta reconecta no próximo uso."}}</span></span><span class="pill pill--ok">{{t "Automático"}}</span></li>
<li class="row"><span class="row__main"><span class="row__title">Claude Code</span><span class="row__meta" data-discovery-detail="claude-code">{{if .Setup.Code.Configured}}{{t "Já usa o endereço novo."}}{{else if .Setup.Code.Found}}{{t "Ele guarda o endereço com a porta: atualize com um clique."}}{{else}}{{t "O comando claude não foi encontrado neste computador."}}{{end}}</span></span>
<button class="btn btn--small" type="button" data-add-client="claude-code"{{if not (and .Setup.Code.Found (not .Setup.Code.Configured))}} hidden{{end}}>{{t "Atualizar o Claude Code agora"}}</button></li>
<li class="row" data-codex-discovery{{if not (or .Setup.Codex.Configured .Setup.Codex.Stale)}} hidden{{end}}><span class="row__main"><span class="row__title">Codex</span><span class="row__meta" data-discovery-detail="codex">{{if .Setup.Codex.Configured}}{{t "Já usa o endereço novo."}}{{else}}{{t "Ele guarda o endereço com a porta: atualize com um clique."}}{{end}}</span></span>
<button class="btn btn--small" type="button" data-add-client="codex"{{if .Setup.Codex.Configured}} hidden{{end}}>{{t "Atualizar o Codex agora"}}</button></li>
{{if or .Setup.Cursor.Configured .Setup.Cursor.Stale}}<li class="row"><span class="row__main"><span class="row__title">Cursor</span><span class="row__meta">{{if .Setup.Cursor.Configured}}{{t "Já usa o endereço novo."}}{{else}}{{t "Ele guarda o endereço com a porta: atualize com um clique."}}{{end}}</span></span>
{{if not .Setup.Cursor.Configured}}<button class="btn btn--small" type="button" data-add-client="cursor">{{t "Atualizar o Cursor agora"}}</button>{{end}}</li>{{end}}
</ul>
<p class="busy" data-client-note="claude-code" role="status" hidden></p>
<p class="busy" data-client-note="codex" role="status" hidden></p>
<p class="busy" data-client-note="cursor" role="status" hidden></p>
<p class="muted">{{t "Antes de mudar a configuração de uma ferramenta, o painel guarda uma cópia do arquivo dela ao lado, com a data no nome."}}</p>
<p class="muted">{{t "O Hermes e as outras ferramentas (Windsurf e outras) guardam o endereço antigo e precisam ser configuradas de novo. Mande isto no chat delas:"}}</p>
<div class="snippet"><pre class="plain" data-copy><code>{{.Setup.AgentPrompt}}</code></pre></div>
</div></section>
<p class="busy" role="status" data-client-discovery-note>{{t "Verificando ferramentas instaladas…"}}</p>

<section class="card" id="porta">
<div class="card__head"><h2 class="card__title">{{icon "port"}}{{t "Porta do MCP"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "As ferramentas de IA falam com o Telegram MCP por este endereço, que só responde a programas instalados e rodando neste computador. Troque a porta se outro programa já usa esta, ou se você quiser uma porta específica."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Endereço"}}</span></div><pre data-copy><code>{{.Setup.Endpoint}}</code></pre></div>
{{if .App}}{{if .Settings.PortLocked}}<p class="note">{{t "A porta está definida pela variável de ambiente <code>TELEGRAM_MCP_PORT</code> e não pode ser trocada aqui."}}</p>
{{else}}<form method="post" action="/configuracoes/porta" data-busy="{{t "Trocando…"}}">
<label class="field" for="port"><span class="field__label">{{t "Porta"}}</span><span class="field__hint">{{t "Um número de 1024 a 65535. Trocar a porta desconecta as ferramentas de IA do MCP."}} <span class="tip" tabindex="0" aria-describedby="tip-porta"><span aria-hidden="true">?</span><span class="tip__body" role="tooltip" id="tip-porta">{{t "O Telegram continua conectado. As ferramentas de IA ligadas por MCP, como Claude, Codex e outras, perdem a conexão e precisam do endereço novo. Depois de salvar, o app mostra como atualizar cada uma."}}</span></span></span></label>
<div class="actions"><input id="port" class="input--short" type="number" name="port" min="1024" max="65535" value="{{.Port}}" required><button class="btn" type="submit">{{t "Salvar"}}</button></div>
</form>{{end}}
{{else}}<p class="muted" data-port-cli>{{t "Na linha de comando, a porta vem da variável de ambiente <code>TELEGRAM_MCP_PORT</code>. Para trocar, defina a porta nova, reinstale o serviço e volte a esta página: ela mostra quais ferramentas ainda usam o endereço antigo e atualiza cada uma com um clique."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Comando"}}</span></div><pre data-copy><code>TELEGRAM_MCP_PORT={{.Port}} telegram-mcp service install</code></pre></div>{{end}}
</div></section>

{{if .App}}
<section class="card">
<div class="card__head"><h2 class="card__title">{{icon "power"}}{{t "Ao ligar e ao fechar"}}</h2></div>
<div class="card__body stack">
<label class="check"><input type="checkbox" data-setting="autostart"{{if .Settings.Autostart}} checked{{end}}><span><strong>{{t "Abrir o Telegram MCP quando o computador ligar"}}</strong><span class="check__hint">{{tf "Ele abre só na %s, sem janela, e as ferramentas de IA já encontram o Telegram." tray}}</span></span></label>
{{if .Settings.CanHide}}<label class="check"><input type="checkbox" data-setting="close_to_tray"{{if .Settings.CloseToTray}} checked{{end}}><span><strong>{{tf "Fechar a janela mantém o app na %s" tray}}</strong><span class="check__hint">{{if mac}}{{t "Vale também para o ⌘Q."}} {{end}}{{t "Desmarcado, fechar a janela encerra o Telegram MCP, e as ferramentas de IA perdem o acesso até ele ser aberto de novo."}}</span></span></label>
{{else}}<p class="muted">{{t "Este sistema não mostra ícones na bandeja (no GNOME, isso pede a extensão AppIndicator). Por isso, fechar a janela só a minimiza."}}</p>{{end}}
<p class="busy" data-setting-note role="status" hidden></p>
<div class="actions"><a class="btn btn--danger btn--small" href="#encerrar-app">{{t "Encerrar o Telegram MCP"}}</a></div>
<p class="muted">{{t "Desliga o app de vez, até você abrir de novo."}}</p>
</div></section>

<div class="overlay" id="encerrar-app" role="dialog" aria-modal="true" aria-labelledby="encerrar-app-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="encerrar-app-titulo">{{t "Encerrar o Telegram MCP?"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<p>{{t "O MCP é desligado: as ferramentas de IA perdem o acesso ao Telegram, e este computador para de receber mensagens enquanto o app estiver fechado."}}</p>
<p class="muted">{{t "Para voltar, abra o Telegram MCP de novo."}}</p>
<form method="post" action="/configuracoes/encerrar" data-busy="{{t "Encerrando…"}}"><div class="actions actions--end"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn btn--danger" type="submit">{{t "Encerrar"}}</button></div></form>
</div></div></div>
{{end}}

<section class="card" id="webhooks" data-webhooks>
<div class="card__head"><h2 class="card__title">{{icon "code"}}Webhooks</h2><a class="btn btn--ghost btn--small" href="/webhooks/documentacao">{{icon "book"}}{{t "Documentação"}}</a></div>
<div class="card__body stack">
<p class="muted" data-webhooks-intro hidden>{{t "Avisam um programa seu a cada mensagem nova que chega, para ele agir sozinho: registrar numa planilha, avisar em outro lugar, abrir um chamado. São para quem usa scripts ou automações."}} <span class="tip" tabindex="0" aria-describedby="tip-webhooks"><span aria-hidden="true">?</span><span class="tip__body" role="tooltip" id="tip-webhooks">{{t "A cada evento, o Telegram MCP faz um POST com JSON no endereço, assinado com a chave do webhook. Se o endereço não responder com sucesso, ele tenta de novo algumas vezes; se continuar falhando, o webhook é desligado. O formato de cada aviso está em Documentação, no alto deste card."}}</span></span></p>
<p class="note" data-webhooks-unavailable hidden>{{t "Os webhooks não estão disponíveis nesta instalação: o motor em uso não entrega eventos novos."}}</p>
<p class="muted">{{t "<strong>A fila fica na memória.</strong> Se o Telegram MCP reiniciar, as entregas pendentes se perdem. O catch-up não reenvia fatos já gravados."}}</p>
<p class="busy" data-webhooks-loading role="status"><span class="spinner" aria-hidden="true"></span>{{t "Carregando…"}}</p>
<ul class="rows" data-webhooks-list hidden></ul>
<div class="empty" data-webhooks-empty hidden><p class="empty__title">{{t "Nenhum webhook configurado"}}</p><p class="muted" style="margin:6px 0 12px">{{t "Avisam um programa seu a cada mensagem nova, para quem usa scripts ou automações."}}</p><button class="btn btn--ghost btn--small" type="button" data-webhook-start>{{t "Configurar o primeiro webhook"}}</button></div>
<div class="secret" data-webhook-secret hidden>
<p class="secret__title">{{t "Webhook adicionado. Guarde a chave dele"}}</p>
<p class="muted" style="margin:0">{{t "Ela não aparece de novo. Com ela, o seu programa confere que cada aviso veio mesmo do Telegram MCP."}}</p>
<code class="secret__value" data-webhook-secret-value></code>
<div class="actions"><button class="btn btn--ghost btn--small" type="button" data-webhook-secret-copy>{{t "Copiar a chave"}}</button><button class="btn btn--quiet btn--small" type="button" data-webhook-secret-close>{{t "Já guardei"}}</button></div>
</div>
<form class="stack" data-webhook-form hidden>
<h3 class="card__sub">{{t "Adicionar um webhook"}}</h3>
<label class="field" for="webhook-url"><span class="field__label">{{t "Endereço"}}</span><span class="field__hint">{{t "O endereço do seu programa, começando com http:// ou https://."}}</span></label>
<input id="webhook-url" type="text" inputmode="url" autocomplete="off" spellcheck="false" placeholder="http://127.0.0.1:8080/telegram" required data-webhook-url>
<span class="field__label">{{t "Avisar quando"}}</span>
<label class="check"><input type="checkbox" value="message" checked data-webhook-event><span>{{t "Chegar uma mensagem"}}</span></label>
<label class="check"><input type="checkbox" value="edit" data-webhook-event><span>{{t "Uma mensagem for editada"}}</span></label>
<label class="check"><input type="checkbox" value="delete" data-webhook-event><span>{{t "Uma mensagem for apagada"}}</span></label>
<label class="check"><input type="checkbox" value="reaction" data-webhook-event><span>{{t "Alguém reagir a uma mensagem"}}</span></label>
<label class="check"><input type="checkbox" value="read" data-webhook-event><span>{{t "Alguém ler as suas mensagens"}}</span></label>
<label class="check"><input type="checkbox" data-webhook-own><span>{{t "Incluir as mensagens que você manda"}}<span class="check__hint">{{t "De outro aparelho ou pela sua ferramenta de IA."}}</span></span></label>
<div class="actions"><button class="btn btn--small" type="submit">{{t "Adicionar webhook"}}</button><button class="btn btn--quiet btn--small" type="button" data-webhook-cancel hidden>{{t "Cancelar"}}</button></div>
<p class="alert" role="alert" data-webhook-error hidden></p>
</form>
</div></section>

<section class="card" id="transcricao" data-asr>
<div class="card__head"><h2 class="card__title">{{icon "mic"}}{{t "Transcrição de áudio"}}</h2>{{if .Local.Ready}}<span class="pill pill--ok">{{t "Ativa"}}</span>{{else if .Local.Supported}}<span class="pill pill--off">{{t "Não instalada"}}</span>{{else}}<span class="pill pill--off">{{t "Indisponível"}}</span>{{end}}</div>
<div class="card__body stack">
{{if .Local.Ready}}
<p class="muted">{{tf "Os áudios viram texto neste computador, com o Whisper rodando %s, só quando você pede." (accel .Local.Accel)}}</p>
{{if .Total}}<p class="muted" data-asr-count>{{tf "%d áudios transcritos até agora, %d com correções." .Total .Corrected}}</p>{{end}}
{{else if .Local.Supported}}
<p class="muted">{{t "Este computador consegue transcrever os áudios sem que eles saiam daqui. A instalação baixa o programa e o modelo do Whisper, cerca de 600 MB, uma vez só."}}</p>
<div class="actions"><button class="btn" type="button" data-asr-install>{{t "Instalar a transcrição local"}}</button></div>
<p class="busy" data-asr-progress role="status"{{if ne .Local.Install.State "running"}} hidden{{end}}><span class="spinner" aria-hidden="true"></span><span data-asr-step>{{.Local.Install.Step}}</span></p>
{{if eq .Local.Install.State "error"}}<p class="alert" role="alert">{{.Local.Install.Error}}</p>{{end}}
{{else}}
<p class="muted">{{t "A transcrição ainda não está disponível para este sistema."}}</p>
{{end}}
{{if and .Local.Supported (eq .Local.Accel "cpu")}}<p class="note">{{t "Sem uma placa de vídeo que o Whisper aproveite, ele roda no processador: cada áudio leva mais ou menos o próprio tempo para ser transcrito."}}</p>{{end}}
{{if .Local.Supported}}<details class="disclose"><summary><span style="display:inline-flex;align-items:center;gap:6px">{{icon "help"}}{{t "Como usar"}}</span></summary>
<div class="stack">
<p class="muted" style="margin:0">{{t "Peça à sua ferramenta de IA algo como:"}}</p>
<div class="snippet"><pre class="plain" data-copy><code>{{t "Transcreva os áudios que recebi hoje no Telegram."}}</code></pre></div>
<p class="muted" style="margin:0">{{t "Cada áudio é transcrito uma vez: pedir de novo devolve o texto guardado. Mais detalhes na <a href='/ajuda#transcrever'>Ajuda</a>."}}</p>
</div></details>{{end}}
</div></section>

<section class="card" id="retencao" data-media>
<div class="card__head"><h2 class="card__title" id="arquivos">{{icon "folder"}}{{t "Retenção e limpeza"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "Fotos, áudios e documentos que a sua ferramenta de IA abriu ficam guardados neste computador, para não serem baixados de novo. Apagar não perde nenhuma mensagem: se precisar, o arquivo é baixado outra vez, enquanto ele ainda existir no Telegram."}}</p>
<dl class="facts">
<div class="fact"><dt>{{t "Espaço usado"}}</dt><dd data-media-bytes>…</dd></div>
<div class="fact"><dt>{{t "Arquivos"}}</dt><dd data-media-files>…</dd></div>
<div class="fact"><dt>{{t "Exportações"}}</dt><dd data-media-exports>…</dd></div>
</dl>
<p class="muted" data-media-types hidden></p>
<div class="actions"><label class="check check--inline"><input type="checkbox" data-media-retention><span>{{t "Apagar sozinho os arquivos baixados há mais de"}}</span></label><input class="input--short" type="number" min="1" max="3650" value="30" aria-label="{{t "dias"}}" data-media-days disabled><span class="muted">{{t "dias"}}</span></div>
<p class="muted" style="margin:0">{{t "As exportações de conversas ficam até você apagar."}} <span class="tip" tabindex="0" aria-describedby="tip-exportacoes"><span aria-hidden="true">?</span><span class="tip__body" role="tooltip" id="tip-exportacoes">{{t "Quando você pede à sua ferramenta de IA para exportar conversas, ela grava um arquivo com as mensagens neste computador, para analisar sem carregar tudo na conversa."}}</span></span></p>
<div class="actions"><button class="btn btn--ghost btn--small" type="button" data-media-clear hidden>{{t "Apagar os arquivos baixados"}}</button><button class="btn btn--ghost btn--small" type="button" data-media-clear-exports hidden>{{t "Apagar as exportações"}}</button></div>
<p class="busy" data-media-note role="status" hidden></p>
<p class="note" data-retention-messages>{{t "As mensagens seguem o Telegram: o que é apagado lá sai daqui também, e as mensagens com autodestruição somem deste computador quando o tempo delas acaba. Conteúdo protegido contra cópia não é baixado."}}</p>
</div></section>

<section class="card" id="idioma">
<div class="card__head"><h2 class="card__title">{{icon "globe"}}{{t "Idioma e tema"}}</h2></div>
<div class="card__body stack">
{{$chosen := .Language}}
<fieldset class="languages stack"><legend class="field__label">{{t "Idioma"}}</legend>
<label class="check"><input type="radio" name="idioma" value="" data-language{{if eq $chosen ""}} checked{{end}}><span><strong>{{t "O do sistema"}}</strong><span class="check__hint">{{t "Português, español ou English, conforme o idioma do computador."}}</span></span></label>
{{range languages}}<label class="check" lang="{{.Code}}"><input type="radio" name="idioma" value="{{.Code}}" data-language{{if eq $chosen .Code}} checked{{end}}><span class="check__flag hint">{{flag .Code}}<strong>{{.Name}}</strong>{{template "hint" .Code}}</span></label>
{{end}}</fieldset>
<p class="busy" data-language-note role="status" hidden></p>
<div id="aparencia" hidden data-theme-switch>
<fieldset class="themes"><legend class="field__label">{{t "Tema"}}</legend>
<label class="theme-pick"><input type="radio" name="tema" value="light" data-theme-choice><span class="theme-pick__preview" aria-hidden="true"><span class="mini mini--light"><i></i><i></i><i></i></span></span><span class="theme-pick__label">{{icon "sun"}}{{t "Claro"}}</span></label>
<label class="theme-pick"><input type="radio" name="tema" value="dark" data-theme-choice><span class="theme-pick__preview" aria-hidden="true"><span class="mini mini--dark"><i></i><i></i><i></i></span></span><span class="theme-pick__label">{{icon "moon"}}{{t "Escuro"}}</span></label>
<label class="theme-pick"><input type="radio" name="tema" value="system" data-theme-choice><span class="theme-pick__preview" aria-hidden="true"><span class="mini mini--light"><i></i><i></i><i></i></span><span class="mini mini--dark mini--half"><i></i><i></i><i></i></span></span><span class="theme-pick__label">{{icon "monitor"}}{{t "Sistema"}}</span></label>
</fieldset>
</div>
</div></section>

<section class="card" id="atualizacoes"{{if .App}} data-update{{end}}>
<div class="card__head"><h2 class="card__title">{{icon "refresh"}}{{t "Versão e atualizações"}}</h2></div>
<div class="card__body stack">
<div class="version"><span class="version__label">{{t "Versão instalada"}}</span>
<div class="version__row"><span class="version__number">{{if .App}}{{.Settings.Version}}{{else}}{{version}}{{end}}</span>{{if .App}}<span class="pill" data-update-badge hidden></span>{{end}}</div></div>
{{if .App}}<p class="muted version__note" data-update-text hidden></p>
<div class="actions"><button class="btn btn--small" type="button" data-update-install hidden>{{t "Instalar e reiniciar"}}</button><button class="btn btn--ghost btn--small" type="button" data-update-check>{{t "Procurar atualização"}}</button><a class="btn btn--ghost btn--small" data-update-page href="#" target="_blank" rel="noopener" hidden>{{t "Baixar a versão nova"}}</a></div>
{{else}}<p class="muted version__note" data-update-cli>{{t "Na linha de comando, a atualização é manual: baixe a versão nova no repositório do projeto e rode o instalador de novo. A sessão do Telegram e as mensagens guardadas continuam."}}</p>
<div class="actions"><a class="btn btn--ghost btn--small" href="{{repositoryURL}}" target="_blank" rel="noopener noreferrer">{{t "Ver as versões"}}</a></div>{{end}}
</div></section>

<section class="card" id="dados">
<div class="card__head"><h2 class="card__title">{{icon "folder"}}{{t "Dados"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "As mensagens, a sessão do Telegram, as transcrições, o modelo de transcrição e as mídias baixadas ficam nesta pasta. Desinstalar o app não a apaga."}}</p>
{{with .DataDir}}<div class="snippet"><pre data-copy><code>{{.}}</code></pre></div>{{end}}
{{if .App}}<div class="actions"><button class="btn btn--ghost btn--small" type="button" data-open-folder>{{t "Abrir a pasta"}}</button></div>{{end}}
</div></section>

<section class="card" id="sair">
<div class="card__head"><h2 class="card__title">{{icon "power"}}{{t "Desconectar, sair ou apagar"}}</h2></div>
<div class="card__body stack">
<ul class="rows">
<li class="row"><span class="row__main"><span class="row__title">{{t "Desconectar"}}</span><span class="row__meta">{{t "Pausa a conexão com o Telegram. A sessão e as mensagens ficam; reconecte quando quiser."}}</span></span><a class="btn btn--ghost btn--small" href="/conta#desconectar">{{t "Desconectar…"}}</a></li>
<li class="row"><span class="row__main"><span class="row__title">{{t "Sair da conta"}}</span><span class="row__meta">{{t "Encerra a sessão deste computador no Telegram. As mensagens guardadas ficam aqui."}}</span></span><a class="btn btn--ghost btn--small" href="/conta#encerrar-sessao">{{t "Sair…"}}</a></li>
<li class="row"><span class="row__main"><span class="row__title">{{t "Apagar tudo"}}</span><span class="row__meta">{{t "Sai da conta e apaga deste computador as mensagens, as transcrições, o modelo e os arquivos. Não tem volta."}}</span></span><a class="btn btn--danger btn--small" href="#apagar-tudo">{{t "Apagar tudo…"}}</a></li>
</ul>
</div></section>

<div class="overlay" id="apagar-tudo" role="dialog" aria-modal="true" aria-labelledby="apagar-tudo-titulo">
<div class="dialog">
<div class="dialog__head"><h2 id="apagar-tudo-titulo">{{t "Apagar tudo?"}}</h2><a class="dialog__close" href="#" aria-label="{{t "Fechar"}}">&times;</a></div>
<div class="dialog__body">
<p>{{t "Este computador sai da sua conta do Telegram e some de Configurações → Dispositivos. As mensagens guardadas, as transcrições, o modelo e as mídias são apagados deste computador. Não tem volta."}}</p>
<p class="muted">{{t "Nada é apagado no Telegram: as suas conversas continuam nos seus outros aparelhos."}}</p>
{{if .App}}<form method="post" action="/configuracoes/apagar" data-busy="{{t "Apagando…"}}">
<label class="field" for="confirm"><span class="field__label">{{tf "Digite <strong>%s</strong> para confirmar" (t "apagar")}}</span></label>
<input id="confirm" type="text" name="confirm" autocomplete="off" spellcheck="false" required>
<div class="actions actions--end" style="margin-top:14px"><a class="btn btn--quiet" href="#">{{t "Cancelar"}}</a><button class="btn btn--danger" type="submit">{{t "Apagar tudo"}}</button></div>
</form>
{{else}}<p class="muted" data-erase-cli>{{t "Na linha de comando, apague em três passos: saia da conta em Conta, pare o serviço e apague a pasta de dados mostrada acima."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Comando"}}</span></div><pre data-copy><code>telegram-mcp service uninstall
rm -rf {{if .DataDir}}{{.DataDir}}{{else}}~/.telegram-mcp{{end}}</code></pre></div>
<div class="actions actions--end"><a class="btn btn--quiet" href="#">{{t "Fechar"}}</a><a class="btn btn--danger" href="/conta#encerrar-sessao">{{t "Sair da conta primeiro"}}</a></div>{{end}}
</div></div></div>
<script src="/assets/settings.js" defer></script>
{{template "foot" .}}{{end}}

{{define "encerrado"}}{{template "head" .}}
<div class="wizard-shell" style="max-width:520px">
<section class="card">
<div class="card__head"><h2>{{t "Encerrando"}}</h2></div>
<div class="card__body"><p class="muted">{{t "O Telegram MCP está fechando. Para voltar a usar, abra o app de novo."}}</p></div>
</section></div></div></body></html>{{end}}

{{define "apagado"}}{{template "head" .}}
<div class="wizard-shell" style="max-width:520px">
<section class="card">
<div class="card__head"><h2>{{t "Tudo apagado"}}</h2></div>
<div class="card__body"><p class="muted">{{t "Este computador saiu da sua conta do Telegram, e os dados foram apagados. O Telegram MCP vai fechar."}}</p></div>
</section></div></div></body></html>{{end}}`
