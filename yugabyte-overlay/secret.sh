#!/usr/bin/env bash

load_ysql_password() {
    if [[ -n "${YSQL_PASSWORD:-}" ]]; then
        printf '%s\n' 'YSQL_PASSWORD must use YSQL_PASSWORD_FILE' >&2
        return 1
    fi
    if [[ ! -v YSQL_PASSWORD_FILE ]]; then
        printf '%s\n' 'YSQL_PASSWORD_FILE is required' >&2
        return 1
    fi
    if [[ -z "$YSQL_PASSWORD_FILE" ]]; then
        printf '%s\n' 'YSQL_PASSWORD_FILE is required' >&2
        return 1
    fi
    local password_lines=()
    if ! mapfile -t password_lines < "$YSQL_PASSWORD_FILE"; then
        printf '%s\n' 'YSQL_PASSWORD_FILE cannot be read' >&2
        return 1
    fi
    if [[ ${#password_lines[@]} -ne 1 ]]; then
        printf '%s\n' 'YSQL_PASSWORD_FILE must contain one password line' >&2
        return 1
    fi
    YSQL_PASSWORD="${password_lines[0]%$'\r'}"
    if [[ ! "$YSQL_PASSWORD" =~ [^[:space:]] ]]; then
        printf '%s\n' 'YSQL_PASSWORD_FILE contains an empty password' >&2
        return 1
    fi
}
