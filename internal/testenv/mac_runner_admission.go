package testenv

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	macRunnerAdmissionPath    = "/tmp/tack-test/mac-runner-admission.json"
	macRunnerAdmissionVersion = 1
	macRunnerAdmissionLimit   = 4096
)

type macRunnerAdmission struct {
	Version         int    `json:"version"`
	RunnerID        string `json:"runner_id"`
	Name            string `json:"name"`
	Image           string `json:"image"`
	Root            string `json:"root"`
	Project         string `json:"project"`
	TemporaryVolume string `json:"temporary_volume"`
	HostDaemonID    string `json:"host_daemon_id"`
	RawDaemonID     string `json:"raw_daemon_id"`
	Architecture    string `json:"architecture"`
	DaemonVersion   string `json:"daemon_version"`
	RawSocketSource string `json:"raw_socket_source"`
}

func (execution *macRunnerExecution) admitRunner(ctx context.Context, inspection container.InspectResponse) error {
	if err := execution.verify(inspection); err != nil {
		return err
	}
	receipt := macRunnerAdmission{
		Version: macRunnerAdmissionVersion, RunnerID: inspection.ID, Name: execution.name,
		Image: inspection.Image, Root: execution.prepared.root, Project: execution.prepared.project.Name,
		TemporaryVolume: execution.temporaryVolumes[inspection.ID].Name,
		HostDaemonID:    execution.hostDaemon.ID, RawDaemonID: execution.rawDaemon.ID,
		Architecture: execution.hostDaemon.Architecture, DaemonVersion: execution.hostDaemon.Version,
		RawSocketSource: "",
	}
	for _, point := range inspection.Mounts {
		if point.Destination == "/var/run/docker.sock" {
			receipt.RawSocketSource = point.Source
		}
	}
	if receipt.RawSocketSource != "/var/run/docker.sock.raw" || receipt.HostDaemonID == "" || receipt.HostDaemonID != receipt.RawDaemonID {
		return errors.New("host admission requires inspected raw socket and equal daemon identities")
	}
	contents, err := json.Marshal(receipt)
	if err != nil {
		return macRunnerFailure("encode runner admission", err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "mac-runner-admission.json", Mode: 0o600, Size: int64(len(contents))}); err != nil {
		return macRunnerFailure("archive runner admission", err)
	}
	if _, err := writer.Write(contents); err != nil {
		return macRunnerFailure("write runner admission archive", err)
	}
	if err := writer.Close(); err != nil {
		return macRunnerFailure("close runner admission archive", err)
	}
	observation, cancel := context.WithTimeout(ctx, macRunnerObservation)
	defer cancel()
	_, err = execution.rawClient.CopyToContainer(observation, inspection.ID, client.CopyToContainerOptions{DestinationPath: "/tmp/tack-test", Content: &archive})
	execution.operation("runner admission write", inspection.ID, err)
	if err != nil {
		return macRunnerFailure("write admitted runner receipt "+inspection.ID, err)
	}
	writeMacRunnerJSON(execution, "host-admission.json", receipt)
	return nil
}

func verifyNativeMacAdmission(ctx context.Context, docker *client.Client, inspection container.InspectResponse) error {
	file, err := os.Open(macRunnerAdmissionPath)
	if err != nil {
		return macRunnerFailure("open required native runner admission", err)
	}
	contents, readErr := io.ReadAll(io.LimitReader(file, macRunnerAdmissionLimit+1))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return macRunnerFailure("read native runner admission", err)
	}
	if len(contents) > macRunnerAdmissionLimit {
		return errors.New("native runner admission exceeds 4096 bytes")
	}
	var receipt macRunnerAdmission
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return macRunnerFailure("decode native runner admission", err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return errors.New("native runner admission contains trailing data")
	}
	labels := inspection.Config.Labels
	if receipt.Version != macRunnerAdmissionVersion || receipt.RunnerID != inspection.ID || receipt.Name != strings.TrimPrefix(inspection.Name, "/") || receipt.Image != inspection.Image || receipt.Root != labels[macRunnerLabel+".root"] || receipt.Project != labels[macRunnerProjectLabel] {
		return errors.New("native runner admission does not match inspected runner identity")
	}
	if receipt.RawSocketSource != "/var/run/docker.sock.raw" || receipt.HostDaemonID == "" || receipt.HostDaemonID != receipt.RawDaemonID {
		return errors.New("native runner admission lacks verified raw socket and equal host daemon identities")
	}
	matchedVolume := false
	for _, point := range inspection.Mounts {
		if point.Destination == "/tmp/tack-test" && point.Name == receipt.TemporaryVolume && macRunnerAnonymousMount(inspection, point) {
			matchedVolume = true
		}
	}
	if !matchedVolume {
		return errors.New("native runner admission does not match its anonymous temporary volume")
	}
	info, err := macObserve(ctx, func(observation context.Context) (client.SystemInfoResult, error) {
		return docker.Info(observation, client.InfoOptions{})
	})
	if err != nil {
		return macRunnerFailure("inspect admitted runner daemon", err)
	}
	version, err := macObserve(ctx, func(observation context.Context) (client.ServerVersionResult, error) {
		return docker.ServerVersion(observation, client.ServerVersionOptions{})
	})
	if err != nil {
		return macRunnerFailure("inspect admitted runner daemon version", err)
	}
	if info.Info.ID != receipt.HostDaemonID || version.Arch != receipt.Architecture || version.Version != receipt.DaemonVersion {
		return fmt.Errorf("native runner admission differs from independently inspected daemon %s", info.Info.ID)
	}
	return nil
}
