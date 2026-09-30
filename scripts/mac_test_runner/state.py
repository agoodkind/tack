from __future__ import annotations

import pathlib
import subprocess

ROOT: pathlib.Path
OUTPUT: pathlib.Path
OVERRIDE: pathlib.Path
EFFECTIVE: pathlib.Path
IMAGE: str
NAME: str
PROJECT: str
COMMAND: list[str]
RAW = "unix://" + str(
    pathlib.Path.home() / "Library/Containers/com.docker.docker/Data/docker.raw.sock"
)
DEFAULT = "unix://" + str(pathlib.Path.home() / ".docker/run/docker.sock")
CACHE_NAMES: dict[str, str] = {}
LABEL = "io.goodkind.tack.mac-runner"
MANAGED = "io.goodkind.tack.testenv"
CHILDREN: list[subprocess.Popen[str]] = []
INTERRUPTED = False
EVIDENCE_CREATED = False
OBSERVATION_SECONDS = 30
STARTUP_SECONDS = 30
OPERATOR_SECONDS: float
