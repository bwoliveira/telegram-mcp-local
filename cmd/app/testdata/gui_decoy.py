#!/usr/bin/env python3
"""Owned title-collision fixture; stdout records XID and any unexpected input."""
import gi

gi.require_version("Gtk", "3.0")
gi.require_version("Gdk", "3.0")
gi.require_version("GdkX11", "3.0")
from gi.repository import Gdk, GdkX11, Gtk

window = Gtk.Window(title="Telegram MCP isolation decoy")
window.add(Gtk.Label(label="PRIVATE TEST DECOY - MUST REMAIN UNTOUCHED"))
window.add_events(Gdk.EventMask.KEY_PRESS_MASK | Gdk.EventMask.BUTTON_PRESS_MASK)
for event in ("key-press-event", "button-press-event", "delete-event"):
    window.connect(event, lambda *args, name=event: print(name, flush=True))
window.connect("destroy", Gtk.main_quit)
window.show_all()
print(window.get_window().get_xid(), flush=True)
Gtk.main()
