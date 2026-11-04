#!/usr/bin/env python3
"""Opt-in Linux Wails smoke with a private D-Bus watcher and fake engine only.

Run: dbus-run-session -- python3 cmd/app/testdata/gui_smoke.py APP EVIDENCE_DIR
Requires an X display, python3-dbus/python3-gi, xdotool, ImageMagick import
and tesseract. --delay-startup holds only the fixture HTTP-token lock until
the initial native waiting document has rendered. --decoy adds an owned
matching-title GTK window and verifies it receives no input or close event.
No real account, default service port, user config, or installed service is used.
"""
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import select
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request

import dbus
import dbus.service
from dbus.mainloop.glib import DBusGMainLoop, threads_init
from gi.repository import GLib


DBusGMainLoop(set_as_default=True)
threads_init()
bus = dbus.SessionBus()
registered = []


class Watcher(dbus.service.Object):
    @dbus.service.method("org.kde.StatusNotifierWatcher", in_signature="s", sender_keyword="sender")
    def RegisterStatusNotifierItem(self, service, sender=None):
        registered.append((str(sender), str(service)))

    @dbus.service.method("org.kde.StatusNotifierWatcher", in_signature="s")
    def RegisterStatusNotifierHost(self, service):
        pass


name = dbus.service.BusName("org.kde.StatusNotifierWatcher", bus)
watcher = Watcher(bus, "/StatusNotifierWatcher")
loop = GLib.MainLoop()
threading.Thread(target=loop.run, daemon=True).start()

app = Path(sys.argv[1]).resolve(strict=True)
evidence = Path(sys.argv[2]).resolve()
evidence.mkdir(parents=True, exist_ok=True)
root = Path(tempfile.mkdtemp(prefix="fixture-", dir=evidence))
os.chmod(root, 0o700)
for child in ("data", "home", "config", "cache", "tmp"):
    (root / child).mkdir(mode=0o700)
with socket.socket() as s:
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
assert port != 47831
(root / "data" / "config.json").write_text(json.dumps({
    "port": port, "autostart": False, "close_to_tray": True,
    "language": "pt-BR", "seen": ["close", "trayless"],
}))
os.chmod(root / "data" / "config.json", 0o600)
token = base64.urlsafe_b64encode(bytes(range(32))).decode().rstrip("=")
env = {k: v for k, v in os.environ.items() if not k.startswith("TELEGRAM_MCP_")}
env.update({
    "HOME": str(root / "home"), "XDG_CONFIG_HOME": str(root / "config"),
    "XDG_DATA_HOME": str(root / "data"), "XDG_CACHE_HOME": str(root / "cache"),
    "TMPDIR": str(root / "tmp"), "TELEGRAM_MCP_ENGINE": "fake",
    "TELEGRAM_MCP_DATA": str(root / "data"), "TELEGRAM_MCP_SECRETS": "file",
    "TELEGRAM_MCP_TOKEN": token, "TELEGRAM_MCP_PORT": str(port),
    "TELEGRAM_MCP_UPDATES_API": "http://127.0.0.1:1",
    "WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS": "1",
    "LIBGL_ALWAYS_SOFTWARE": "1", "GDK_BACKEND": "x11",
})
base = f"http://127.0.0.1:{port}"


def api(path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, headers={
        "Authorization": "Bearer " + token, "Content-Type": "application/json",
    })
    with urllib.request.urlopen(req, timeout=4) as response:
        return json.load(response)


def wait_for(fn, description, timeout=25):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except Exception as exc:
            last = type(exc).__name__
        time.sleep(0.2)
    raise AssertionError(f"timeout waiting for {description} ({last})")


def assert_owned(window):
    # Recheck immediately before every capture/activation/input, even after a
    # conjunctive search: never trust a cached XID or title on a shared display.
    owner = subprocess.run(["xdotool", "getwindowpid", window],
                           capture_output=True, text=True, check=True).stdout.strip()
    assert owner == str(proc.pid), f"window {window} PID {owner} is not fixture owner {proc.pid}"
    assert proc.poll() is None, "fixture owner has exited"


def windows():
    result = subprocess.run(["xdotool", "search", "--all", "--onlyvisible", "--pid", str(proc.pid),
                             "--name", "Telegram MCP"], capture_output=True, text=True)
    found = result.stdout.split()
    for window in found:
        assert_owned(window)
    return found


def screenshot(label):
    window = wait_for(windows, "visible Wails window")[0]
    assert_owned(window)
    subprocess.run(["xdotool", "windowactivate", "--sync", window], check=True)
    time.sleep(0.2)
    path = evidence / (label + ".png")
    assert_owned(window)
    subprocess.run(["import", "-window", window, str(path)], check=True)
    assert path.stat().st_size > 1000
    return str(path)


def close_window(window):
    # A directed synthetic Alt+F4 is ignored by some window managers; sending
    # keys to global focus instead is unsafe on a shared screen. Address the
    # standard WM close request to this exact owned XID, just like its close
    # button, without keyboard input or a focus dependency.
    assert_owned(window)
    request_window_close(window)


def request_window_close(window):
    import ctypes as C

    class Message(C.Structure):
        _fields_ = [("type", C.c_int), ("serial", C.c_ulong),
                    ("send_event", C.c_int), ("display", C.c_void_p),
                    ("window", C.c_ulong), ("message_type", C.c_ulong),
                    ("format", C.c_int), ("data", C.c_long * 5)]

    class Event(C.Union):
        _fields_ = [("message", Message), ("pad", C.c_long * 24)]

    x11 = C.CDLL("libX11.so.6")
    x11.XOpenDisplay.argtypes, x11.XOpenDisplay.restype = [C.c_char_p], C.c_void_p
    x11.XDefaultRootWindow.argtypes, x11.XDefaultRootWindow.restype = [C.c_void_p], C.c_ulong
    x11.XInternAtom.argtypes, x11.XInternAtom.restype = [C.c_void_p, C.c_char_p, C.c_int], C.c_ulong
    x11.XSendEvent.argtypes = [C.c_void_p, C.c_ulong, C.c_int, C.c_long, C.POINTER(Event)]
    x11.XSync.argtypes = [C.c_void_p, C.c_int]
    x11.XCloseDisplay.argtypes = [C.c_void_p]
    display = x11.XOpenDisplay(None)
    assert display, "cannot open fixture X display"
    try:
        event = Event()
        event.message.type, event.message.format = 33, 32  # ClientMessage
        event.message.display, event.message.window = display, int(window)
        event.message.message_type = x11.XInternAtom(display, b"_NET_CLOSE_WINDOW", False)
        event.message.data[1] = 1  # Normal application source.
        assert_owned(window)
        assert x11.XSendEvent(display, x11.XDefaultRootWindow(display), False,
                             (1 << 20) | (1 << 19), C.byref(event)), "WM close request failed"
        x11.XSync(display, False)
    finally:
        x11.XCloseDisplay(display)


def rendered_text(label):
    image = screenshot(label)
    text = subprocess.run(["tesseract", image, "stdout", "--psm", "6"],
                          capture_output=True, text=True, check=True).stdout
    (evidence / (label + ".txt")).write_text(text)
    return text


def wait_rendered(label, *expected):
    def matches():
        text = rendered_text(label)
        return text if all(word.casefold() in text.casefold() for word in expected) else None
    text = wait_for(matches, "native rendered content: " + ", ".join(expected))
    report["screenshots"].append(str(evidence / (label + ".png")))
    report.setdefault("native_content", {})[label] = text
    return text


def menu_state():
    service, path = registered[-1]
    obj = bus.get_object(service, "/StatusNotifierMenu")
    menu = dbus.Interface(obj, "com.canonical.dbusmenu")
    _, layout = menu.GetLayout(0, -1, dbus.Array([], signature="s"))
    rows = []
    def walk(node):
        node_id, props, children = node
        if "label" in props:
            rows.append({"id": int(node_id), "label": str(props["label"])})
        for child in children:
            walk(child)
    walk(layout)
    item = bus.get_object(service, path)
    props = dbus.Interface(item, "org.freedesktop.DBus.Properties")
    title = str(props.Get("org.kde.StatusNotifierItem", "Title"))
    assert title.startswith("Telegram MCP"), title
    pix = props.Get("org.kde.StatusNotifierItem", "IconPixmap")
    icon = bytes(pix[0][2])
    return rows, hashlib.sha256(icon).hexdigest(), menu


report = {"engine": "fake", "port": port, "fixture": str(root), "result": "FAIL", "checks": {}, "screenshots": []}
log = (evidence / "gui-process.log").open("w")
hidden = "--hidden" in sys.argv[3:]
delayed = "--delay-startup" in sys.argv[3:]
assert not (hidden and delayed), "delayed-start regression requires a visible initial window"
startup_lock = None
if delayed:
    startup_lock = (root / "data" / "http-token.lock").open("w")
    os.chmod(startup_lock.name, 0o600)
    fcntl.flock(startup_lock, fcntl.LOCK_EX)
decoy = None
decoy_window = None
proc = subprocess.Popen([str(app)] + (["--hidden"] if hidden else []), env=env, stdout=log, stderr=subprocess.STDOUT)
try:
    if "--decoy" in sys.argv[3:]:
        decoy = subprocess.Popen([sys.executable, str(Path(__file__).with_name("gui_decoy.py"))],
                                 env=env, stdout=subprocess.PIPE, text=True)
        assert decoy.stdout is not None
        assert select.select([decoy.stdout], [], [], 10)[0], "decoy did not publish its XID"
        decoy_window = decoy.stdout.readline().strip()
        assert decoy_window.isdecimal(), "invalid decoy XID"
        assert decoy_window not in windows(), "title-collision decoy selected"
        report["decoy"] = {"pid": decoy.pid, "window": decoy_window}
    if delayed:
        # Capture and OCR only our PID's native window while initialization is
        # still blocked. Releasing a timer before navigation made this flaky.
        initial = wait_for(lambda: rendered_text("00-startup-waiting"), "initial native document")
        report["startup_before_unlock"] = initial
        assert any(word in initial.casefold() for word in ("starting", "carregando")), initial
        time.sleep(2)  # Keep initialization blocked through at least one retry.
        assert startup_lock is not None
        assert proc.poll() is None
        fcntl.flock(startup_lock, fcntl.LOCK_UN)
        startup_lock.close()
        startup_lock = None
        report["checks"]["delayed_token_lock_released"] = True
    wait_for(lambda: api("/api/status"), "fake HTTP daemon")
    wait_for(lambda: registered, "Wails tray registration")
    if hidden:
        time.sleep(0.5)
        assert not windows(), "--hidden unexpectedly showed a window"
        report["checks"]["hidden_login_start"] = True
        api("/api/app/open", {"path": "/"})
    wait_for(windows, "Wails panel")
    rows, connected_icon, menu = wait_for(lambda: menu_state(), "tray menu")
    assert any("Conectado" in r["label"] for r in rows), rows
    report["checks"]["connected"] = rows
    wait_rendered("01-connected-panel", "Seu Telegram", "Conectado")
    report["checks"]["initial_native_panel"] = True
    api("/api/app/open", {"path": "/conectar"})
    wait_rendered("01-connect-claude", "Claude Desktop")
    report["checks"]["claude_connection_preserved"] = True

    api("/api/account/disconnect", {})
    wait_for(lambda: any("Desconectado" in r["label"] for r in menu_state()[0]), "disconnected tray")
    rows, disconnected_icon, _ = menu_state()
    assert disconnected_icon != connected_icon
    report["checks"]["disconnected"] = rows
    api("/api/app/open", {"path": "/conta"})
    wait_rendered("02-disconnected-account", "Desconectado")

    window = windows()[0]
    close_window(window)
    wait_for(lambda: not windows(), "close hides window")
    assert proc.poll() is None
    assert api("/api/status")
    report["checks"]["close_keeps_daemon_alive"] = True
    rows, _, menu = menu_state()
    open_id = next(r["id"] for r in rows if r["label"].startswith("Abrir"))
    menu.Event(open_id, "clicked", dbus.Int32(0), dbus.UInt32(0))
    wait_for(windows, "tray reopens panel")
    wait_rendered("03-reopened-from-tray", "Conta", "Desconectado")
    report["checks"]["tray_reopens_window"] = True

    api("/api/account/connect", {})
    wait_for(lambda: any("Conectado" in r["label"] for r in menu_state()[0]), "reconnected tray")
    report["checks"]["reconnected"] = True
    api("/api/account/logout", {})
    wait_for(lambda: any("Entrar" in r["label"] for r in menu_state()[0]), "login-required tray")
    rows, login_icon, _ = menu_state()
    assert login_icon != connected_icon and login_icon != disconnected_icon
    report["checks"]["login_required"] = rows
    report["icon_sha256"] = {"connected": connected_icon, "disconnected": disconnected_icon, "login_required": login_icon}
    api("/api/app/open", {"path": "/conta"})
    wait_rendered("04-login-required", "Entrar")

    api("/api/configuracoes", {"autostart": True})
    entry = root / "config" / "autostart" / "com.bwoliveira.telegram-mcp.desktop"
    assert entry.is_file() and "--hidden" in entry.read_text()
    cfg = json.loads((root / "data" / "config.json").read_text())
    assert cfg["autostart"] is True
    report["checks"]["xdg_autostart_enabled_readback"] = True
    api("/api/configuracoes", {"autostart": False})
    assert not entry.exists()
    cfg = json.loads((root / "data" / "config.json").read_text())
    assert cfg["autostart"] is False
    report["checks"]["xdg_autostart_disabled_readback"] = True
    report["checks"]["original_blue_icon"] = "manual screenshot review required"
    if decoy is not None:
        assert decoy_window is not None
        assert decoy.poll() is None, "decoy was closed"
        owner = subprocess.run(["xdotool", "getwindowpid", decoy_window],
                               capture_output=True, text=True, check=True).stdout.strip()
        assert owner == str(decoy.pid), "decoy no longer owns its window"
        assert decoy_window not in windows(), "title-collision decoy selected"
        assert decoy.stdout is not None
        assert not select.select([decoy.stdout], [], [], 0)[0], "decoy received unexpected input"
        report["checks"]["decoy_untouched"] = True
    report["result"] = "PASS"
finally:
    if startup_lock is not None:
        fcntl.flock(startup_lock, fcntl.LOCK_UN)
        startup_lock.close()
    proc.terminate()
    try:
        proc.wait(timeout=8)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=5)
    log.close()
    if decoy is not None:
        decoy.terminate()
        decoy.wait(timeout=5)
        if decoy.stdout is not None:
            decoy.stdout.close()
    loop.quit()
    (evidence / "gui-smoke.json").write_text(json.dumps(report, indent=2) + "\n")
print(json.dumps(report, indent=2))
