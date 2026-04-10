import os
import sys


def main() -> None:
    bin_path = os.path.expanduser("~/.local/bin/tack-mcp-client")
    os.execv(bin_path, [bin_path])
