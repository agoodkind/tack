"""
Structured JSON logging setup for the Tack MCP client.

Creates a logger named "tack-mcp-client" that writes JSON lines to two
destinations simultaneously:
  - stderr: so the parent process (Claude Code) can surface errors immediately
  - $XDG_STATE_HOME/tack/mcp-client.log: persistent structured log

Each log record is a single JSON object with at minimum:
  {"time": "<ISO8601 UTC>", "level": "INFO", "msg": "..."}

Extra keyword arguments passed via the extra dict are merged into the
top-level JSON object alongside time, level, and msg.
"""

import json
import logging
import sys
from datetime import datetime, timezone
from pathlib import Path

from tack_mcp_client.xdg import log_file_path


class JsonFormatter(logging.Formatter):
    """Formats each log record as a single JSON object on one line.

    The output schema is:
      {"time": "<ISO8601 UTC>", "level": "<LEVEL>", "msg": "<message>", ...extra}

    Any keys passed via logger.xxx(..., extra={...}) are merged into the
    top-level object alongside time, level, and msg.
    """

    # Keys always present on a LogRecord that should not be re-emitted as
    # extra fields — avoids cluttering the structured output.
    _RESERVED_LOG_RECORD_KEYS = frozenset(
        logging.LogRecord("", 0, "", 0, "", (), None).__dict__.keys()
    ) | {"message", "asctime"}

    def format(self, record: logging.LogRecord) -> str:
        """Render the log record as a JSON string."""
        record.message = record.getMessage()
        timestamp = (
            datetime.fromtimestamp(record.created, tz=timezone.utc)
            .isoformat(timespec="milliseconds")
            .replace("+00:00", "Z")
        )
        log_entry: dict = {
            "time": timestamp,
            "level": record.levelname,
            "msg": record.message,
        }
        # Merge any extra fields the caller passed in.
        for key, value in record.__dict__.items():
            if key not in self._RESERVED_LOG_RECORD_KEYS:
                log_entry[key] = value

        if record.exc_info:
            log_entry["exc_info"] = self.formatException(record.exc_info)

        return json.dumps(log_entry)


def _parse_log_level(log_level_string: str) -> int:
    """Convert a level name string to a logging module integer level.

    Accepts: debug, info, warn, warning, error. Defaults to INFO for
    unrecognised values.
    """
    normalised = log_level_string.strip().upper()
    if normalised == "WARN":
        normalised = "WARNING"
    level = logging.getLevelName(normalised)
    if not isinstance(level, int):
        return logging.INFO
    return level


def setup(log_level: str) -> logging.Logger:
    """Create and return the application logger.

    Adds two handlers:
      - StreamHandler to sys.stderr for immediate visibility
      - FileHandler to xdg.log_file_path() (creates parent directories as needed)

    Both handlers use JsonFormatter so every destination produces identical
    structured output. The logger level is set from log_level.

    Args:
        log_level: One of "debug", "info", "warn", "error".

    Returns:
        A configured logging.Logger instance named "tack-mcp-client".
    """
    logger = logging.getLogger("tack-mcp-client")
    logger.setLevel(_parse_log_level(log_level))
    # Prevent duplicate handlers if setup() is called more than once.
    logger.handlers.clear()
    logger.propagate = False

    json_formatter = JsonFormatter()

    stderr_handler = logging.StreamHandler(sys.stderr)
    stderr_handler.setFormatter(json_formatter)
    logger.addHandler(stderr_handler)

    log_path = log_file_path()
    log_path.parent.mkdir(parents=True, exist_ok=True)
    file_handler = logging.FileHandler(str(log_path), encoding="utf-8")
    file_handler.setFormatter(json_formatter)
    logger.addHandler(file_handler)

    return logger
