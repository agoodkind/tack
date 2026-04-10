"""
XDG Base Directory resolution for the Tack MCP client.

Only the state directory is resolved here. Configuration comes exclusively
from environment variables. There is no config file.

Path resolution for the log file:
  1. $XDG_STATE_HOME/tack/mcp-client.log  (if XDG_STATE_HOME is set)
  2. ~/.local/state/tack/mcp-client.log   (XDG spec default)
"""

import os
from pathlib import Path


def state_dir() -> Path:
    """Return the XDG state directory for tack: $XDG_STATE_HOME/tack or ~/.local/state/tack."""
    base = os.environ.get("XDG_STATE_HOME")
    if base:
        return Path(base) / "tack"
    return Path.home() / ".local" / "state" / "tack"


def log_file_path() -> Path:
    """Return the path for the MCP client log file."""
    return state_dir() / "mcp-client.log"
