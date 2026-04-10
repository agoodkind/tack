"""
Tack MCP client.

Thin stdio-to-HTTP bridge. Reads JSON-RPC from stdin, forwards to the Tack
server's Streamable HTTP MCP endpoint, writes responses to stdout.

Configuration comes exclusively from environment variables:
  TACK_URL        Server endpoint (default: https://tack.home.goodkind.io/mcp)
  TACK_TOKEN      API bearer token (required)
  TACK_LOG_LEVEL  Log verbosity: debug, info, warn, error (default: info)

Logs are written to stderr and ~/.local/state/tack/mcp-client.log
(respects $XDG_STATE_HOME).
"""

import asyncio
import sys

from tack_mcp_client.config import load as load_config
from tack_mcp_client.log import setup as setup_logging
from tack_mcp_client.bridge import BridgeConfig, run_bridge


def main() -> None:
    """Entry point registered by pyproject.toml."""
    config = load_config()
    logger = setup_logging(config.log_level)

    if not config.token:
        logger.error(
            "startup_failed",
            extra={"reason": "TACK_TOKEN env var is required but not set"},
        )
        sys.exit(1)

    bridge_config = BridgeConfig(url=config.url, token=config.token, log=logger)
    asyncio.run(run_bridge(bridge_config))
