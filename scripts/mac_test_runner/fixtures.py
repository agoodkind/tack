from __future__ import annotations

import json
import re

from . import state
from .evidence import require
from .schemas import DockerEvent

# The label testenv sets to the creating test process ID on every engine.
PROCESS_LABEL = state.MANAGED + ".process"
GENERATED_NAME = r"tack-testenv-[a-z0-9-]+-\d+-[0-9a-f]{8}"
GENERATED_MEMBER = re.compile(
    r"(tack-testenv-[a-z0-9-]+-(\d+)-[0-9a-f]{8})(?:-(?:[1-9]\d*|proxy))?"
)
ENGINE_STARTED = re.compile(r"testenv\.engine\.started container=(\S+)")
PROCESS_ID = re.compile(r"[0-9]+")


def logged_container_names() -> tuple[set[str], set[str]]:
    """Return the generated names and the started engine names in the logs."""
    generated: set[str] = set()
    started: set[str] = set()
    for runtime in [
        state.OUTPUT / "runtime.log",
        state.OUTPUT / "owned-runner-logs.stdout",
        state.OUTPUT / "owned-runner-logs.stderr",
    ]:
        if runtime.exists():
            text = runtime.read_text()
            generated.update(re.findall(GENERATED_NAME, text))
            started.update(ENGINE_STARTED.findall(text))
    return generated, started


def attributed_process(
    attributes: dict[str, str], generated: set[str], started: set[str]
) -> str | None:
    """Return the test process ID of a managed container, or None when no log
    and label prove which process created it."""
    name = attributes.get("name", "")
    match = GENERATED_MEMBER.fullmatch(name)
    if match is not None:
        if match.group(1) in generated:
            return match.group(2)
        return None
    process = attributes.get(PROCESS_LABEL, "")
    if PROCESS_ID.fullmatch(process) is not None and name in started:
        return process
    return None


def created_fixtures() -> list[str]:
    identities: list[str] = []
    process_ids: set[str] = set()
    generated, started = logged_container_names()
    unattributed: list[str] = []
    events = state.OUTPUT / "events.jsonl"
    if events.exists():
        for line in events.read_text().splitlines():
            event = DockerEvent.model_validate_json(line)
            if event.kind != "container" or event.action != "create":
                continue
            actor = event.actor
            if actor.attributes.get(state.MANAGED) != "true":
                continue
            process = attributed_process(actor.attributes, generated, started)
            if process is None:
                unattributed.append(actor.identity)
                continue
            process_ids.add(process)
            identities.append(actor.identity)
    require(
        len(process_ids) <= 1,
        "Fixture create events include multiple test process identities",
    )
    (state.OUTPUT / "inner-process-ids.json").write_text(
        json.dumps(sorted(process_ids)) + "\n"
    )
    (state.OUTPUT / "unattributed-preserved-ids.json").write_text(
        json.dumps(unattributed) + "\n"
    )
    if unattributed:
        (state.OUTPUT / "fixture-attribution.exit").write_text("1\n")
        (state.OUTPUT / "fixture-attribution.status").write_text(
            "Unmatched create events remain preserved. "
            "Complete invocation fixture cleanup is unproved.\n"
        )
    else:
        (state.OUTPUT / "fixture-attribution.exit").write_text("0\n")
    return sorted(set(identities))
