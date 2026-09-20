package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// probeQueue opens a bounded client, pings it, and reads cluster metadata.
// The bar is a response listing three brokers. A refused connection or a
// shorter list is ordinary while the quorum forms. Either reason comes back
// as text for the deadline error in waitForQueue.
func probeQueue(ctx context.Context, bootstrap string) error {
	probeCtx, cancel := context.WithTimeout(ctx, queueProbeTimeout)
	defer cancel()
	probeClient, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(bootstrap, ",")...),
		kgo.ClientID("tack-testenv-queue-probe"),
	)
	if err != nil {
		slog.DebugContext(ctx, "testenv.queue.probe", slog.String("err", err.Error()))
		return errors.New("client: " + err.Error())
	}
	defer probeClient.Close()
	if err := probeClient.Ping(probeCtx); err != nil {
		slog.DebugContext(ctx, "testenv.queue.probe", slog.String("err", err.Error()))
		return errors.New("ping: " + err.Error())
	}
	req := kmsg.NewPtrMetadataRequest()
	req.Topics = []kmsg.MetadataRequestTopic{}
	resp, err := req.RequestWith(probeCtx, probeClient)
	if err != nil {
		slog.DebugContext(ctx, "testenv.queue.probe", slog.String("err", err.Error()))
		return errors.New("metadata: " + err.Error())
	}
	if len(resp.Brokers) != queueBrokerCount {
		return fmt.Errorf("the cluster reports %d brokers, not %d", len(resp.Brokers), queueBrokerCount)
	}
	return nil
}

// waitForQueue repeats probeQueue until one attempt passes. When ctx ends
// first, the reason from the final attempt becomes the failure.
func waitForQueue(ctx context.Context, bootstrap string) error {
	for {
		lastErr := probeQueue(ctx, bootstrap)
		if lastErr == nil {
			return nil
		}
		if !sleepOrDone(ctx) {
			slog.ErrorContext(ctx, "testenv.queue.not_ready", slog.String("err", lastErr.Error()))
			return fmt.Errorf("the test queue did not answer before the deadline: %w", lastErr)
		}
	}
}
