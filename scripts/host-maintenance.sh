#!/usr/bin/env bash
# Maintain CT 117 host disk health without changing the existing deploy flow.
# TODO: do we still need this?
set -euo pipefail

ROOT_USAGE_MAX_PERCENT="${ROOT_USAGE_MAX_PERCENT:-85}"
ROOT_MIN_FREE_KB="${ROOT_MIN_FREE_KB:-6291456}"
BACKUP_DIR="${BACKUP_DIR:-/root/backups}"
BACKUP_KEEP_COUNT="${BACKUP_KEEP_COUNT:-7}"
BACKUP_KEEP_DAYS="${BACKUP_KEEP_DAYS:-3}"
BUILDKIT_RESERVED_STORAGE="${BUILDKIT_RESERVED_STORAGE:-2GB}"
DANGLING_IMAGE_UNTIL="${DANGLING_IMAGE_UNTIL:-24h}"
UNUSED_IMAGE_UNTIL="${UNUSED_IMAGE_UNTIL:-168h}"

usage() {
	printf 'usage: %s {report|preflight|deploy-preflight|deploy-cleanup|rotate-backups|weekly|install-timer}\n' "$0" >&2
}

root_usage_percent() {
	df -P / | awk 'NR == 2 { gsub(/%/, "", $5); print $5 }'
}

root_available_kb() {
	df -Pk / | awk 'NR == 2 { print $4 }'
}

report() {
	echo "== filesystem =="
	df -h /
	echo ""
	echo "== docker =="
	docker system df
	echo ""
	echo "== backups =="
	if [[ -d "$BACKUP_DIR" ]]; then
		du -sh "$BACKUP_DIR"
	else
		echo "$BACKUP_DIR missing"
	fi
}

preflight() {
	local usage_percent
	local available_kb

	usage_percent="$(root_usage_percent)"
	available_kb="$(root_available_kb)"

	if ((usage_percent > ROOT_USAGE_MAX_PERCENT)); then
		echo "root filesystem is ${usage_percent}% full; max allowed is ${ROOT_USAGE_MAX_PERCENT}%" >&2
		return 1
	fi

	if ((available_kb < ROOT_MIN_FREE_KB)); then
		echo "root filesystem has ${available_kb}KB free; minimum is ${ROOT_MIN_FREE_KB}KB" >&2
		return 1
	fi
}

prune_dangling_images() {
	echo "== prune dangling docker images older than ${DANGLING_IMAGE_UNTIL} =="
	docker image prune --force --filter "until=${DANGLING_IMAGE_UNTIL}"
}

prune_unused_images() {
	echo "== prune unused docker images older than ${UNUSED_IMAGE_UNTIL} =="
	docker image prune --all --force --filter "until=${UNUSED_IMAGE_UNTIL}"
}

prune_build_cache() {
	echo "== prune build cache to ${BUILDKIT_RESERVED_STORAGE} =="
	docker builder prune --all --force --reserved-space "${BUILDKIT_RESERVED_STORAGE}"
}

rotate_backups() {
	local latest_timestamp
	local backup_index
	local backup_line
	local backup_path
	local backup_name
	local backup_day
	local backup_epoch
	local now_epoch
	local age_days
	declare -A kept_days=()

	if [[ ! -d "$BACKUP_DIR" ]]; then
		echo "backup directory missing: $BACKUP_DIR"
		return 0
	fi

	latest_timestamp=""
	if [[ -r "$BACKUP_DIR/.latest" ]]; then
		latest_timestamp="$(tr -d '[:space:]' <"$BACKUP_DIR/.latest")"
	fi

	now_epoch="$(date +%s)"
	backup_index=0

	while IFS= read -r backup_line; do
		backup_path="${backup_line#* }"
		backup_name="$(basename "$backup_path")"
		backup_day="${backup_name#tack-}"
		backup_day="${backup_day:0:8}"
		backup_epoch="$(stat -c '%Y' "$backup_path")"
		age_days=$(((now_epoch - backup_epoch) / 86400))

		if ((backup_index < BACKUP_KEEP_COUNT)); then
			kept_days["$backup_day"]=1
			backup_index=$((backup_index + 1))
			continue
		fi

		if [[ "$backup_name" == "tack-${latest_timestamp}" ]]; then
			kept_days["$backup_day"]=1
			backup_index=$((backup_index + 1))
			continue
		fi

		if ((age_days <= BACKUP_KEEP_DAYS)) && [[ -z "${kept_days[$backup_day]:-}" ]]; then
			kept_days["$backup_day"]=1
			backup_index=$((backup_index + 1))
			continue
		fi

		echo "removing old backup: $backup_path"
		rm -rf -- "$backup_path"
		backup_index=$((backup_index + 1))
	done < <(find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -name 'tack-*' -printf '%T@ %p\n' | sort -rn)
}

deploy_cleanup() {
	prune_dangling_images
	prune_build_cache
}

weekly() {
	report
	rotate_backups
	prune_dangling_images
	prune_unused_images
	prune_build_cache
	echo ""
	report
}

deploy_preflight() {
	rotate_backups
	deploy_cleanup
	preflight
}

install_timer() {
	local script_dir

	script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
	install -m 0644 "$script_dir/systemd/tack-host-maintenance.service" /etc/systemd/system/tack-host-maintenance.service
	install -m 0644 "$script_dir/systemd/tack-host-maintenance.timer" /etc/systemd/system/tack-host-maintenance.timer

	systemctl daemon-reload
	systemctl enable --now tack-host-maintenance.timer
}

case "${1:-}" in
report)
	report
	;;
preflight)
	preflight
	;;
deploy-preflight)
	deploy_preflight
	;;
deploy-cleanup)
	deploy_cleanup
	;;
rotate-backups)
	rotate_backups
	;;
weekly)
	weekly
	;;
install-timer)
	install_timer
	;;
*)
	usage
	exit 2
	;;
esac
