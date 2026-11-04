package panel

const credentialSetupPage = `<!doctype html>
<html lang="{{lang}}" data-app><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{t "Credenciais da API do Telegram"}} · {{shortName}}</title><style>{{css}}
.credential-setup{max-width:560px;margin:8vh auto;padding:32px}.credential-setup label{display:block;margin:20px 0}.credential-setup input{display:block;width:100%;box-sizing:border-box;margin-top:8px;padding:12px}.credential-setup button{margin-top:16px;padding:12px 20px}
</style></head><body><main class="credential-setup"><h1>{{t "Credenciais da API do Telegram"}}</h1>
<p>{{t "Antes do QR code, crie suas credenciais de aplicativo no Telegram."}}</p>
<p><a href="https://my.telegram.org/apps" target="_blank" rel="noopener noreferrer">my.telegram.org/apps</a></p>
<p>{{t "Os valores ficam no cofre deste computador. Não são a senha nem o código de login do Telegram."}}</p>
{{if .}}<p role="alert">{{t "Não foi possível iniciar. Confira o api_id e o api_hash, verifique o acesso ao cofre e tente novamente."}}</p>{{end}}
<form method="post" action="/api/credentials" autocomplete="off">
<label>api_id<input name="api_id" type="text" inputmode="numeric" pattern="[0-9]+" maxlength="20" required autocomplete="off" spellcheck="false"></label>
<label>api_hash<input name="api_hash" type="password" pattern="[a-fA-F0-9]{32}" minlength="32" maxlength="32" required autocomplete="off" spellcheck="false"></label>
<button type="submit">{{t "Salvar e continuar para o QR code"}}</button>
</form></main></body></html>`
