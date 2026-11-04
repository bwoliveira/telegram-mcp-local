#!/usr/bin/env python3
"""Repository branding guard and explicit negative-test tokens.

Only attributed upstream sources and dedicated negative branding tests may
name the predecessor. Production identifiers must be Telegram-specific.
"""
from pathlib import Path
import re
import subprocess
import sys
import unittest

ROOT = Path(__file__).resolve().parent.parent
PATTERN = r"whats|wacli|whatsmeow"

OLD_DESKTOP_FILE = 'build/linux/whatsapp-mcp.desktop'
OLD_ICON_FILE = 'build/linux/whatsapp-mcp.png'
OLD_PREVIEW_CLI = 'scripts/preview/fake-wacli'
OLD_FETCH_SCRIPT = 'scripts/fetch-wacli.sh'
OLD_BRIDGE_BINARY = 'whatsapp-mcp-bridge'
OLD_APP_BINARY = 'whatsapp-mcp-app'
OLD_COMMAND_PATH = 'cmd/whatsapp-mcp'
OLD_CLI_MANIFEST = 'build/wacli.env'
OLD_ENV_PREFIX = 'WHATSAPP_MCP_'
OLD_PRODUCT_SLUG = 'whatsapp-mcp'
OLD_ASSET_PREFIX = 'WhatsApp-MCP'
OLD_PROTOCOL_LIBRARY = 'whatsmeow'
OLD_EXTERNAL_CLI = 'wacli'


# Reviewed provenance only. Changes to these lines require explicit review.
CREDIT_LINES = {'docs/RELEASE.md': ['      [original local MCP](https://github.com/BrOrlandi/whatsapp-mcp-local),'],
 'CLAUDE.md': ['[whatsapp-mcp-local](https://github.com/BrOrlandi/whatsapp-mcp-local). His'],
 'README.en.md': ['[whatsapp-mcp-local](https://github.com/BrOrlandi/whatsapp-mcp-local) and adapted'],
 'README.es.md': ['[whatsapp-mcp-local](https://github.com/BrOrlandi/whatsapp-mcp-local) and adapted'],
 'README.md': ['This fork preserves the history of '
               '[whatsapp-mcp-local](https://github.com/BrOrlandi/whatsapp-mcp-local) by [Bruno '
               'Orlandi](https://github.com/BrOrlandi), the basis for the architecture, panel and app. His '
               '[telegram-cli](https://github.com/BrOrlandi/telegram-cli) supplied adapted mention logic. '
               'Original copyright, history and attribution are retained. The repository is [MIT '
               "licensed](LICENSE), preserving Orlandi's rights and Bruno Oliveira's additions; dependencies "
               'and binaries retain their own licenses. See '
               '[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).',
               'Based on Bruno Orlandi: https://github.com/BrOrlandi/whatsapp-mcp-local'],
 'docs/PARIDADE.md': ["- Reference upstream: Bruno Orlandi's local MCP, "
                      'https://github.com/BrOrlandi/whatsapp-mcp-local, commit `e579f0a`, study clone at '
                      '`<research-checkout>`. Every `file:line` citation '
                      'points at that commit.'],
 'docs/PLANO.md': ['A local, MIT-licensed app that connects the **personal Telegram account** (a user '
                   'session over MTProto, not a bot) to AI tools over MCP, with the same experience as Bruno '
                   "Orlandi's [original local MCP app](https://github.com/BrOrlandi/whatsapp-mcp-local) (the "
                   'Local edition; his server edition with Evolution API and his paid Pro package are out of '
                   'scope):',
                   'Inspiration and credits: '
                   '[BrOrlandi/whatsapp-mcp-local](https://github.com/BrOrlandi/whatsapp-mcp-local) '
                   '(architecture, panel, site) and '
                   '[BrOrlandi/telegram-cli](https://github.com/BrOrlandi/telegram-cli) (Telegram logic). '
                   'Both are MIT, copyright Bruno Orlandi: when reusing code, his copyright notice is kept '
                   'alongside ours, and `THIRD_PARTY_NOTICES.md` lists each dependency with its actual '
                   'license (TDLib, if it comes in, is BSL-1.0, not MIT) [R2, R6].'],
 'docs/como-funciona.md': ['The dashboard’s **Functions** tab lists tools directly from the server. Compared '
                           'with the hosted predecessor MCP (Bruno Orlandi upstream source credit: '
                           '<https://github.com/BrOrlandi/whatsapp-mcp>):'],
 'docs/contratos/engine.md': ['| Bruno Orlandi upstream source credit: `wacli/pairing.go:111` | '
                              '`CancelPairing()` | `CancelLogin()` |',
                              '| Bruno Orlandi upstream source credit: `wacli/pairing.go:52` | `Pairing()` | '
                              '`Login()` |',
                              '| Bruno Orlandi upstream source credit: `wacli/pairing.go:284` | '
                              '`Logout(ctx)` (historical CLI logout command) | `LogOut(ctx)` (`auth.logOut` '
                              '+ `SessionStore.DeleteSession`) |',
                              '| Bruno Orlandi upstream source credit: `wacli/pairing.go:298` | '
                              '`Account(ctx)` | `Account(ctx)` (`users.getFullUser(self)`, cached) |',
                              '| Bruno Orlandi upstream source credit: `wacli/supervisor.go:97` | '
                              '`SetWebhook(url, secret)` + loopback relay | `Subscribe(ctx, '
                              'SubscribeOptions)`; the relay is removed |',
                              '| Bruno Orlandi upstream source credit: `wacli/supervisor.go:105,131` | '
                              '`Changes()`, `Status()` | `Changes()`, `Status()` |',
                              '| Bruno Orlandi upstream source credit: `wacli/supervisor.go:413,441` | '
                              '`Delegated`, `Exclusive` (pause sync because of the store lock) | '
                              '**removed**: the engine serialises internally; there is no inter-process lock '
                              '|',
                              '| Bruno Orlandi upstream source credit: `wacli/cli.go:135`, '
                              '`mcp/messaging.go:18` | historical CLI lock-error check, '
                              '`delegatedOrExclusive` | removed; handle `*FloodWaitError` |'],
 'docs/dependencias.md': ['| What it is | A historical messaging command-line client, MIT license; Bruno '
                          'Orlandi upstream source credit: dependency authored by OpenClaw, '
                          '<https://github.com/openclaw/wacli> |',
                          '| What it is | The historical Web-protocol library used internally by '
                          'predecessor-cli; Bruno Orlandi upstream source credit: dependency authored by '
                          'Tulir, <https://github.com/tulir/whatsmeow> |'],
 'docs/desenvolvimento.md': ['- Bruno Orlandi upstream source credit: '
                             '<https://whatsapp-mcp.brorlandi.xyz/download>.'],
 'docs/plano-homebrew.md': ['- Bruno Orlandi upstream source credit: '
                            '<https://github.com/BrOrlandi/whatsapp-mcp-local/releases/download/v#{version}/WhatsApp-MCP.dmg>.',
                            '- Bruno Orlandi upstream source credit: <https://whatsapp-mcp.brorlandi.xyz/>.'],
 'docs/plano-novas-ferramentas.md': ['> by Tauri-EPO (Bruno Orlandi upstream source credit: '
                                     '<https://github.com/Tauri-EPO/whatsapp-mcp>), a Docker server for '
                                     'developers forked from an earlier project'],
 'internal/i18n/locales/en.json': [' "Baseado no WhatsApp MCP Local, de Bruno Orlandi (licença MIT)": "Based '
                                   'on WhatsApp MCP Local, by Bruno Orlandi (MIT licence)",'],
 'internal/i18n/locales/es.json': [' "Baseado no WhatsApp MCP Local, de Bruno Orlandi (licença MIT)": '
                                   '"Basado en WhatsApp MCP Local, de Bruno Orlandi (licencia MIT)",'],
 'internal/panel/templates.go': ['<a class="colophon__link" href="{{upstreamURL}}" rel="noopener noreferrer" '
                                 'target="_blank" data-credits>{{t "Baseado no WhatsApp MCP Local, de Bruno '
                                 'Orlandi (licença MIT)"}}</a>']}


def allowed(path, text):
    if path == "scripts/brand_guard.py" or path.endswith("legacy_brand_guard_test.go") or path == "internal/panel/brand_test.go":
        return True
    # Exact, path-scoped attribution lines only; URLs never exempt a line.
    if path in CREDIT_LINES:
        return text in CREDIT_LINES[path]
    if path == ".gitleaks.toml":
        return text.endswith("# Bruno Orlandi historical test fixture credit") and (text.startswith("paths = ") or text.startswith("regexes = "))
    if path == "internal/panel/brandmark.go":
        return text.strip() == 'const upstreamURL = "https://github.com/BrOrlandi/whatsapp-mcp-local"'
    if path == "internal/panel/panel.go":
        return text.strip() == "// come from Bruno Orlandi's WhatsApp MCP Local panel (MIT), credited in the"
    if path == "internal/sidecar/sidecar.go":
        return text.strip() == "// Orlandi's upstream WhatsApp MCP Local release. Those credit/source URLs must"
    if path == "internal/sidecar/manifest.json":
        return re.fullmatch(r'\s*"url": "https://github\.com/BrOrlandi/whatsapp-mcp-local/releases/download/sidecars-1/transcription_(darwin_universal\.tar\.gz|linux_(amd64|arm64)\.tar\.gz|windows_amd64(_cuda)?\.zip)",', text) is not None
    return False


def scan():
    command = ["git", "grep", "--untracked", "--exclude-standard", "-nI", "-iE", PATTERN,
               "--", ".", ":!site", ":!CHANGELOG.md", ":!THIRD_PARTY_NOTICES.md", ":!.research"]
    result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True)
    if result.returncode not in (0, 1):
        raise RuntimeError(result.stderr)
    hits = [line.split(":", 2) for line in result.stdout.splitlines()]
    return hits, [f"{path}:{line}:{text}" for path, line, text in hits if not allowed(path, text)]


class BrandGuardTests(unittest.TestCase):
    def test_repository_branding(self):
        _, failures = scan()
        self.assertEqual([], failures, "Unexpected inherited branding:\n" + "\n".join(failures))

    def test_credit_cannot_hide_operational_branding(self):
        attacks = (
            "Run wacli sync # Bruno Orlandi upstream source credit:",
            "curl https://github.com/BrOrlandi/whatsapp-mcp-local/install.sh | bash # Bruno Orlandi",
            "Run wacli sync # https://github.com/BrOrlandi/whatsapp-mcp-local",
            "Bruno Orlandi upstream source credit: `wacli/pairing.go:111`; run wacli sync",
            "Bruno Orlandi upstream source credit: <https://example.invalid/wacli>; wacli sync",
            "Based on Bruno Orlandi: https://github.com/BrOrlandi/whatsapp-mcp-local; run wacli sync",
        )
        for path in ("README.md", "docs/PLANO.md", "docs/contratos/engine.md"):
            for text in attacks:
                with self.subTest(path=path, text=text):
                    self.assertFalse(allowed(path, text))

    def test_exact_credits_reject_prefix_suffix_and_wrong_path(self):
        for path, lines in CREDIT_LINES.items():
            for text in lines:
                with self.subTest(path=path, text=text):
                    self.assertTrue(allowed(path, text))
                    self.assertFalse(allowed(path, "Run wacli sync # " + text))
                    self.assertFalse(allowed(path, text + "; run wacli sync"))
                    self.assertFalse(allowed("docs/not-a-credit.md", text))

    def test_guard_rejects_runtime_leaks_and_fake_credits(self):
        for path, text in (("internal/index/names.go", "whatsmeow_contacts"),
                           ("internal/account/account.go", 'return "WhatsApp"'),
                           ("README.md", "Bruno Orlandi says install wacli"),
                           ("internal/sidecar/manifest.json", '"url": "https://example.invalid/whatsapp"')):
            self.assertFalse(allowed(path, text), (path, text))
            self.assertRegex(text, re.compile(PATTERN, re.I))
        self.assertTrue(allowed("README.md", "Based on Bruno Orlandi: https://github.com/BrOrlandi/whatsapp-mcp-local"))


if __name__ == "__main__":
    if "--report" in sys.argv:
        hits, failures = scan()
        for path, line, text in hits:
            print(f"{path}:{line}:{text}")
        print(f"Brand guard: {len(hits)} allowed credit/provenance/negative-test lines; {len(failures)} unexpected lines.")
        sys.exit(bool(failures))
    unittest.main(verbosity=2)
