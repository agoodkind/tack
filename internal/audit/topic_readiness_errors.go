package audit

import "fmt"

type kafkaTopicMetadataMissingError struct {
	Topic string
}

func (failure kafkaTopicMetadataMissingError) Error() string {
	return fmt.Sprintf("audit kafka topic %s: cluster did not return topic metadata", failure.Topic)
}

type kafkaTopicPartitionsMissingError struct {
	Topic string
}

func (failure kafkaTopicPartitionsMissingError) Error() string {
	return fmt.Sprintf("audit kafka topic %s: does not report any partitions", failure.Topic)
}

type kafkaTopicConfigMissingError struct {
	Topic  string
	Config string
}

func (failure kafkaTopicConfigMissingError) Error() string {
	return fmt.Sprintf(
		"audit kafka topic %s: cluster did not report %s",
		failure.Topic,
		failure.Config,
	)
}
