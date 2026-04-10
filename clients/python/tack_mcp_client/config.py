"""
Configuration for the Tack MCP client.

All configuration comes from environment variables. There is no config file.

Environment variables:
  TACK_URL        MCP server endpoint URL (required)
  TACK_TOKEN      API bearer token (required)
  TACK_LOG_LEVEL  Log level: debug, info, warn, error (default: info)
"""

import os
from dataclasses import dataclass


DEFAULT_URL = "https://tack.home.goodkind.io/mcp"
DEFAULT_LOG_LEVEL = "info"


@dataclass
class Config:
    """Resolved configuration for the MCP client. All fields come from env vars."""

    # Full URL of the Tack MCP Streamable HTTP endpoint.
    url: str

    # API bearer token. Must be non-empty before the bridge starts.
    token: str

    # Log level string. One of: debug, info, warn, error.
    log_level: str


def load() -> Config:
    """Load configuration from environment variables."""
    return Config(
        url=os.environ.get("TACK_URL", DEFAULT_URL),
        token=os.environ.get("TACK_TOKEN", ""),
        log_level=os.environ.get("TACK_LOG_LEVEL", DEFAULT_LOG_LEVEL),
    )
