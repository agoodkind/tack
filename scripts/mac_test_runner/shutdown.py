from __future__ import annotations

import subprocess
from typing import TextIO

from pydantic import TypeAdapter

from . import state
from .cleanup import cleanup
from .evidence import capture, require
from .schemas import DockerInspect, DockerNetwork, Prepared


def shutdown(prepared: Prepared, events_output: TextIO, events_error: TextIO) -> None:
    environment = prepared.environment
    baseline = prepared.baseline
    baseline_networks = prepared.baseline_networks
    baseline_volumes = prepared.baseline_volumes
    outer = ""
    owned_network = ""
    try:
        outer_file = state.OUTPUT / "outer-id.txt"
        if outer_file.exists():
            outer = outer_file.read_text().strip()
        network_file = state.OUTPUT / "owned-network.id"
        if network_file.exists():
            owned_network = network_file.read_text().strip()
        if not outer:
            partial = capture(
                "partial-create-id",
                [
                    "docker",
                    "ps",
                    "-aq",
                    "--no-trunc",
                    "--filter",
                    "name=^/" + state.NAME + "$",
                    "--filter",
                    "label=" + state.LABEL + "=" + state.NAME,
                ],
                environment,
            ).splitlines()
            require(
                len(partial) <= 1,
                "Partial creation returned multiple runner identities",
            )
            if partial:
                require(
                    partial[0] not in baseline,
                    "Partial creation matched a preexisting container",
                )
                outer = partial[0]
                recovered = TypeAdapter(list[DockerInspect]).validate_json(
                    capture(
                        "partial-created", ["docker", "inspect", outer], environment
                    )
                )[0]
                require(
                    recovered.config.labels.get(state.LABEL) == state.NAME,
                    "Partial creation has unexpected ownership",
                )
                require(
                    recovered.config.labels.get("com.docker.compose.project")
                    == state.PROJECT,
                    "Partial creation has an unexpected project",
                )
                for mount in recovered.mounts:
                    if mount.destination == "/tmp/tack-test" and mount.name is not None:
                        (state.OUTPUT / "anonymous-volume.txt").write_text(
                            mount.name + "\n"
                        )
        if outer:
            require(outer not in baseline, "A preexisting runner cannot be stopped")
            owned_state = TypeAdapter(list[DockerInspect]).validate_json(
                capture("owned-before-stop", ["docker", "inspect", outer], environment)
            )[0]
            require(
                owned_state.identity == outer, "Runner stop resolved another identity"
            )
            require(
                owned_state.config.labels.get(state.LABEL) == state.NAME,
                "Runner stop has an unexpected invocation label",
            )
            require(
                owned_state.config.labels.get("com.docker.compose.project")
                == state.PROJECT,
                "Runner stop has an unexpected project",
            )
            require(
                owned_state.config.labels.get("com.docker.compose.service") == "tests",
                "Runner stop has an unexpected service",
            )
            if owned_state.state.running:
                capture(
                    "owned-runner-stop",
                    ["docker", "stop", "--time", "10", outer],
                    environment,
                )
            stopped = TypeAdapter(list[DockerInspect]).validate_json(
                capture("owned-after-stop", ["docker", "inspect", outer], environment)
            )[0]
            require(
                not stopped.state.running,
                "The owned runner remains active before fixture cleanup",
            )
            capture("owned-runner-logs", ["docker", "logs", outer], environment)
    finally:
        for child in state.CHILDREN:
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=10)
        events_output.close()
        events_error.close()
    if not owned_network:
        candidates = capture(
            "partial-network-ids",
            [
                "docker",
                "network",
                "ls",
                "-q",
                "--no-trunc",
                "--filter",
                "name=" + state.PROJECT + "_default",
            ],
            environment,
        ).splitlines()
        for identity in candidates:
            if identity in baseline_networks:
                continue
            network = TypeAdapter(list[DockerNetwork]).validate_json(
                capture(
                    "partial-network",
                    ["docker", "network", "inspect", identity],
                    environment,
                )
            )[0]
            if (
                network.name == state.PROJECT + "_default"
                and network.labels.get("com.docker.compose.project") == state.PROJECT
            ):
                require(
                    not owned_network, "Partial creation has multiple owned networks"
                )
                owned_network = identity
    cleanup(outer, environment, baseline, baseline_volumes, owned_network)
