from __future__ import annotations

import json
import subprocess
import time

from pydantic import TypeAdapter

from . import state
from .create import create_verified_runner
from .evidence import capture, require
from .schemas import DockerInspect


def run_selected(environment: dict[str, str]) -> int:
    outer = create_verified_runner(environment)
    require(not state.INTERRUPTED, "The invocation was interrupted before startup")
    start = ["docker", "--host", state.RAW, "start", "--attach", outer]
    (state.OUTPUT / "start.command.json").write_text(json.dumps(start) + "\n")
    with (state.OUTPUT / "runtime.log").open("w") as output:
        launch_time = time.monotonic()
        process = subprocess.Popen(
            start, env=environment, stdout=output, stderr=subprocess.STDOUT, text=True
        )
        state.CHILDREN.append(process)
        (state.OUTPUT / "start.pid").write_text(str(process.pid) + "\n")
        while process.poll() is None and not state.INTERRUPTED:
            response = capture(
                "outer-current", ["docker", "inspect", outer], environment
            )
            inspected_state = TypeAdapter(list[DockerInspect]).validate_json(response)[
                0
            ]
            elapsed = time.monotonic() - launch_time
            require(
                elapsed < state.OPERATOR_SECONDS,
                "The operator bound expired; the selected Go timeout remains unchanged",
            )
            if inspected_state.state.status == "created":
                require(
                    elapsed < state.STARTUP_SECONDS,
                    "The runner remained Created beyond the thirty-second "
                    "startup bound",
                )
            with (state.OUTPUT / "states.jsonl").open("a") as states:
                states.write(
                    inspected_state.state.model_dump_json(by_alias=True) + "\n"
                )
            if inspected_state.state.running:
                capture("outer-processes", ["docker", "top", outer], environment)
            time.sleep(5)
        attached_exit = process.wait(timeout=10)
    (state.OUTPUT / "attach.exit").write_text(str(attached_exit) + "\n")
    final = TypeAdapter(list[DockerInspect]).validate_json(
        capture("outer-terminal", ["docker", "inspect", outer], environment)
    )[0]
    result = final.state.exit_code
    if state.INTERRUPTED:
        result = 130
    (state.OUTPUT / "runtime.exit").write_text(str(result) + "\n")
    require(
        final.state.status == "exited" and not final.state.error,
        "The runner did not exit normally",
    )
    require(attached_exit == result, "The attached client and runner exit codes differ")
    return result
