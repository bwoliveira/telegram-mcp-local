#!/usr/bin/env python3
"""Run the branding guard without changing the checkout."""
import runpy
from pathlib import Path
runpy.run_path(str(Path(__file__).with_name("brand_guard.py")), run_name="__main__")
