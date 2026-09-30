#!/usr/bin/env python3
from __future__ import annotations

import pathlib
import signal
import sys


def main() -> int:
    sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent.parent))
    from scripts.mac_test_runner.inputs import configure, inputs
    from scripts.mac_test_runner.lifecycle import entry, interrupt

    configure(inputs())
    signal.signal(signal.SIGINT, interrupt)
    signal.signal(signal.SIGTERM, interrupt)
    return entry()


if __name__ == "__main__":
    raise SystemExit(main())
