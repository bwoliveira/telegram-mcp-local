package panel

import (
	"strconv"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// codeVia says where Telegram sent the login code, from Login.CodeType.
func codeVia(codeType string) string {
	switch codeType {
	case "app":
		return i18n.T("O Telegram mandou o código no app, em outro aparelho onde você já está conectado: procure a conversa \"Telegram\".")
	case "sms":
		return i18n.T("O Telegram mandou o código por SMS.")
	case "call":
		return i18n.T("O Telegram vai ligar e dizer o código.")
	case "flash_call", "missed_call":
		return i18n.T("O Telegram vai fazer uma chamada rápida: o código são os últimos dígitos do número que ligar.")
	case "email":
		return i18n.T("O Telegram mandou o código para o e-mail de login da conta.")
	case "fragment":
		return i18n.T("O código chegou pelo Fragment, para números anônimos.")
	}
	return i18n.T("Digite o código que o Telegram mandou.")
}

// modeLabel is the account's mode in the person's language.
func modeLabel(m engine.Mode) string {
	if m == engine.ModeReadWrite {
		return i18n.T("Leitura e escrita")
	}
	return i18n.T("Somente leitura")
}

// eventLabel says one lifecycle event for the Status tab. Only the fields the
// panel knows are read; anything else shows as the event's name.
func eventLabel(ev engine.LifecycleEvent) string {
	switch ev.Event {
	case "connection":
		if st, ok := ev.Data["state"].(string); ok {
			_, label := syncLabel(engine.Status{State: engine.ConnState(st)})
			return i18n.Tf("Conexão: %s", i18n.T(label))
		}
	case "mode":
		if m, ok := ev.Data["mode"].(string); ok {
			return i18n.Tf("Modo: %s", modeLabel(engine.Mode(m)))
		}
	case "authorized":
		return i18n.T("Login concluído")
	case "logged_out":
		return i18n.T("Saiu da conta")
	}
	return ev.Event
}

// peerLabel names a conversation by its kind and id, never by its title: the
// Status tab shows no message data.
func peerLabel(ref *engine.PeerRef) string {
	if ref == nil {
		return ""
	}
	kind := i18n.T("conversa")
	switch ref.Type {
	case engine.PeerUser:
		kind = i18n.T("conversa privada")
	case engine.PeerChat:
		kind = i18n.T("grupo")
	case engine.PeerChannel:
		kind = i18n.T("canal ou supergrupo")
	}
	return kind + " " + strconv.FormatInt(ref.ID, 10)
}
