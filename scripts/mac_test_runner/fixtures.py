from __future__ import annotations

import json
import re

from . import state
from .evidence import require
from .schemas import DockerEvent


def created_fixtures() -> list[str]:
    identities: list[str] = []
    process_ids: set[str] = set()
    logged_names: set[str] = set()
    for runtime in [
        state.OUTPUT / "runtime.log",
        state.OUTPUT / "owned-runner-logs.stdout",
        state.OUTPUT / "owned-runner-logs.stderr",
    ]:
        if runtime.exists():
            logged_names.update(
                re.findall(
                    r"tack-testenv-[a-z0-9-]+-\d+-[0-9a-f]{8}",
                    runtime.read_text(),
                )
            )
    unattributed: list[str] = []
    events = state.OUTPUT / "events.jsonl"
    if events.exists():
        for line in events.read_text().splitlines():
            event = DockerEvent.model_validate_json(line)
            if event.kind != "container" or event.action != "create":
                continue
            actor = event.actor
            attributes = actor.attributes
            match = re.fullmatch(
                r"tack-testenv-[a-z0-9-]+-(\d+)-[0-9a-f]{8}",
                attributes.get("name", ""),
            )
            if attributes.get(state.MANAGED) == "true":
                if match is None or attributes.get("name", "") not in logged_names:
                    unattributed.append(actor.identity)
                    continue
                process_ids.add(match.group(1))
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
