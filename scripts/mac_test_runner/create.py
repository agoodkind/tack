from __future__ import annotations

import json

from pydantic import BaseModel, TypeAdapter

from . import state
from .evidence import SocketMountRefusedError, capture, require, verify_outer
from .schemas import ContainerOwnership, DockerInspect, DockerNetwork

REFUSED_RUNNERS = "refused-runners.json"
CREATE_SUMMARY = "runner-create-summary.json"
SOCKET_TARGET = "/var/run/docker.sock"
TEMPORARY_TARGET = "/tmp/tack-test"
OWNERSHIP_FORMAT = '{"Id":{{json .Id}},"Labels":{{json .Config.Labels}}}'
LIST_CONTAINERS = ["docker", "ps", "-aq", "--no-trunc"]
CREATE_FLAGS = ["--profile", "runner", "create", "--no-build", "--pull", "never"]
MISSING_VOLUME = "The refused runner has no verified anonymous volume identity"


class RefusedRunner(BaseModel):
    attempt: int
    container: str
    socket_source: str
    volume: str
    message: str


class CreateSummary(BaseModel):
    create_attempts: int
    socket_refusals: int
    accepted: bool


def attempt_name(base: str, attempt: int) -> str:
    if attempt == 1:
        return base
    return base + "-" + str(attempt)


def create_verified_runner(environment: dict[str, str]) -> str:
    refused: list[RefusedRunner] = []
    write_refusals(refused)
    for attempt in range(1, state.CREATE_ATTEMPTS + 1):
        require(not state.INTERRUPTED, "The invocation was interrupted before creation")
        outer = create_runner(attempt, environment)
        if attempt == 1:
            record_compose_network(environment)
        inspected = TypeAdapter(list[DockerInspect]).validate_json(
            capture(
                attempt_name("outer-created", attempt),
                ["docker", "inspect", outer],
                environment,
            )
        )[0]
        require(
            inspected.identity == outer, "Runner inspection resolved another identity"
        )
        try:
            verify_outer(inspected)
        except SocketMountRefusedError as refusal:
            record = refusal_record(attempt, inspected, refusal)
            refused.append(record)
            write_refusals(refused)
            write_summary(attempt, len(refused), accepted=False)
            if not record.volume:
                write_incomplete(record)
                raise RuntimeError(MISSING_VOLUME) from refusal
            if attempt == state.CREATE_ATTEMPTS:
                raise
            remove_refused(record, environment)
            continue
        write_summary(attempt, len(refused), accepted=True)
        return outer
    raise RuntimeError("The runner create loop ended without a result")


def create_runner(attempt: int, environment: dict[str, str]) -> str:
    create = ["docker", "compose", "-f", str(state.EFFECTIVE), *CREATE_FLAGS, "tests"]
    name = attempt_name("create", attempt)
    (state.OUTPUT / (name + ".command.json")).write_text(json.dumps(create) + "\n")
    capture(name, create, environment)
    outer = capture(
        attempt_name("outer-id", attempt),
        ["docker", "inspect", state.NAME, "--format", "{{.Id}}"],
        environment,
    ).strip()
    (state.OUTPUT / "outer-id.txt").write_text(outer + "\n")
    return outer


def record_compose_network(environment: dict[str, str]) -> None:
    compose_network = TypeAdapter(list[DockerNetwork]).validate_json(
        capture(
            "compose-network-created",
            ["docker", "network", "inspect", state.PROJECT + "_default"],
            environment,
        )
    )[0]
    baseline_networks = set(
        (state.OUTPUT / "baseline-networks.stdout").read_text().splitlines()
    )
    if compose_network.identity in baseline_networks:
        return
    require(
        compose_network.labels.get("com.docker.compose.project") == state.PROJECT,
        "The new network has unexpected ownership",
    )
    (state.OUTPUT / "owned-network.id").write_text(compose_network.identity + "\n")


def refusal_record(
    attempt: int, inspected: DockerInspect, refusal: SocketMountRefusedError
) -> RefusedRunner:
    socket_source = ""
    volume = ""
    for mount in inspected.mounts:
        if mount.destination == SOCKET_TARGET:
            socket_source = mount.source
        if mount.destination == TEMPORARY_TARGET and mount.kind == "volume":
            volume = mount.name or ""
    record = RefusedRunner(
        attempt=attempt,
        container=inspected.identity,
        socket_source=socket_source,
        volume=volume,
        message=str(refusal),
    )
    refusal_path = state.OUTPUT / (attempt_name("refusal", attempt) + ".json")
    refusal_path.write_text(record.model_dump_json(indent=2) + "\n")
    return record


def remove_refused(record: RefusedRunner, environment: dict[str, str]) -> None:
    name = attempt_name("refused", record.attempt)
    ownership = ContainerOwnership.model_validate_json(
        capture(
            name + "-ownership",
            ["docker", "inspect", record.container, "--format", OWNERSHIP_FORMAT],
            environment,
        )
    )
    require(
        ownership.identity == record.container
        and ownership.labels.get(state.LABEL) == state.NAME
        and ownership.labels.get("com.docker.compose.project") == state.PROJECT,
        "Refused runner ownership changed",
    )
    capture(name + "-remove", ["docker", "rm", "-f", record.container], environment)
    require(
        not preexisting_or_shared(record.volume),
        "The refused runner volume is preexisting or shared",
    )
    volume_filter = [*LIST_CONTAINERS, "--filter", "volume=" + record.volume]
    consumers = capture(name + "-volume-consumers", volume_filter, environment)
    require(not consumers.strip(), "The refused runner volume has another consumer")
    volume_remove = ["docker", "volume", "rm", record.volume]
    capture(name + "-volume-remove", volume_remove, environment)
    container_filter = [*LIST_CONTAINERS, "--filter", "id=" + record.container]
    remaining = capture(name + "-absence", container_filter, environment)
    require(not remaining.strip(), "The refused runner remains")


def preexisting_or_shared(volume: str) -> bool:
    baseline_file = state.OUTPUT / "baseline-volumes.stdout"
    baseline = set(baseline_file.read_text().splitlines())
    return volume in baseline or volume in state.CACHE_NAMES.values()


def write_incomplete(record: RefusedRunner) -> None:
    inspect_name = attempt_name("outer-created", record.attempt) + ".stdout"
    inspect_text = (state.OUTPUT / inspect_name).read_text()
    (state.OUTPUT / "incomplete-cleanup.txt").write_text(
        MISSING_VOLUME + "\ncontainer=" + record.container + "\n"
    )
    (state.OUTPUT / "incomplete-cleanup-inspect.json").write_text(inspect_text)


def write_refusals(refused: list[RefusedRunner]) -> None:
    payload = TypeAdapter(list[RefusedRunner]).dump_json(refused, indent=2)
    (state.OUTPUT / REFUSED_RUNNERS).write_bytes(payload + b"\n")


def write_summary(attempts: int, refusals: int, *, accepted: bool) -> None:
    summary = CreateSummary(
        create_attempts=attempts, socket_refusals=refusals, accepted=accepted
    )
    (state.OUTPUT / CREATE_SUMMARY).write_text(summary.model_dump_json(indent=2) + "\n")


def read_refusals() -> list[RefusedRunner]:
    path = state.OUTPUT / REFUSED_RUNNERS
    if not path.exists():
        return []
    return TypeAdapter(list[RefusedRunner]).validate_json(path.read_text())
