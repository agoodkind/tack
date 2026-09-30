from __future__ import annotations

import pathlib

from pydantic import BaseModel, ConfigDict, Field


class Inputs(BaseModel):
    root: pathlib.Path
    image: str
    selection: str
    count: int
    timeout: str
    evidence_dir: pathlib.Path


class ComposeVolume(BaseModel):
    name: str


class ComposeConfiguration(BaseModel):
    name: str
    volumes: dict[str, ComposeVolume]


class RunnerService(BaseModel):
    image: str
    container_name: str
    entrypoint: list[str]
    command: list[str]
    labels: dict[str, str]
    volumes: list[str]
    pull_policy: str = "never"


class RunnerOverride(BaseModel):
    services: dict[str, RunnerService]


class Invocation(BaseModel):
    root: str
    image: str
    command: list[str]
    name: str
    project: str


class DockerState(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    status: str = Field(alias="Status")
    running: bool = Field(alias="Running")
    pid: int = Field(alias="Pid")
    exit_code: int = Field(alias="ExitCode")
    error: str = Field(alias="Error")


class DockerConfig(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    working_dir: str = Field(alias="WorkingDir")
    command: list[str] = Field(alias="Cmd")
    entrypoint: list[str] = Field(alias="Entrypoint")
    labels: dict[str, str] = Field(alias="Labels")


class DockerMount(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    kind: str = Field(alias="Type")
    source: str = Field(alias="Source")
    destination: str = Field(alias="Destination")
    writable: bool = Field(alias="RW")
    name: str | None = Field(default=None, alias="Name")


class DockerInspect(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    identity: str = Field(alias="Id")
    image: str = Field(alias="Image")
    state: DockerState = Field(alias="State")
    config: DockerConfig = Field(alias="Config")
    mounts: list[DockerMount] = Field(alias="Mounts")


class ContainerOwnership(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    identity: str = Field(alias="Id")
    labels: dict[str, str] = Field(alias="Labels")


class DockerActor(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    identity: str = Field(alias="ID")
    attributes: dict[str, str] = Field(alias="Attributes")


class DockerEvent(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    kind: str = Field(alias="Type")
    action: str = Field(alias="Action")
    actor: DockerActor = Field(alias="Actor")


class DockerEndpoint(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    name: str = Field(alias="Name")


class DockerNetwork(BaseModel):
    model_config = ConfigDict(populate_by_name=True)
    identity: str = Field(alias="Id")
    name: str = Field(alias="Name")
    labels: dict[str, str] = Field(alias="Labels")
    containers: dict[str, DockerEndpoint] = Field(alias="Containers")


class Prepared(BaseModel):
    environment: dict[str, str]
    baseline: set[str]
    baseline_networks: set[str]
    baseline_volumes: set[str]
    hashes: list[str]
