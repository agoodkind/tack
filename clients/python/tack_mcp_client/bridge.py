"""
HTTP bridge between Claude Code's stdio MCP transport and the Tack server.

Reads newline-delimited JSON-RPC from stdin. For each message, POSTs it to
the configured MCP endpoint with the bearer token. Handles both
application/json and text/event-stream responses. Writes responses to stdout.

The bridge maintains an MCP session ID across requests using the
Mcp-Session-Id header returned by the server.
"""

import asyncio
import json
import logging
import sys
from dataclasses import dataclass

import httpx


@dataclass
class BridgeConfig:
    """Runtime configuration for the HTTP bridge.

    Fields:
        url:   Full MCP endpoint URL (e.g. https://tack.example.com/mcp).
        token: Bearer token sent in every Authorization header.
        log:   Configured logger; carries structured context on every call.
    """

    url: str
    token: str
    log: logging.Logger


async def post_message(
    config: BridgeConfig,
    session_id: str | None,
    raw_message: str,
) -> tuple[str | None, list[str]]:
    """POST a single JSON-RPC message (already a JSON string) to the MCP server.

    Sets Authorization, Content-Type, Accept, and (when present) Mcp-Session-Id
    headers. Handles both text/event-stream (SSE) and application/json responses.

    Args:
        config:      Bridge configuration including URL and token.
        session_id:  Current MCP session ID, or None for the first request.
        raw_message: The raw JSON string to send as the request body.

    Returns:
        A tuple of (new_session_id, list_of_response_strings). new_session_id
        is the value from the Mcp-Session-Id response header; it equals
        session_id when the server does not rotate it.

    Raises:
        httpx.HTTPStatusError: On non-2xx responses, after logging status and body.
    """
    request_headers: dict[str, str] = {
        "Authorization": f"Bearer {config.token}",
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if session_id:
        request_headers["Mcp-Session-Id"] = session_id

    config.log.debug(
        "http.request",
        extra={
            "url": config.url,
            "method": "POST",
            "session_id": session_id,
        },
    )

    response_strings: list[str] = []

    async with httpx.AsyncClient(timeout=120.0) as http_client:
        async with http_client.stream(
            "POST",
            config.url,
            content=raw_message.encode(),
            headers=request_headers,
        ) as http_response:
            try:
                http_response.raise_for_status()
            except httpx.HTTPStatusError as status_error:
                response_body = await http_response.aread()
                config.log.error(
                    "http.error_response",
                    extra={
                        "status": http_response.status_code,
                        "body_snippet": response_body[:200].decode(errors="replace"),
                        "session_id": session_id,
                    },
                )
                raise

            new_session_id = http_response.headers.get("mcp-session-id", session_id)
            content_type = http_response.headers.get("content-type", "")

            if new_session_id != session_id:
                config.log.info(
                    "session.assigned",
                    extra={"new_session_id": new_session_id},
                )

            config.log.debug(
                "http.response",
                extra={
                    "status": http_response.status_code,
                    "content_type": content_type,
                    "session_id": new_session_id,
                },
            )

            if "text/event-stream" in content_type:
                async for raw_line in http_response.aiter_lines():
                    if raw_line.startswith("data:"):
                        data_value = raw_line[5:].strip()
                        if data_value and data_value != "[DONE]":
                            response_strings.append(data_value)
            else:
                response_body = await http_response.aread()
                decoded_body = response_body.decode(errors="replace").strip()
                if decoded_body:
                    response_strings.append(decoded_body)

    return new_session_id, response_strings


async def run_bridge(config: BridgeConfig) -> None:
    """Run the stdio-to-HTTP bridge loop.

    Reads stdin line by line using run_in_executor (reliable on all platforms
    for blocking stdin reads). For each line: logs receipt, calls post_message,
    logs each response, and writes each response to stdout.

    Exits cleanly on EOF. On HTTP or parse errors, logs the error and (for
    requests with an id field) writes a JSON-RPC error response to stdout so
    the caller is not left waiting.
    """
    config.log.info(
        "bridge.startup",
        extra={"url": config.url, "token_len": len(config.token)},
    )

    event_loop = asyncio.get_running_loop()
    current_session_id: str | None = None

    while True:
        raw_line = await event_loop.run_in_executor(None, sys.stdin.readline)
        if not raw_line:
            config.log.info("bridge.eof")
            break

        stripped_line = raw_line.strip()
        if not stripped_line:
            continue

        config.log.debug(
            "stdin.received",
            extra={"raw": stripped_line[:120]},
        )

        try:
            incoming_message = json.loads(stripped_line)
        except json.JSONDecodeError as parse_error:
            config.log.error(
                "stdin.parse_error",
                extra={"error": str(parse_error), "raw": stripped_line[:80]},
            )
            continue

        config.log.info(
            "message.received",
            extra={
                "session_id": current_session_id,
                "msg_id": incoming_message.get("id"),
                "method": incoming_message.get("method"),
            },
        )

        try:
            current_session_id, response_strings = await post_message(
                config, current_session_id, stripped_line
            )
        except Exception as request_error:
            config.log.error(
                "http.error",
                extra={
                    "session_id": current_session_id,
                    "msg_id": incoming_message.get("id"),
                    "error": str(request_error),
                },
            )
            if isinstance(incoming_message, dict) and "id" in incoming_message:
                error_response = {
                    "jsonrpc": "2.0",
                    "id": incoming_message["id"],
                    "error": {"code": -32603, "message": str(request_error)},
                }
                sys.stdout.write(json.dumps(error_response) + "\n")
                sys.stdout.flush()
            continue

        for response_string in response_strings:
            config.log.info(
                "message.sent",
                extra={
                    "session_id": current_session_id,
                    "raw": response_string[:120],
                },
            )
            sys.stdout.write(response_string + "\n")
            sys.stdout.flush()
