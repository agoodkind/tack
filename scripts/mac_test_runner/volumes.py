from __future__ import annotations

from pydantic import TypeAdapter

from . import state
from .evidence import capture, require
from .schemas import DockerMount


def owned_volumes(
    identity: str, owned: set[str], baseline: set[str], environment: dict[str, str]
) -> set[str]:
    mounts = TypeAdapter(list[DockerMount]).validate_json(
        capture(
            identity + "-cleanup-mounts",
            ["docker", "inspect", identity, "--format", "{{json .Mounts}}"],
            environment,
        )
    )
    eligible: set[str] = set()
    for mount in mounts:
        if mount.kind != "volume" or mount.name is None:
            continue
        if mount.name in baseline or mount.name in state.CACHE_NAMES.values():
            continue
        consumers = set(
            capture(
                mount.name + "-consumers",
                [
                    "docker",
                    "ps",
                    "-aq",
                    "--no-trunc",
                    "--filter",
                    "volume=" + mount.name,
                ],
                environment,
            ).splitlines()
        )
        if consumers and consumers.issubset(owned):
            eligible.add(mount.name)
    return eligible


def remove_volumes(
    eligible: set[str], baseline: set[str], environment: dict[str, str]
) -> None:
    for name in sorted(eligible):
        require(
            name not in baseline and name not in state.CACHE_NAMES.values(),
            "A preexisting or named cache volume cannot be removed",
        )
        consumers = capture(
            name + "-final-consumers",
            ["docker", "ps", "-aq", "--no-trunc", "--filter", "volume=" + name],
            environment,
        )
        require(
            not consumers.strip(),
            "An owned volume has another consumer and cannot be removed",
        )
        volumes = capture(
            name + "-presence",
            ["docker", "volume", "ls", "--format", "{{.Name}}"],
            environment,
        ).splitlines()
        if name in volumes:
            capture(name + "-remove", ["docker", "volume", "rm", name], environment)
    remaining = set(
        capture(
            "final-volumes",
            ["docker", "volume", "ls", "--format", "{{.Name}}"],
            environment,
        ).splitlines()
    )
    require(not eligible.intersection(remaining), "An owned volume remains")
    require(baseline.issubset(remaining), "A preexisting volume was removed")
