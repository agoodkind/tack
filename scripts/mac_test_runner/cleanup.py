from __future__ import annotations

import json

from pydantic import TypeAdapter

from . import state
from .create import read_refusals
from .evidence import capture, require
from .fixtures import created_fixtures
from .schemas import ContainerOwnership, DockerNetwork
from .volumes import owned_volumes, remove_volumes


def cleanup(
    outer: str,
    environment: dict[str, str],
    baseline: set[str],
    baseline_volumes: set[str],
    owned_network: str,
) -> None:
    fixtures = created_fixtures()
    owned = {*fixtures, outer}
    eligible: set[str] = set()
    (state.OUTPUT / "fixture-ids.json").write_text(
        json.dumps(fixtures, indent=2) + "\n"
    )
    for identity in [*fixtures, outer]:
        if not identity:
            continue
        require(identity not in baseline, "A preexisting container cannot be removed")
        present = capture(
            identity + "-presence",
            ["docker", "ps", "-aq", "--no-trunc", "--filter", "id=" + identity],
            environment,
        )
        if present.strip():
            current = ContainerOwnership.model_validate_json(
                capture(
                    identity + "-cleanup-state",
                    [
                        "docker",
                        "inspect",
                        identity,
                        "--format",
                        '{"Id":{{json .Id}},"Labels":{{json .Config.Labels}}}',
                    ],
                    environment,
                )
            )
            require(
                current.identity == identity,
                "Cleanup resolved a different container identity",
            )
            if identity == outer:
                require(
                    current.labels.get(state.LABEL) == state.NAME
                    and current.labels.get("com.docker.compose.project")
                    == state.PROJECT,
                    "Cleanup runner ownership changed",
                )
            else:
                require(
                    current.labels.get(state.MANAGED) == "true",
                    "Cleanup fixture ownership changed",
                )
            eligible.update(
                owned_volumes(identity, owned, baseline_volumes, environment)
            )
            arguments = ["docker", "rm", "-f"]
            arguments.append(identity)
            capture(identity + "-remove", arguments, environment)
    remaining = capture(
        "cleanup-inventory", ["docker", "ps", "-aq", "--no-trunc"], environment
    ).splitlines()
    require(not owned.intersection(remaining), "An owned container remains")
    require(baseline.issubset(remaining), "A preexisting container was removed")
    if owned_network:
        inspection = TypeAdapter(list[DockerNetwork]).validate_json(
            capture(
                "owned-network-terminal",
                ["docker", "network", "inspect", owned_network],
                environment,
            )
        )[0]
        require(
            not inspection.containers,
            "The newly created Compose network has another container",
        )
        capture(
            "owned-network-remove",
            ["docker", "network", "rm", owned_network],
            environment,
        )
        remaining_networks = capture(
            "remaining-networks",
            ["docker", "network", "ls", "-q", "--no-trunc"],
            environment,
        ).splitlines()
        require(
            owned_network not in remaining_networks,
            "The newly owned Compose network remains",
        )
    anonymous = state.OUTPUT / "anonymous-volume.txt"
    if anonymous.exists():
        volume_name = anonymous.read_text().strip()
        require(
            volume_name not in baseline_volumes
            and volume_name not in state.CACHE_NAMES.values(),
            "The temporary volume is preexisting or shared and cannot be removed",
        )
        eligible.add(volume_name)
    remove_volumes(eligible, baseline_volumes, environment)
    require_refused_absent(environment)
    for volume in state.CACHE_NAMES.values():
        capture(
            volume + "-preserved", ["docker", "volume", "inspect", volume], environment
        )
    require(
        (state.OUTPUT / "fixture-attribution.exit").read_text().strip() == "0",
        "Unattributed create events prevent a complete invocation cleanup claim",
    )
    (state.OUTPUT / "cleanup.exit").write_text("0\n")


def require_refused_absent(environment: dict[str, str]) -> None:
    refused = read_refusals()
    if not refused:
        return
    containers = set(
        capture(
            "refused-final-containers",
            ["docker", "ps", "-aq", "--no-trunc"],
            environment,
        ).splitlines()
    )
    volumes = set(
        capture(
            "refused-final-volumes",
            ["docker", "volume", "ls", "--format", "{{.Name}}"],
            environment,
        ).splitlines()
    )
    for record in refused:
        require(
            bool(record.volume),
            "A refused runner has no verified anonymous volume identity",
        )
        require(record.container not in containers, "A refused runner remains")
        require(record.volume not in volumes, "A refused runner volume remains")
