#!/bin/bash
# IPv6-aware patch of foundationdb/foundationdb:7.4.6 /var/fdb/scripts/fdb.bash.
# Fixes: (1) bracket IPv6 addresses in the connection string, (2) listen on
# [::] when running v6, not 0.0.0.0.

function bracketed() {
    local ip="$1"
    if [[ "$ip" == *:* ]]; then
        echo "[$ip]"
    else
        echo "$ip"
    fi
}

function create_cluster_file() {
    FDB_CLUSTER_FILE=${FDB_CLUSTER_FILE:-/etc/foundationdb/fdb.cluster}
    mkdir -p "$(dirname $FDB_CLUSTER_FILE)"

    if [[ -n "$FDB_CLUSTER_FILE_CONTENTS" ]]; then
        echo "$FDB_CLUSTER_FILE_CONTENTS" > "$FDB_CLUSTER_FILE"
    elif [[ -n "$FDB_COORDINATOR" ]]; then
        coordinator_ip=$(dig +short "$FDB_COORDINATOR" AAAA)
        if [[ -z "$coordinator_ip" ]]; then
            coordinator_ip=$(dig +short "$FDB_COORDINATOR" A)
        fi
        if [[ -z "$coordinator_ip" ]]; then
            echo "Failed to look up coordinator address for $FDB_COORDINATOR" 1>&2
            exit 1
        fi
        coordinator_port=${FDB_COORDINATOR_PORT:-4500}
        echo "docker:docker@$(bracketed "$coordinator_ip"):$coordinator_port" > "$FDB_CLUSTER_FILE"
    else
        echo "FDB_COORDINATOR environment variable not defined" 1>&2
        exit 1
    fi
}

function create_server_environment() {
    env_file=/var/fdb/.fdbenv

    if [[ "$FDB_NETWORKING_MODE" == "host" ]]; then
        public_ip=127.0.0.1
    elif [[ "$FDB_NETWORKING_MODE" == "container" ]]; then
        # Prefer global IPv6 if present, otherwise fall back to IPv4.
        public_ip=$(hostname -I | tr " " "\n" | awk "/^[0-9a-fA-F:]+:/ && !/^fe80/ && !/^::1/ {print; exit}")
        if [[ -z "$public_ip" ]]; then
            public_ip=$(hostname -i | awk "{print \$1}")
        fi
    else
        echo "Unknown FDB Networking mode \"$FDB_NETWORKING_MODE\"" 1>&2
        exit 1
    fi

    echo "export PUBLIC_IP=$public_ip" > $env_file
    if [[ -z "$FDB_COORDINATOR" && -z "$FDB_CLUSTER_FILE_CONTENTS" ]]; then
        FDB_CLUSTER_FILE_CONTENTS="docker:docker@$(bracketed "$public_ip"):$FDB_PORT"
    fi

    create_cluster_file
}

create_server_environment
source /var/fdb/.fdbenv

# Listen address: IPv6 catch-all when public_ip is v6, else v4 catch-all.
if [[ "$PUBLIC_IP" == *:* ]]; then
    LISTEN_ADDR="[::]:$FDB_PORT"
    PUBLIC_ADDR="[$PUBLIC_IP]:$FDB_PORT"
else
    LISTEN_ADDR="0.0.0.0:$FDB_PORT"
    PUBLIC_ADDR="$PUBLIC_IP:$FDB_PORT"
fi

echo "Starting FDB server on $PUBLIC_ADDR (listen $LISTEN_ADDR)"
fdbserver --listen-address "$LISTEN_ADDR" --public-address "$PUBLIC_ADDR" \
    --datadir /var/fdb/data --logdir /var/fdb/logs \
    --locality-zoneid="$(hostname)" --locality-machineid="$(hostname)" --class "$FDB_PROCESS_CLASS" --knob_disable_posix_kernel_aio=1
