from __future__ import annotations

import hashlib

from . import state
from .evidence import require


def verify_sources(hashes: list[str]) -> None:
    try:
        for line in hashes:
            expected, relative = line.split("  ", 1)
            require(
                hashlib.sha256((state.ROOT / relative).read_bytes()).hexdigest()
                == expected,
                "Runtime source hash changed: " + relative,
            )
    except Exception as error:
        (state.OUTPUT / "source-hash-verification.exit").write_text("1\n")
        (state.OUTPUT / "source-hash-verification.error").write_text(str(error) + "\n")
        raise
    (state.OUTPUT / "source-hash-verification.exit").write_text("0\n")
