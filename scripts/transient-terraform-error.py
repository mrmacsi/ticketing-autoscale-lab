#!/usr/bin/env python3
"""Recognize only temporary transport errors in Terraform's error summaries.

With --already-exists, print "<address> <id>" for every resource that a dropped connection left created
in Google Cloud but missing from the state (Error 409 "already exists"), so it can be imported.
"""
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
        r"dial tcp.*(?:network is unreachable|no route to host|no such host)|"
        r"tls: failed to verify certificate|certificate is not standards compliant", re.IGNORECASE)
    permanent = re.compile(
        r"permission denied|forbidden|unauthorized|invalid request|resourceInUse|"
        r"access denied|deletion protection|Error (?:400|401|403|404)\b", re.IGNORECASE)
    return bool(summaries) and all(transient.search(s) and not permanent.search(s) for s in summaries)


def already_exists(output):
    """(address, id) pairs from 409 "already exists" errors, in order."""
    output = re.sub(r"\x1b\[[0-9;]*m", "", output)
    pairs = []
    for block in re.split(r"\n\s*[╷│]?\s*Error:", "\n" + output)[1:]:
        found = re.search(r"The resource '([^']+)' already exists", block)
        where = re.search(r"\bwith ([\w.\[\]\"-]+),", block)
        if found and where:
            pairs.append((where.group(1), found.group(1)))
    return pairs


if __name__ == "__main__":
    if sys.argv[1] == "--already-exists":
        with open(sys.argv[2]) as log:
            for address, resource_id in already_exists(log.read()):
                print(address, resource_id)
        sys.exit(0)
    with open(sys.argv[1]) as log:
        sys.exit(0 if is_transient_terraform_failure(log.read()) else 1)
