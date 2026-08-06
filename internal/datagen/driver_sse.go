package datagen

import (
	"bufio"
	"fmt"
	"strings"
)

func readLastSSEData(body []byte) ([]byte, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 64*1024), maxResponseBodyBytes)
	var eventData []string
	var payload string
	hasCompleteEvent := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(eventData) > 0 {
				payload = strings.Join(eventData, "\n")
				hasCompleteEvent = true
			}
			eventData = eventData[:0]
			continue
		}
		data, ok := sseDataLine(line)
		if ok {
			eventData = append(eventData, data)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !hasCompleteEvent {
		return nil, fmt.Errorf("SSE response contains no complete data event")
	}
	return []byte(payload), nil
}

func sseDataLine(line string) (string, bool) {
	if line == "data" {
		return "", true
	}
	if !strings.HasPrefix(line, "data:") {
		return "", false
	}
	value := strings.TrimPrefix(line, "data:")
	value = strings.TrimPrefix(value, " ")
	return value, true
}
