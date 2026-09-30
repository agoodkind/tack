from __future__ import annotations

import subprocess

from . import state
from .schemas import DockerInspect


def capture(name: str, arguments: list[str], environment: dict[str, str]) -> str:
    try:
        result = subprocess.run(
            arguments,
            cwd=state.ROOT,
            env=environment,
            capture_output=True,
            text=True,
            check=False,
            timeout=state.OBSERVATION_SECONDS,
        )
    except subprocess.TimeoutExpired:
        (state.OUTPUT / (name + ".exit")).write_text("124\n")
        (state.OUTPUT / (name + ".stderr")).write_text(
            "The command exceeded its thirty-second observation bound.\n"
        )
        raise
    (state.OUTPUT / (name + ".stdout")).write_text(result.stdout)
    (state.OUTPUT / (name + ".stderr")).write_text(result.stderr)
    (state.OUTPUT / (name + ".exit")).write_text(str(result.returncode) + "\n")
    if result.returncode != 0:
        raise RuntimeError(name + " failed; inspect its stderr artifact")
    return result.stdout


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def verify_outer(inspected: DockerInspect) -> None:
    require(
        inspected.image == state.IMAGE, "The runner image differs from the pinned image"
    )
    config = inspected.config
    require(
        config.working_dir == str(state.ROOT), "The runner working directory changed"
    )
    require(config.command == state.COMMAND, "The selected test command changed")
    require(
        config.entrypoint == ["/usr/local/go/bin/go"], "The runner entrypoint changed"
    )
    require(
        config.labels.get(state.LABEL) == state.NAME,
        "The runner invocation label is absent",
    )
    require(
        config.labels.get("com.docker.compose.project") == state.PROJECT,
        "The runner Compose project changed",
    )
    require(
        config.labels.get("com.docker.compose.service") == "tests",
        "The runner Compose service changed",
    )
    mounts = {mount.destination: mount for mount in inspected.mounts}
    require(
        set(mounts)
        == {
            str(state.ROOT),
            "/var/run/docker.sock",
            "/root/.cache/go-build",
            "/go/pkg/mod",
            "/tmp/tack-test",
        },
        "The runner mount set changed",
    )
    source = mounts[str(state.ROOT)]
    require(
        source.kind == "bind"
        and source.source in {str(state.ROOT), "/host_mnt" + str(state.ROOT)}
        and source.writable,
        "The source mount differs from the original writable path",
    )
    socket = mounts["/var/run/docker.sock"]
    require(
        socket.kind == "bind"
        and socket.source == "/var/run/docker.sock.raw"
        and socket.writable,
        "The guest socket mount differs from the approved mount",
    )
    for target, expected in state.CACHE_NAMES.items():
        mount = mounts[target]
        require(
            mount.kind == "volume" and mount.name == expected and mount.writable,
            "A named cache mount changed",
        )
    temporary = mounts["/tmp/tack-test"]
    require(
        temporary.kind == "volume"
        and temporary.writable
        and temporary.name not in set(state.CACHE_NAMES.values()),
        "The temporary anonymous volume changed",
    )
    if temporary.name is None:
        raise RuntimeError("The anonymous volume has no identity")
    (state.OUTPUT / "anonymous-volume.txt").write_text(temporary.name + "\n")
