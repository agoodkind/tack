package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// queueConfigEdit is one configuration key with the value written to it. A
// deletion ignores Value.
type queueConfigEdit struct {
	Name  string
	Value string
}

// applyQueueConfigs sends one incremental alter against a single resource.
// Configuration keys this call leaves unnamed keep the values they already
// carry.
func applyQueueConfigs(
	ctx context.Context,
	client *kgo.Client,
	resourceType kmsg.ConfigResourceType,
	resourceName string,
	operation kmsg.IncrementalAlterConfigOp,
	edits []queueConfigEdit,
) error {
	req := kmsg.NewPtrIncrementalAlterConfigsRequest()
	resource := kmsg.NewIncrementalAlterConfigsRequestResource()
	resource.ResourceType = resourceType
	resource.ResourceName = resourceName
	for index := range edits {
		config := kmsg.NewIncrementalAlterConfigsRequestResourceConfig()
		config.Name = edits[index].Name
		config.Op = operation
		if operation == kmsg.IncrementalAlterConfigOpDelete {
			config.Value = nil
		} else {
			config.Value = &edits[index].Value
		}
		resource.Configs = append(resource.Configs, config)
	}
	req.Resources = append(req.Resources, resource)

	callCtx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	resp, err := req.RequestWith(callCtx, client)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.alter_configs_failed",
			slog.String("resource", resourceName), slog.String("err", err.Error()))
		return fmt.Errorf("ops queue alter configs of %s: %w", resourceName, err)
	}
	return readAlterConfigsOutcome(ctx, resp, resourceName)
}

// readAlterConfigsOutcome turns a refusal in the answer into an error.
func readAlterConfigsOutcome(
	ctx context.Context,
	resp *kmsg.IncrementalAlterConfigsResponse,
	resourceName string,
) error {
	for _, respResource := range resp.Resources {
		if respResource.ErrorCode == 0 {
			continue
		}
		codeErr := kerr.ErrorForCode(respResource.ErrorCode)
		slog.ErrorContext(ctx, "ops.queue.alter_configs_rejected",
			slog.String("resource", resourceName), slog.String("err", codeErr.Error()))
		return fmt.Errorf("ops queue config change on %s was refused: %w", resourceName, codeErr)
	}
	slog.InfoContext(ctx, "ops.queue.configs_applied", slog.String("resource", resourceName))
	return nil
}
