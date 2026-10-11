#!/usr/bin/env python3
"""Recognize only temporary transport errors in Terraform's error summaries."""
import re
import sys


def is_transient_terraform_failure(output):
    output = re.sub(r"\x1b\[[0-9;]*m", "", output)
    lines = [line.lstrip(" │") for line in output.splitlines()]
    summaries = []
    for index, line in enumerate(lines):
        if not line.startswith("Error:"):
            continue
        summary = [line]
        for continuation in lines[index + 1:]:
            if not continuation or continuation.startswith(("Error:", "╵", "╷", "with ")):
                break
            summary.append(continuation)
        summaries.append(" ".join(summary))
    transient = re.compile(
        r"\b(?:unexpected )?EOF\b|connection reset by peer|TLS handshake timeout|"
        r"context deadline exceeded|i/o timeout|http2: client connection lost|temporary failure in name resolution|"
        r"dial tcp.*(?:network is unreachable|no route to host|no such host)", re.IGNORECASE)
    permanent = re.compile(
        r"permission denied|forbidden|unauthorized|invalid request|resourceInUse|"
        r"access denied|deletion protection|Error (?:400|401|403|404)\b", re.IGNORECASE)
    return bool(summaries) and all(transient.search(s) and not permanent.search(s) for s in summaries)


if __name__ == "__main__":
    with open(sys.argv[1]) as log:
        sys.exit(0 if is_transient_terraform_failure(log.read()) else 1)
