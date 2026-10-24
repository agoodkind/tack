
			INSERT INTO audit.events_dlq (received_at, topic, partition, "offset", payload, error, attempt_count, last_attempt_at)
			VALUES (now(), $1, $2, $3, $4, $5, 0, NULL)
			ON CONFLICT (topic, partition, "offset") DO NOTHING
