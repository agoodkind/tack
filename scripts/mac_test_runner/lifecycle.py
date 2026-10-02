from __future__ import annotations

import subprocess
import time
from types import FrameType

from . import state
from .prepare import prepare
from .runner import run_selected
from .shutdown import shutdown
from .source import verify_sources


def main() -> int:
    prepared = prepare()
    environment = prepared.environment
    events_output = (state.OUTPUT / "events.jsonl").open("w")
    events_error = (state.OUTPUT / "events.stderr").open("w")
    events = subprocess.Popen(
        [
            "docker",
            "events",
            "--since",
            str(int(time.time())),
            "--filter",
            "type=container",
            "--format",
            "{{json .}}",
        ],
        env=environment,
        stdout=events_output,
        stderr=events_error,
        text=True,
    )
    state.CHILDREN.append(events)
    (state.OUTPUT / "events.pid").write_text(str(events.pid) + "\n")
    try:
        return run_selected(environment)
    except Exception as error:
        (state.OUTPUT / "primary.error").write_text(str(error) + "\n")
        (state.OUTPUT / "primary.exit").write_text("1\n")
        raise
    finally:
        try:
            shutdown(prepared, events_output, events_error)
        finally:
            verify_sources(prepared.hashes)


def interrupt(signum: int, frame: FrameType | None) -> None:
    state.INTERRUPTED = True


def entry() -> int:
    try:
        result = main()
    except Exception as error:  # noqa: BLE001 - record any wrapper failure
        if state.EVIDENCE_CREATED:
            (state.OUTPUT / "wrapper.error").write_text(str(error) + "\n")
            if not (state.OUTPUT / "cleanup.exit").exists():
                (state.OUTPUT / "cleanup.exit").write_text("1\n")
        result = 1
    if state.INTERRUPTED:
        result = 130
    if state.EVIDENCE_CREATED:
        (state.OUTPUT / "wrapper.exit").write_text(str(result) + "\n")
        if not (state.OUTPUT / "primary.exit").exists():
            (state.OUTPUT / "primary.exit").write_text(str(result) + "\n")
    return result
