package panel

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
)

// The webhook documentation page: what reaches a webhook, event by event.
// Its examples are marshalled from the very types the deliveries are, so the
// page cannot drift from what a script receives.

type webhookExample struct {
	ID    string
	Title string
	Note  string
	JSON  string
}

type webhookExampleGroup struct {
	ID       string
	Title    string
	Intro    string
	Examples []webhookExample
}

func exampleJSON(ev webhook.Event) string {
	body, _ := json.MarshalIndent(ev, "", "  ")
	return string(body)
}

// webhookExamples are deliveries as a script receives them, with made-up
// people and ids, in the envelope of docs/contratos/webhooks.md.
func webhookExamples() []webhookExampleGroup {
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	sent := at.Add(400 * time.Millisecond)
	const (
		maria   = "user:777000123"
		joao    = "user:777000456"
		familia = "channel:1987654321"
		me      = "user:123456789"
	)
	acct := &webhook.Identity{ID: "primary", SelfID: 123456789, Username: "voce"}
	direct := &webhook.Chat{ID: maria, Type: engine.PeerUser, Kind: engine.KindUser, Name: "Maria Silva"}
	group := &webhook.Chat{ID: familia, Type: engine.PeerChannel, Kind: engine.KindSupergroup, Name: "Família"}
	dm := func(id int, text string) *webhook.Message {
		return &webhook.Message{MessageID: id, ChatID: maria, Timestamp: at, SenderID: maria, SenderName: "Maria Silva", Text: text}
	}
	inGroup := func(id int, text string) *webhook.Message {
		return &webhook.Message{MessageID: id, ChatID: familia, Timestamp: at, SenderID: joao, SenderName: "João", Text: text}
	}
	event := func(kind, delivery string, chat *webhook.Chat, ev webhook.Event) string {
		ev.Schema, ev.Kind, ev.ID, ev.EventID = 1, kind, delivery, "e_"+delivery[2:18]
		ev.OccurredAt, ev.At, ev.Source, ev.Identity, ev.Chat = at, sent, engine.SourceUpdate, acct, chat
		return exampleJSON(ev)
	}
	msg := func(delivery string, chat *webhook.Chat, m *webhook.Message) string {
		return event("message", delivery, chat, webhook.Event{Message: m})
	}

	reply := inGroup(4571, "Pode deixar, eu levo o bolo")
	reply.ReplyTo = &webhook.Reference{MessageID: 4568, SenderID: maria, Text: "Quem leva o bolo no domingo?"}
	reply.Mentioned = true
	photo := dm(4572, "Olha a vista daqui")
	photo.Media = &webhook.Media{Type: "photo", MimeType: "image/jpeg", Bytes: 184320, Width: 1280, Height: 960}
	document := dm(4573, "")
	document.Media = &webhook.Media{Type: "document", MimeType: "application/pdf", Filename: "boleto-outubro.pdf", Bytes: 92416}
	voice := dm(4574, "")
	voice.Media = &webhook.Media{Type: "voice", MimeType: "audio/ogg", Bytes: 23552, DurationSeconds: 7.4}
	place := dm(4575, "")
	place.Location = map[string]any{"latitude": -23.561684, "longitude": -46.655981, "title": "MASP", "address": "Av. Paulista, 1578", "live_period": 0}
	poll := inGroup(4576, "")
	poll.Poll = map[string]any{"question": "Churrasco no sábado?", "options": []string{"Sim", "Não", "Talvez"}, "multiple_choice": false, "anonymous": true}
	forwarded := dm(4577, "Promoção válida até sexta")
	forwarded.Forward = &webhook.Forward{From: "channel:1122334455", FromName: "Ofertas do Bairro", Date: at.Add(-2 * time.Hour), ChannelPost: 310}
	edited := dm(4569, "Chego às 19h30, não às 19h")
	editAt := at.Add(3 * time.Minute)
	edited.EditDate = &editAt
	own := &webhook.Message{MessageID: 4578, ChatID: maria, Timestamp: at, FromMe: true, SenderID: me, Text: "Combinado!"}
	timed := dm(4579, "Senha do wi-fi: 1234")
	timed.TTLPeriod = 86400
	expires := at.Add(24 * time.Hour)
	timed.ExpiresAt = &expires
	once := dm(4580, "")
	once.Media = &webhook.Media{Type: "photo", Ephemeral: true}

	return []webhookExampleGroup{
		{ID: "message", Title: i18n.T("Mensagens (message)"), Intro: i18n.T("Cada mensagem nova, de uma conversa, de um grupo ou de um canal. Todo aviso traz account (a sua conta do Telegram, sem o telefone) e chat (a conversa)."), Examples: []webhookExample{
			{ID: "texto", Title: i18n.T("Texto numa conversa"), JSON: msg("d_9f2c41d07a3b8e6551c0a7e2b4d6f810", direct, dm(4570, "Oi! Você vem amanhã?"))},
			{ID: "grupo", Title: i18n.T("Resposta num grupo"), Note: i18n.T("chat.kind diz se é conversa, grupo, supergrupo ou canal. reply_to diz qual mensagem foi citada, e mentioned é true quando citam você."), JSON: msg("d_1b7e93c2a4f05d6822e1b8f3c5a7d921", group, reply)},
			{ID: "foto", Title: i18n.T("Foto com legenda"), Note: i18n.T("A mídia vem descrita, sem o arquivo nem link; a legenda vem em text. Para o arquivo, a sua ferramenta de IA usa download_media com o id."), JSON: msg("d_2c8f04d3b5a16e7933f2c9a4d6b8ea32", direct, photo)},
			{ID: "documento", Title: i18n.T("Documento"), JSON: msg("d_3d9015e4c6b27f8a44a3dab5e7c9fb43", direct, document)},
			{ID: "audio", Title: i18n.T("Mensagem de voz"), Note: i18n.T("Chega sem transcrição: a transcrição acontece quando a sua ferramenta de IA pede."), JSON: msg("d_4ea126f5d7c3809b55b4ebc6f8da0c54", direct, voice)},
			{ID: "localizacao", Title: i18n.T("Localização"), JSON: msg("d_5fb23706e8d491ac66c5fcd7a9eb1d65", direct, place)},
			{ID: "enquete", Title: i18n.T("Enquete"), Note: i18n.T("Os votos não chegam como avisos; o resultado se lê com get_poll_results."), JSON: msg("d_60c34817f9e5a2bd77d60de8bafc2e76", group, poll)},
			{ID: "encaminhada", Title: i18n.T("Mensagem encaminhada"), JSON: msg("d_71d45928a0f6b3ce88e71ef9cb0d3f87", direct, forwarded)},
			{ID: "sua", Title: i18n.T("Mensagem que você mandou"), Note: i18n.T("Só chega se o webhook incluir as mensagens que você manda. from_me é true."), JSON: msg("d_a4078c5bd329e6f1bb1a42cfe0306a1b", direct, own)},
			{ID: "temporaria", Title: i18n.T("Mensagem com autodestruição"), Note: i18n.T("ttl_period e expires_at dizem quando ela some. Apague a sua cópia até lá: o Telegram MCP apaga a dele."), JSON: msg("d_b5189d6ce43af702cc2b53d0f1417b2c", direct, timed)},
			{ID: "efemera", Title: i18n.T("Mídia de visualização única"), Note: i18n.T("Chega só com o tipo e ephemeral true, sem nome nem tamanho, e não pode ser baixada."), JSON: msg("d_c629ae7df54b0813dd3c64e1a2528c3d", direct, once)},
		}},
		{ID: "edit", Title: i18n.T("Edições (edit)"), Intro: i18n.T("Uma mensagem foi editada. Chega a versão nova inteira, com edit_date; o texto anterior não vem, porque o Telegram não o mostra a ninguém."), Examples: []webhookExample{
			{ID: "editada", Title: i18n.T("Mensagem editada"), JSON: event("edit", "d_d73abf8e065c1924ee4d75f2b3639d4e", direct, webhook.Event{Message: edited})},
		}},
		{ID: "delete", Title: i18n.T("Exclusões (delete)"), Intro: i18n.T("Mensagens apagadas. reason é deleted (apagada no Telegram) ou ttl (o tempo de autodestruição acabou). revoked_by_me só aparece quando sabemos que foi você; o stream não informa isso sozinho. Ids desconhecidos vêm em unresolved_ids, com chat null."), Examples: []webhookExample{
			{ID: "apagada", Title: i18n.T("Mensagem apagada"), JSON: event("delete", "d_e84bc09f176d2a35ff5e8603c474ae5f", direct, webhook.Event{Delete: &webhook.Deletion{MessageIDs: []int{4567, 4568}, UnresolvedIDs: []int{}, Reason: "deleted"}})},
		}},
		{ID: "reaction", Title: i18n.T("Reações (reaction)"), Intro: i18n.T("As reações de uma mensagem mudaram. counts traz a contagem atual; added e removed, quem mudou, quando o Telegram conta. Em grupos grandes ele muitas vezes não diz quem reagiu, e só counts vem preenchido."), Examples: []webhookExample{
			{ID: "reacao", Title: i18n.T("Reação"), JSON: event("reaction", "d_f95cd1a0287e3b46006f9714d585bf60", direct, webhook.Event{Reaction: &webhook.Reaction{MessageID: 4578,
				Counts: []engine.ReactionCount{{Emoji: "❤️", Count: 1}}, Added: []webhook.PeerReaction{{By: maria, Emoji: "❤️", Date: at}}, ToID: 4578, Emoji: "❤️", By: maria}})},
			{ID: "reacao-removida", Title: i18n.T("Reação tirada"), Note: i18n.T("emoji vazio quer dizer que a reação foi tirada."), JSON: event("reaction", "d_0a6de2b1398f4c5711708a25e696c071", direct, webhook.Event{Reaction: &webhook.Reaction{MessageID: 4578,
				Counts: []engine.ReactionCount{}, Removed: []webhook.PeerReaction{{By: maria, Emoji: "❤️", Date: at}}, ToID: 4578, Emoji: "", By: maria}})},
		}},
		{ID: "read", Title: i18n.T("Leituras (read)"), Intro: i18n.T("A outra ponta leu as suas mensagens até max_id. O Telegram não avisa entrega por pessoa, e num grupo diz só que alguém leu até ali, sem dizer quem. A sua própria leitura nunca vira aviso."), Examples: []webhookExample{
			{ID: "lida", Title: i18n.T("Lida"), JSON: event("read", "d_1b7ef3c24a905d6822819b36f7a7d182", direct, webhook.Event{Read: &webhook.Read{MaxID: 4578, MessageIDs: []int{4577, 4578}}})},
		}},
		{ID: "test", Title: i18n.T("Aviso de teste"), Intro: i18n.T("O que o botão Testar manda. Tem test true; não conta para o desligamento automático."), Examples: []webhookExample{
			{ID: "teste", Title: i18n.T("Teste"), JSON: event("test", "d_2c80a4d35ba16e7933920c47a8b8e293", &webhook.Chat{ID: "user:777000", Type: engine.PeerUser, Kind: engine.KindUser, Name: "Teste"},
				webhook.Event{Test: true, Message: webhook.TestMessage(at)})},
		}},
	}
}

func (p *Panel) webhookDocs(w http.ResponseWriter, r *http.Request) (string, any) {
	s := p.snapshot(r.Context())
	return "webhookdocs", struct {
		layout
		Groups  []webhookExampleGroup
		APIBase string
	}{p.layout(r, i18n.N("Webhooks: documentação"), "configuracoes", s), webhookExamples(), strings.TrimSuffix(p.endpoint(), "/mcp")}
}

const webhookDocsSource = `{{define "webhookdocs"}}{{template "head" .}}{{template "nav" .}}
<p class="muted" style="margin:0 0 6px"><a href="/configuracoes#webhooks">← {{t "Configurações › Webhooks"}}</a></p>
<h1>{{t "Webhooks: o que chega"}}</h1>
<p class="lead">{{t "Cada aviso é um POST com JSON no endereço do webhook. Aqui estão o formato, como conferir que ele veio mesmo do Telegram MCP e um exemplo de cada tipo."}}</p>
<nav class="note" aria-label="{{t "Nesta página"}}"><p style="margin:0"><strong>{{t "Nesta página:"}}</strong> <a href="#como-chega">{{t "Como chega"}}</a> · <a href="#assinatura">{{t "Conferir a assinatura"}}</a> · <a href="#envelope">{{t "Os campos"}}</a> · <a href="#exemplo-programa">{{t "Um programa que recebe"}}</a></p>
{{range .Groups}}<p style="margin:6px 0 0"><a href="#{{.ID}}"><strong>{{.Title}}</strong></a>: {{range $i, $e := .Examples}}{{if $i}} · {{end}}<a href="#exemplo-{{$e.ID}}">{{$e.Title}}</a>{{end}}</p>
{{end}}</nav>

<section class="card" id="como-chega">
<div class="card__head"><h2 class="card__title">{{icon "code"}}{{t "Como chega"}}</h2></div>
<div class="card__body stack">
<ul class="rows">
<li class="row"><span class="row__main"><span class="row__title mono">Content-Type: application/json</span><span class="row__meta">{{t "O corpo é o aviso, em UTF-8."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">X-Telegram-MCP-Event</span><span class="row__meta">{{t "O tipo do aviso: message, edit, delete, reaction, read ou test. É o mesmo campo event do corpo."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">X-Telegram-MCP-Delivery</span><span class="row__meta">{{t "O id do aviso, o mesmo delivery_id do corpo. Uma nova tentativa do mesmo aviso repete o id: use-o para não processar duas vezes."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">X-Telegram-MCP-Signature</span><span class="row__meta">{{t "sha256= seguido do HMAC-SHA256 do corpo com a chave do webhook, em hexadecimal."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">X-Telegram-MCP-Attempt</span><span class="row__meta">{{t "O número da tentativa, de 1 a 10. Só informativo: fica fora da assinatura."}}</span></span></li>
</ul>
<ul class="stack" style="margin:0;padding-left:20px">
<li>{{t "<strong>Responda com qualquer 2xx em até 3 segundos</strong> e faça o trabalho pesado depois. Outra resposta, um redirecionamento ou nenhuma resposta contam como falha."}}</li>
<li>{{t "<strong>Cada webhook tem uma fila, entregue em ordem.</strong> Um aviso que falha é tentado de novo, com o mesmo corpo e a mesma assinatura, até 10 vezes em menos de um minuto, e os seguintes esperam."}}</li>
<li>{{t "<strong>Na 10ª falha seguida o webhook é desligado</strong> e os avisos que esperavam são descartados. Ele volta em Configurações, com a fila vazia."}}</li>
<li>{{t "<strong>A fila fica na memória.</strong> Se o Telegram MCP reiniciar, as entregas pendentes se perdem. O catch-up não reenvia fatos já gravados."}}</li>
</ul>
</div></section>

<section class="card" id="assinatura">
<div class="card__head"><h2 class="card__title">{{icon "code"}}{{t "Conferir a assinatura"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "Calcule o HMAC-SHA256 do corpo exatamente como chegou, com a chave que apareceu ao criar o webhook, e compare com o cabeçalho X-Telegram-MCP-Signature. Se não bater, ignore o aviso. Para evitar repetição, guarde os delivery_id já vistos e recuse sent_at com mais de 5 minutos."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">Python</span></div><pre data-copy><code>import hashlib, hmac

def assinatura_valida(corpo: bytes, cabecalho: str, chave: str) -&gt; bool:
    esperado = "sha256=" + hmac.new(chave.encode(), corpo, hashlib.sha256).hexdigest()
    return hmac.compare_digest(esperado, cabecalho or "")</code></pre></div>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">Node.js</span></div><pre data-copy><code>const crypto = require("crypto");

function assinaturaValida(corpo, cabecalho, chave) {
  const esperado = "sha256=" + crypto.createHmac("sha256", chave).update(corpo).digest("hex");
  return typeof cabecalho === "string" &amp;&amp; cabecalho.length === esperado.length &amp;&amp;
    crypto.timingSafeEqual(Buffer.from(cabecalho), Buffer.from(esperado));
}</code></pre></div>
</div></section>

<section class="card" id="envelope">
<div class="card__head"><h2 class="card__title">{{icon "code"}}{{t "Os campos"}}</h2></div>
<div class="card__body stack">
<h3 class="card__sub" style="border:0;padding:0">{{t "Em todo aviso"}}</h3>
<ul class="rows">
<li class="row"><span class="row__main"><span class="row__title mono">schema</span><span class="row__meta">{{t "A versão do formato, hoje 1. Um campo novo não muda a versão."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">event</span><span class="row__meta">{{t "message, edit, delete, reaction, read ou test."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">delivery_id · event_id</span><span class="row__meta">{{t "delivery_id é o id deste aviso para este webhook, igual em todas as tentativas; event_id é o do fato, o mesmo para todos os webhooks."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">occurred_at · sent_at</span><span class="row__meta">{{t "Quando aconteceu no Telegram e quando o aviso entrou na fila, em UTC."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">source · catch_up</span><span class="row__meta">{{t "De onde veio o fato; catch_up é true quando ele foi recuperado depois que o Telegram MCP ficou desligado."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">account</span><span class="row__meta">{{t "A sua conta: id, self_id e username. O telefone nunca vai no aviso."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">chat</span><span class="row__meta">{{t "A conversa: id no formato tipo:número (user:…, chat:…, channel:…), type, kind (user, bot, group, supergroup, channel) e name."}}</span></span></li>
</ul>
<h3 class="card__sub">{{t "message, em message, edit e test"}}</h3>
<ul class="rows">
<li class="row"><span class="row__main"><span class="row__title mono">id · chat_id</span><span class="row__meta">{{t "O id da mensagem dentro da conversa, o mesmo que as ferramentas do MCP usam (responder, reagir, baixar a mídia)."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">date · edit_date</span><span class="row__meta">{{t "Quando foi enviada e, se foi editada, quando."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">from_me · sender_id · sender_name</span><span class="row__meta">{{t "Quem escreveu. from_me é true quando a mensagem é da sua conta."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">text · entities</span><span class="row__meta">{{t "O texto, ou a legenda de uma mídia, e a formatação e as menções, com posições em UTF-16."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">media</span><span class="row__meta">{{t "type (photo, video, voice, audio, document, sticker…), mime_type, filename, bytes, duration_seconds, width e height. O arquivo não vem junto."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">reply_to · forward · topic_id</span><span class="row__meta">{{t "A mensagem citada, de onde veio uma encaminhada e o tópico do fórum (0 fora de fóruns)."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">location · poll · service</span><span class="row__meta">{{t "Localização, enquete e ações de serviço (alguém entrou no grupo, mudou o nome…)."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">mentioned · protected · ttl_period · expires_at</span><span class="row__meta">{{t "Se citam você, se o conteúdo é protegido contra cópia, e quando uma mensagem com autodestruição some."}}</span></span></li>
</ul>
<h3 class="card__sub">delete · reaction · read</h3>
<ul class="rows">
<li class="row"><span class="row__main"><span class="row__title mono">delete</span><span class="row__meta">{{t "message_ids, unresolved_ids e reason (deleted ou ttl; revoked_by_me só quando conhecido)."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">reaction</span><span class="row__meta">{{t "message_id, counts, added e removed; to, emoji e by quando a mudança é uma só e tem autor conhecido."}}</span></span></li>
<li class="row"><span class="row__main"><span class="row__title mono">read</span><span class="row__meta">{{t "max_id e message_ids: as suas mensagens que essa leitura passou a cobrir."}}</span></span></li>
</ul>
</div></section>

{{range .Groups}}
<section class="card" id="{{.ID}}">
<div class="card__head"><h2 class="card__title">{{icon "code"}}{{.Title}}</h2></div>
<div class="card__body stack">
<p class="muted">{{.Intro}}</p>
{{range .Examples}}
<div class="stack" id="exemplo-{{.ID}}">
<h3 class="card__sub">{{.Title}}</h3>
{{with .Note}}<p class="muted" style="margin:0">{{.}}</p>{{end}}
<div class="snippet"><div class="snippet__head"><span class="snippet__title">JSON</span></div><pre data-copy><code>{{.JSON}}</code></pre></div>
</div>
{{end}}
</div></section>
{{end}}

<section class="card" id="exemplo-programa">
<div class="card__head"><h2 class="card__title">{{icon "terminal"}}{{t "Um programa que recebe"}}</h2></div>
<div class="card__body stack">
<p class="muted">{{t "Um receptor mínimo em Python, sem bibliotecas: confere a assinatura, ignora repetições, responde na hora e mostra quem escreveu o quê. Rode, adicione o webhook com o endereço http://127.0.0.1:8080/telegram e cole a chave no lugar indicado."}}</p>
<p class="note">{{t "Para este receptor local, marque Permitir destinos locais e de rede privada ao criar ou religar o webhook. Pela API, envie allow_private_network=true. Destinos privados, loopback e link-local exigem essa permissão; autorize apenas destinos em que você confia."}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">receptor.py</span></div><pre data-copy><code># {{t "Recebe os avisos do Telegram MCP em http://127.0.0.1:8080/telegram"}}
import hashlib, hmac, json
from http.server import BaseHTTPRequestHandler, HTTPServer

CHAVE = "{{t "cole aqui a chave do webhook"}}"
VISTOS = set()

class Webhook(BaseHTTPRequestHandler):
    def do_POST(self):
        corpo = self.rfile.read(int(self.headers["Content-Length"]))
        esperado = "sha256=" + hmac.new(CHAVE.encode(), corpo, hashlib.sha256).hexdigest()
        if not hmac.compare_digest(esperado, self.headers.get("X-Telegram-MCP-Signature", "")):
            self.send_response(401)
            self.end_headers()
            return
        self.send_response(204)  # {{t "responda logo; trabalhe depois"}}
        self.end_headers()
        aviso = json.loads(corpo)
        if aviso["delivery_id"] in VISTOS:
            return
        VISTOS.add(aviso["delivery_id"])
        if aviso["event"] in ("message", "edit"):
            chat, m = aviso.get("chat") or {}, aviso["message"]
            print(chat.get("name"), "·", m.get("sender_name", "{{t "eu"}}"), ":", m.get("text", ""))

HTTPServer(("127.0.0.1", 8080), Webhook).serve_forever()</code></pre></div>
<p class="muted">{{t "Os webhooks também se criam e se apagam por programa, pela API local deste computador, com o token do MCP:"}}</p>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Criar um webhook"}}</span></div><pre data-copy><code>curl -s -X POST -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TELEGRAM_MCP_TOKEN" \
  -d '{"url": "http://127.0.0.1:8080/telegram", "events": ["message", "edit", "reaction"], "allow_private_network": true}' \
  {{.APIBase}}/api/webhooks</code></pre></div>
<div class="snippet"><div class="snippet__head"><span class="snippet__title">{{t "Religar um webhook local"}}</span></div><pre data-copy><code>curl -s -X PATCH -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TELEGRAM_MCP_TOKEN" \
  -d '{"enabled":true,"allow_private_network":true}' \
  {{.APIBase}}/api/webhooks/WEBHOOK_ID</code></pre></div>
</div></section>
{{template "foot" .}}{{end}}`
