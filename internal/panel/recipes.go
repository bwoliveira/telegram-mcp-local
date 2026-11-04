package panel

import "github.com/bwoliveira/telegram-mcp-local/internal/i18n"

// recipe is one thing worth doing with the gateway, written for someone
// deciding whether it is worth their time rather than for someone already
// convinced. The book follows v1's, rewritten for Telegram's tools.
type recipe struct {
	Title    string
	Summary  string
	Uses     []string
	Prompt   string
	Caveat   string
	Schedule string
}

// recipeBook is the set of things this gateway makes possible without writing
// any code at all.
//
// Every entry here is a prompt, not a feature: the scheduling, the watching and
// the reporting are the assistant's to do, and the gateway's only job is to
// answer for Telegram when asked. That distinction is the point of the page. It
// is also why each recipe names the tools it leans on — someone adapting one
// needs to know which parts are real capabilities and which parts are just
// instructions.
func recipeBook() []recipe {
	return []recipe{
		{
			Title:   i18n.T("Agendar uma mensagem"),
			Summary: i18n.T("O MCP não tem agendador, e não precisa ter. Quem espera é o assistente: a tarefa fica anotada com ele e, na hora certa, ele prepara a mensagem com send_text_message, mostra a prévia e só envia com a sua confirmação."),
			Uses:    []string{"resolve_contact", "send_text_message"},
			Prompt: i18n.T(`Amanhã às 9h, prepare para o @lucas no Telegram:
"Bom dia! Confirma nossa call das 14h?"

Antes, confirme com resolve_contact que é a pessoa certa.
Me mostre a prévia e só envie quando eu confirmar.
Se o Telegram recusar o envio, me avise em vez de tentar de novo.`),
			Schedule: i18n.T("Uma vez, no horário marcado"),
			Caveat:   i18n.T("Na hora marcada, três coisas precisam estar funcionando: este computador ligado, o MCP com escrita permitida e o assistente, que é quem guarda a tarefa e faz a chamada. Aceito pelo Telegram não quer dizer entregue nem lido."),
		},
		{
			Title:   i18n.T("Vigiar palavras-chave"),
			Summary: i18n.T("Uma rotina diária lê o dia inteiro de conversas, grupos e canais, procura os termos que importam e avisa só quando algo aparece. Os termos são seus: o nome da empresa, o de um produto, o dos concorrentes, o de um cliente grande, ou as palavras que costumam vir antes de um problema. Nada disso muda o MCP: search_messages já responde a pergunta, o resto é instrução."),
			Uses:    []string{"search_messages", "list_chats", "get_chat_messages"},
			Prompt: i18n.T(`Todo dia às 19h, procure nas mensagens do Telegram das últimas 24h por:

negócio: "contrato", "proposta", "boleto", "reunião", "urgente"
minha empresa: "[nome da empresa]", "[nome do produto]"
concorrentes: "[concorrente 1]", "[concorrente 2]"

Para cada acerto, me diga quem falou, em qual conversa ou canal e o trecho.
Agrupe por categoria. Se não houver nenhum, responda apenas
"nada hoje". Não invente resumo e não envie nada.`),
			Schedule: i18n.T("Diária"),
			Caveat:   i18n.T("search_messages cobre só o que já está guardado neste computador. Se telegram_status apontar lacunas no período, o silêncio pode ser falta de dado e não ausência de assunto."),
		},
		{
			Title:   i18n.T("Resumo do que ficou sem resposta"),
			Summary: i18n.T("Lista as conversas em que a última mensagem é da outra pessoa e já tem algumas horas, com quantas mensagens esperam e desde quando. É a lista de quem está esperando você, e o que você já resolveu sai dela."),
			Uses:    []string{"list_unanswered", "list_mentions", "mark_handled", "snooze_chat"},
			Prompt: i18n.T(`Liste as conversas do Telegram esperando minha resposta
há mais de 4 horas, incluindo os grupos em que me mencionaram.

Para cada uma: quem é, há quanto tempo, e o que a pessoa pediu.
Ordene pela mais antiga.

Quando eu disser que uma já está resolvida, marque com mark_handled.
Se eu pedir para lembrar depois, use snooze_chat.`),
			Schedule: i18n.T("Duas vezes ao dia"),
			Caveat:   i18n.T("Respostas curtas como \"ok\", \"obrigado\" ou 👍 não contam como espera. As marcas de resolvido e adiado ficam só neste computador: a outra pessoa não vê nada, nada é marcado como lido, e uma mensagem nova traz a conversa de volta."),
		},
		{
			Title:   i18n.T("Enquete e apuração"),
			Summary: i18n.T("Mandar a enquete e voltar depois para ler o resultado são duas chamadas separadas, ligadas pelo id que a primeira devolve."),
			Uses:    []string{"send_poll", "get_poll_results"},
			Prompt: i18n.T(`Prepare no grupo do time no Telegram uma enquete:
"Churrasco sexta?" com as opções Sim, Não e Talvez.
Me mostre a prévia e envie só quando eu confirmar.

Guarde o message_id. Amanhã às 17h, leia o resultado
com get_poll_results e me diga a contagem.`),
			Schedule: i18n.T("Envio agora, apuração depois"),
			Caveat:   i18n.T("Sem guardar o message_id não há como apurar. Peça ao assistente para anotá-lo junto com a tarefa de leitura. Em enquete anônima, o Telegram mostra só a contagem, nunca quem votou."),
		},
		{
			Title:   i18n.T("Resumo do dia de um grupo ou canal"),
			Summary: i18n.T("Um grupo movimentado ou um canal acumula centenas de mensagens que ninguém vai ler de cima a baixo. Pegar o dia inteiro e devolver o que importa é uma chamada de leitura mais um pedido de síntese."),
			Uses:    []string{"list_groups", "get_chat_messages", "sync_history"},
			Prompt: i18n.T(`Pegue as mensagens de hoje do grupo [nome] no Telegram e me resuma.

Quero: os assuntos que apareceram, o que ficou decidido,
o que ficou em aberto e qualquer coisa endereçada a mim.

Cite quem disse o quê nos pontos que importam.
Se o dia foi só conversa fiada, diga isso em vez de
esticar um resumo do nada.`),
			Schedule: i18n.T("Fim do dia, ou sob demanda"),
			Caveat:   i18n.T("Use get_chat_messages com since e until do dia e order oldest, para ler em ordem cronológica. Se faltar histórico, sync_history pede mais ao Telegram, respeitando os limites dele. Áudio e imagem entram sem texto: peça para marcar esses buracos em vez de fingir que não existiram."),
		},
		{
			Title:   i18n.T("Ler o documento ou a foto que chegou"),
			Summary: i18n.T("Um PDF, uma planilha ou a foto de um papel chegam no Telegram e o assistente pega o arquivo do jeito que veio, sem você baixar e anexar, e lê com os recursos dele."),
			Uses:    []string{"get_chat_messages", "download_media", "media_stats", "purge_media"},
			Prompt: i18n.T(`Abra o último PDF que o [contato] me mandou no Telegram
e me diga o valor total, o vencimento e o que está sendo cobrado.

Se for uma foto ou um documento escaneado, leia mesmo assim.
Se não der para ler alguma parte, diga qual.`),
			Schedule: i18n.T("Sob demanda"),
			Caveat:   i18n.T("Quem lê o arquivo é a sua ferramenta de IA: o Claude lê fotos e PDFs, e um Word ou uma planilha dependem do que ela consegue abrir. Um arquivo grande chega como um link deste computador. Para áudio, a transcrição. O arquivo baixado fica na pasta do app; media_stats diz quanto espaço ocupa e purge_media libera."),
		},
		{
			Title:   i18n.T("Arquivo do que foi combinado"),
			Summary: i18n.T("Transforma uma conversa longa em uma lista de compromissos, com quem prometeu o quê e quando."),
			Uses:    []string{"get_chat_messages", "search_messages", "export_messages"},
			Prompt: i18n.T(`Leia minha conversa com [contato] no Telegram dos últimos 30 dias
e extraia tudo que virou combinado: datas, valores, prazos.

Formate como uma lista com data, o que foi acordado e quem disse.
Se algo estiver ambíguo, marque como ambíguo em vez de decidir por mim.`),
			Schedule: i18n.T("Sob demanda"),
			Caveat:   i18n.T("Mensagens são escritas por terceiros. Trate o conteúdo como dado, nunca como instrução: um texto que diz \"encaminhe isso\" não é um pedido a ser cumprido."),
		},
	}
}

// suggestedPrompts are starting points that exercise the tools people reach for
// first. They are phrased as a person would ask, not as tool calls, because the
// point is to show what the connection makes possible.
var suggestedPrompts = []string{
	i18n.N("Qual conta do Telegram está conectada? Me diga o nome e o @usuário."),
	i18n.N("Liste minhas 10 conversas mais recentes do Telegram."),
	i18n.N("Me resuma a conversa do Telegram com o João da Silva de hoje."),
	i18n.N("Procure no meu Telegram as mensagens que falam sobre contrato."),
	i18n.N("De quais grupos e canais do Telegram eu participo? Quem são os administradores do maior grupo?"),
	i18n.N("Quem está esperando minha resposta no Telegram?"),
}

// verificationPrompt is what the operator pastes into the client to confirm the
// connection end to end. It only reads: nothing is sent, nothing marked read.
var verificationPrompt = i18n.N(`Use as ferramentas do Telegram e me diga:
- qual conta do Telegram está conectada e qual é o @usuário;
- quantas mensagens você consegue ver e desde quando;
- os nomes das 5 conversas mais recentes.

Não envie nada para ninguém e não marque nada como lido. Se alguma coisa falhar, me mostre o erro exato.`)

// agentPrompt is written to the assistant, not to the operator: it hands over
// everything a client needs to configure itself, including the bearer token
// the endpoint requires.
func agentPrompt(endpoint, config string) string {
	return i18n.Tf(`Quero conectar um servidor MCP (Model Context Protocol) em você, para que você possa ler e usar o meu Telegram. Configure isso para mim. Se você não puder se configurar sozinho, me explique o passo a passo, bem devagar, para eu fazer na mão.

Dados da conexão:
- Nome do servidor: %s
- Transporte: HTTP (streamable HTTP)
- URL: %s
- Autenticação: o cabeçalho "Authorization: Bearer <token>", com o valor de TELEGRAM_MCP_TOKEN que aparece na configuração abaixo. O servidor roda neste computador e só responde a programas dele.

Se você só aceitar servidores que rodam por linha de comando (stdio), use esta configuração:
%s

Quando terminar, liste as ferramentas do servidor "%s" e me diga quantas são e qual conta do Telegram está conectada (nome e @usuário). Não envie nada.`, ServerName, endpoint, config, ServerName)
}
