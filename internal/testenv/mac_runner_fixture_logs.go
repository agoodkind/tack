package testenv

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type macRunnerFixtureLogMessage string

const (
	macRunnerEngineStarted  macRunnerFixtureLogMessage = "testenv.engine.started"
	macRunnerProbeStarted   macRunnerFixtureLogMessage = "testenv.shared.probe_started"
	macRunnerLedgerStarted  macRunnerFixtureLogMessage = "testenv.ledger_cluster.node_started"
	macRunnerClusterStarted macRunnerFixtureLogMessage = "testenv.cluster.started"
)

type macRunnerFixtureLog struct {
	Message   macRunnerFixtureLogMessage `json:"msg"`
	Container string                     `json:"container"`
	Cluster   string                     `json:"cluster"`
	Planned   int                        `json:"planned"`
}

var macRunnerLogField = regexp.MustCompile(`(?:^|\s)(msg|container|cluster|planned)=("(?:[^"\\]|\\.)*"|[^\s]+)`)

var macRunnerDefaultMessage = regexp.MustCompile(`(?:^|\s)(?:INFO|DEBUG) (testenv\.(?:engine\.started|shared\.probe_started|ledger_cluster\.node_started|cluster\.started))(?:\s|$)`)

func (execution *macRunnerExecution) fixtureLogs() (map[string]bool, error) {
	names := make(map[string]bool)
	if !execution.started {
		return names, nil
	}
	complete := false
	for _, stage := range execution.stages {
		if stage.Name == "attach completion" && stage.Error == "" {
			complete = true
		}
	}
	if !complete {
		return names, errors.New("fixture log attribution requires completed runtime output")
	}
	file, err := os.Open(filepath.Join(execution.prepared.options.EvidenceDirectory, "runtime.log"))
	if err != nil {
		return names, macRunnerFailure("read completed fixture logs", err)
	}
	defer func() { execution.evidenceErrors = append(execution.evidenceErrors, file.Close()) }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		record := decodeMacRunnerFixtureLog(scanner.Text())
		switch record.Message {
		case macRunnerEngineStarted, macRunnerProbeStarted, macRunnerLedgerStarted:
			if record.Container != "" {
				names[record.Container] = true
			}
		case macRunnerClusterStarted:
			if record.Cluster == "" || record.Planned < 1 {
				continue
			}
			names[record.Cluster+"-proxy"] = true
			for member := 1; member <= record.Planned; member++ {
				names[record.Cluster+"-"+strconv.Itoa(member)] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return names, macRunnerFailure("scan completed fixture logs", err)
	}
	return names, nil
}

func decodeMacRunnerFixtureLog(line string) macRunnerFixtureLog {
	var record macRunnerFixtureLog
	if strings.HasPrefix(line, "{") {
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return macRunnerFixtureLog{Message: "", Container: "", Cluster: "", Planned: 0}
		}
		return record
	}
	values := make(map[string]string)
	for _, field := range macRunnerLogField.FindAllStringSubmatch(line, -1) {
		value := field[2]
		if strings.HasPrefix(value, `"`) {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				continue
			}
			value = decoded
		}
		values[field[1]] = value
	}
	record.Message, record.Container, record.Cluster = macRunnerFixtureLogMessage(values["msg"]), values["container"], values["cluster"]
	if record.Message == "" {
		if message := macRunnerDefaultMessage.FindStringSubmatch(line); len(message) == 2 {
			record.Message = macRunnerFixtureLogMessage(message[1])
		}
	}
	record.Planned, _ = strconv.Atoi(values["planned"])
	return record
}
