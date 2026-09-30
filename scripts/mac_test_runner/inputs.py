from __future__ import annotations

import argparse
import os
import pathlib
import re

from . import state
from .evidence import require
from .schemas import Inputs


def duration_seconds(value: str) -> float:
    factors = {
        "h": 3600,
        "m": 60,
        "s": 1,
        "ms": 0.001,
        "µs": 0.000001,
        "ns": 0.000000001,
    }
    parts = re.findall(r"(\d+(?:\.\d+)?)(h|ms|µs|ns|m|s)", value)
    require(
        bool(parts) and "".join(number + unit for number, unit in parts) == value,
        "The timeout must be a positive Go duration",
    )
    seconds = sum(float(number) * factors[unit] for number, unit in parts)
    require(seconds > 0, "The timeout must be positive")
    return seconds


def inputs() -> Inputs:
    parser = argparse.ArgumentParser(
        description="Run Tack integration tests on local Docker Desktop. "
        "The command does not build or pull the runner image."
    )
    parser.add_argument("--root", required=True, type=pathlib.Path)
    parser.add_argument(
        "--image",
        required=True,
        help="Existing immutable runner image ID, sha256 followed by 64 hex digits",
    )
    parser.add_argument(
        "--run",
        required=True,
        dest="selection",
        help="Exact Go test selection expression",
    )
    parser.add_argument("--count", required=True, type=int)
    parser.add_argument(
        "--timeout", required=True, help="Positive Go duration, such as 45m"
    )
    parser.add_argument(
        "--evidence-dir",
        required=True,
        type=pathlib.Path,
        help="New absolute evidence directory",
    )
    arguments = parser.parse_args()
    result = Inputs.model_validate(vars(arguments))
    require(
        result.root.is_absolute() and result.root.is_dir(),
        "The checkout root must be an existing absolute directory",
    )
    require(
        result.evidence_dir.is_absolute() and not result.evidence_dir.exists(),
        "The evidence directory must be a new absolute path",
    )
    require(
        re.fullmatch(r"sha256:[0-9a-f]{64}", result.image) is not None,
        "The runner image must be an immutable local image ID",
    )
    require(
        bool(result.selection) and result.count > 0,
        "The test selection must be nonempty and count must be positive",
    )
    duration_seconds(result.timeout)
    return result


def configure(settings: Inputs) -> None:
    state.ROOT = settings.root.resolve()
    state.OUTPUT = settings.evidence_dir
    state.OVERRIDE = state.OUTPUT / "compose.override.json"
    state.EFFECTIVE = state.OUTPUT / "compose.effective.yml"
    state.IMAGE = settings.image
    state.NAME = "tack-mac-runner-" + os.urandom(12).hex()
    state.COMMAND = [
        "test",
        "-tags=fdb",
        "./internal/test/integration",
        "-run",
        settings.selection,
        "-count=" + str(settings.count),
        "-timeout=" + settings.timeout,
        "-v",
    ]
    state.OPERATOR_SECONDS = duration_seconds(settings.timeout) + 300
