#!/bin/bash

#
# fdb.bash
#
# This source file is part of the FoundationDB open source project
#
# Copyright 2013-2024 Apple Inc. and the FoundationDB project authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

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
    mkdir -p "$(dirname "$FDB_CLUSTER_FILE")"

    if [[ -s "$FDB_CLUSTER_FILE" ]]; then
        echo "Keeping the cluster file already on this guest at $FDB_CLUSTER_FILE"
        return 0
    fi

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

    if [[ -n "$FDB_PUBLIC_ADDRESS" ]]; then
        public_ip="$FDB_PUBLIC_ADDRESS"
    elif [[ "$FDB_NETWORKING_MODE" == "host" ]]; then
        public_ip=127.0.0.1
    elif [[ "$FDB_NETWORKING_MODE" == "container" ]]; then
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

if [[ "$PUBLIC_IP" == *:* ]]; then
    LISTEN_ADDR="[::]:$FDB_PORT"
    PUBLIC_ADDR="[$PUBLIC_IP]:$FDB_PORT"
else
    LISTEN_ADDR="0.0.0.0:$FDB_PORT"
    PUBLIC_ADDR="$PUBLIC_IP:$FDB_PORT"
fi

ZONE_ID=${FDB_ZONE_ID:-$(hostname)}
MACHINE_ID=${FDB_MACHINE_ID:-$(hostname)}

echo "Starting FDB server on $PUBLIC_ADDR (listen $LISTEN_ADDR, zone $ZONE_ID)"
fdbserver --listen-address "$LISTEN_ADDR" --public-address "$PUBLIC_ADDR" \
    --datadir /var/fdb/data --logdir /var/fdb/logs \
    --locality-zoneid="$ZONE_ID" --locality-machineid="$MACHINE_ID" --class "$FDB_PROCESS_CLASS" --knob_disable_posix_kernel_aio=1
