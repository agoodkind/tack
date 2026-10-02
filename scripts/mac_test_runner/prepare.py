from __future__ import annotations

import hashlib
import os

from . import state
from .evidence import capture, require
from .schemas import (
    ComposeConfiguration,
    Invocation,
    Prepared,
    RunnerOverride,
    RunnerService,
)


def prepare() -> Prepared:
    require(
        os.uname().sysname == "Darwin",
        "This command requires local macOS Docker Desktop",
    )
    state.OUTPUT.mkdir(mode=0o700)
    state.EVIDENCE_CREATED = True
    (state.OUTPUT / "wrapper.pid").write_text(str(os.getpid()) + "\n")
    environment = dict(os.environ)
    environment["DOCKER_HOST"] = state.DEFAULT
    environment.pop("DOCKER_CONTEXT", None)
    environment["TACK_TEST_ROOT"] = str(state.ROOT)
    environment["TACK_SEARCH_INTEGRATION"] = "1"
    configuration = ComposeConfiguration.model_validate_json(
        capture(
            "compose-configuration",
            [
                "docker",
                "compose",
                "-f",
                "docker-compose.test.yml",
                "--profile",
                "runner",
                "config",
                "--format",
                "json",
            ],
            environment,
        )
    )
    state.PROJECT = configuration.name
    state.CACHE_NAMES["/root/.cache/go-build"] = configuration.volumes[
        "tack-test-go-cache"
    ].name
    state.CACHE_NAMES["/go/pkg/mod"] = configuration.volumes["tack-test-go-mod"].name
    override = RunnerOverride(
        services={
            "tests": RunnerService(
                image=state.IMAGE,
                container_name=state.NAME,
                entrypoint=["/usr/local/go/bin/go"],
                command=state.COMMAND,
                labels={state.LABEL: state.NAME},
                volumes=["/var/run/docker.sock.raw:/var/run/docker.sock"],
            )
        }
    )
    state.OVERRIDE.write_text(override.model_dump_json(indent=2) + "\n")
    invocation = Invocation(
        root=str(state.ROOT),
        image=state.IMAGE,
        command=state.COMMAND,
        name=state.NAME,
        project=state.PROJECT,
    )
    (state.OUTPUT / "invocation.json").write_text(invocation.model_dump_json() + "\n")
    revision = capture("revision", ["git", "rev-parse", "HEAD"], environment).strip()
    environment["TACK_TEST_SOURCE_REVISION"] = revision
    effective = capture(
        "effective-compose",
        [
            "docker",
            "compose",
            "-f",
            "docker-compose.test.yml",
            "-f",
            str(state.OVERRIDE),
            "--profile",
            "runner",
            "config",
        ],
        environment,
    )
    state.EFFECTIVE.write_text(effective)
    capture("dirty", ["git", "status", "--short"], environment)
    paths = capture(
        "source-paths",
        ["git", "ls-files", "--cached", "--others", "--exclude-standard"],
        environment,
    ).splitlines()
    hashes: list[str] = []
    for relative in sorted(set(paths)):
        path = state.ROOT / relative
        if path.is_relative_to(state.OUTPUT.resolve()):
            continue
        if path.is_file() and (
            path.suffix in {".go", ".sql", ".json", ".yml", ".yaml", ".py"}
            or path.name == "Makefile"
            or path.name.startswith("Dockerfile")
        ):
            hashes.append(
                hashlib.sha256(path.read_bytes()).hexdigest() + "  " + relative
            )
    (state.OUTPUT / "source.sha256").write_text("\n".join(hashes) + "\n")
    default = capture(
        "default-daemon",
        ["docker", "info", "--format", "{{.ID}} {{.Architecture}} {{.ServerVersion}}"],
        environment,
    )
    raw = capture(
        "raw-daemon",
        [
            "docker",
            "--host",
            state.RAW,
            "info",
            "--format",
            "{{.ID}} {{.Architecture}} {{.ServerVersion}}",
        ],
        environment,
    )
    require(default == raw, "The default and raw sockets identify different daemons")
    capture("pinned-image", ["docker", "image", "inspect", state.IMAGE], environment)
    baseline = set(
        capture(
            "baseline-containers", ["docker", "ps", "-aq", "--no-trunc"], environment
        ).splitlines()
    )
    baseline_networks = set(
        capture(
            "baseline-networks",
            ["docker", "network", "ls", "-q", "--no-trunc"],
            environment,
        ).splitlines()
    )
    baseline_volumes = set(
        capture(
            "baseline-volumes",
            ["docker", "volume", "ls", "--format", "{{.Name}}"],
            environment,
        ).splitlines()
    )
    service_collision = capture(
        "service-collision",
        [
            "docker",
            "ps",
            "-aq",
            "--filter",
            "label=com.docker.compose.project=" + state.PROJECT,
            "--filter",
            "label=com.docker.compose.service=tests",
        ],
        environment,
    )
    require(
        not service_collision.strip(),
        "The existing Compose project already has a tests service container",
    )
    require(
        not capture(
            "name-collision",
            ["docker", "ps", "-aq", "--filter", "name=^/" + state.NAME + "$"],
            environment,
        ).strip(),
        "The diagnostic runner name already exists",
    )
    return Prepared(
        environment=environment,
        baseline=baseline,
        baseline_networks=baseline_networks,
        baseline_volumes=baseline_volumes,
        hashes=hashes,
    )
