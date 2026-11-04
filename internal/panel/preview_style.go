package panel

// previewCSS is what the Conta page's chat preview adds to the chat list and
// bubbles extraCSS already draws: the filters, the search inside a chat and
// the state of a page of chats or messages on its way.
const previewCSS = `
.chatfilters,.chatsearch{display:flex;flex-wrap:wrap;gap:8px 14px;align-items:center;margin:14px 0}
.chatsearch{margin:0 0 10px;gap:8px}
.chatfilters input[type=search],.chatsearch input[type=search]{flex:1 1 200px;min-width:0}
.chatfilters input[type=search],.chatsearch input[type=search],.chatfilters select{font:inherit;padding:8px 12px;color:var(--text);background:var(--surface-soft);border:1px solid var(--border-strong);border-radius:var(--radius-sm)}
.chatfilters input:focus-visible,.chatsearch input:focus-visible,.chatfilters select:focus-visible{outline:2px solid var(--brand);outline-offset:1px}
.preview-list{min-width:0}
[data-chats] .chats{max-height:560px;overflow:auto}
[data-chats] .chat:focus-visible{outline:2px solid var(--brand);outline-offset:-2px}
[data-chats] [aria-busy=true]{opacity:.65;transition:opacity .2s}
[data-chats] .busy{margin:8px 0 0}
.busy .btn{margin-left:auto}
@media (prefers-reduced-motion:reduce){[data-chats] [aria-busy=true]{transition:none}}
`

// navLoadingCSS is the feedback of a click on a link to another page of the
// panel: a bar along the top at once, and a note when the page is slow.
const navLoadingCSS = `
.navload{position:fixed;top:0;left:0;right:0;height:3px;z-index:60;overflow:hidden;pointer-events:none}
.navload::before{content:"";position:absolute;top:0;bottom:0;left:-40%;width:40%;background:var(--brand);animation:navload 1.1s ease-in-out infinite}
@keyframes navload{to{left:100%}}
@media (prefers-reduced-motion:reduce){.navload::before{animation:none;left:0;width:100%;opacity:.6}}
.navnote{position:fixed;right:16px;bottom:16px;z-index:61;max-width:min(420px,calc(100vw - 32px));display:flex;flex-wrap:wrap;align-items:center;gap:8px 12px;padding:12px 14px;border-radius:var(--radius-sm);background:var(--surface);border:1px solid var(--border-strong);box-shadow:var(--shadow-lift);font-size:.9rem;color:var(--text-soft)}
.navnote--late{border-color:var(--danger-border);background:var(--danger-bg);color:var(--danger)}
.navnote[hidden]{display:none}
.nav a[aria-busy=true]{color:var(--text)}
.nav a[aria-busy=true] .icon{animation:pulse 1s ease-in-out infinite}
@media (prefers-reduced-motion:reduce){.nav a[aria-busy=true] .icon{animation:none}}
`
