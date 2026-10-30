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
	stream := execution.defaultClient.Events(observation, client.EventsListOptions{
		Since: execution.startedAt.Format(time.RFC3339Nano), Filters: client.Filters{}.Add("type", "container", "network"),
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
	encoder := json.NewEncoder(execution.eventsFile)
	for {
		select {
		case event, open := <-stream.Messages:
			if !open {
				return errors.New("container event messages closed unexpectedly")
			}
			if err := encoder.Encode(event); err != nil {
				return macRunnerFailure("write container event", err)
			}
			execution.recordFixtureCreate(ctx, event)
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
