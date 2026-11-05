package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/moby/moby/client"
)

const macRunnerEventEvidenceLimit = 16 << 20

var errMacRunnerStreamTimeout = errors.New("stream completion exceeded 10 seconds")

func (execution *macRunnerExecution) observeEvents(ctx context.Context) error {
	var err error
	execution.eventsFile, err = execution.open("events.jsonl")
	if err != nil {
		return err
	}
	execution.eventsErrorFile, err = execution.open("events.stderr")
	if err != nil {
		return err
	}
	observation, cancel := context.WithCancel(ctx)
	execution.eventCancel = cancel
	timer := time.AfterFunc(macRunnerObservation, cancel)
	stream := execution.eventClient.Events(observation, client.EventsListOptions{
		Since: execution.startedAt.Format(time.RFC3339Nano), Filters: client.Filters{}.Add("type", "container", "network", "volume"),
	})
	timer.Stop()
	if err := observation.Err(); err != nil {
		return macRunnerFailure("subscribe to container events within 30 seconds", err)
	}
	select {
	case err := <-stream.Err:
		return macRunnerFailure("subscribe to container events", err)
	default:
	}
	completed := make(chan error, 1)
	execution.eventDone = completed
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("container event observer panic: %v", recovered)
				slog.Error("testenv.run_mac.events_failed", slog.String("err", err.Error()))
				completed <- err
			}
		}()
		completed <- execution.recordEvents(observation, stream)
	}()
	return nil
}

func (execution *macRunnerExecution) recordEvents(ctx context.Context, stream client.EventsResult) error {
	var evidenceBytes int
	for {
		select {
		case event, open := <-stream.Messages:
			if !open {
				return errors.New("container event messages closed unexpectedly")
			}
			candidate, err := execution.recordFixtureCreate(ctx, event)
			if err != nil {
				return err
			}
			execution.eventSnapshot.Lock()
			runnerID := execution.eventSnapshot.runnerID
			execution.eventSnapshot.Unlock()
			if !candidate && event.Actor.Attributes[macRunnerLabel] != execution.name && (runnerID == "" || event.Actor.Attributes[runnerLabel] != runnerID) {
				continue
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return macRunnerFailure("encode container event", err)
			}
			if evidenceBytes+len(encoded)+1 > macRunnerEventEvidenceLimit {
				return errors.New("container event evidence exceeds 16 MiB")
			}
			evidenceBytes += len(encoded) + 1
			if _, err := execution.eventsFile.Write(append(encoded, '\n')); err != nil {
				return macRunnerFailure("write container event", err)
			}
		case err := <-stream.Err:
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return nil
			}
			if err == nil {
				err = errors.New("container event stream closed unexpectedly")
			}
			_, writeErr := fmt.Fprintln(execution.eventsErrorFile, err)
			return macRunnerErrors("observe container events", macRunnerFailure("observe container events", err), writeErr)
		}
	}
}

func macRunnerAwait(done <-chan error, operation string) error {
	timer := time.NewTimer(macRunnerStreamWait)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return macRunnerFailure(operation, errMacRunnerStreamTimeout)
	}
}
