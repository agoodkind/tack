SELECT partman.create_partition_time(
    'audit.events',
    ARRAY[date_trunc('week', $1::timestamptz AT TIME ZONE 'UTC') AT TIME ZONE 'UTC']
)
