#!/usr/bin/env python3
"""Fail closed on failed/skipped tests or missing required demo scenarios."""
import argparse
import collections
import json
import sys

parser = argparse.ArgumentParser()
parser.add_argument("log")
parser.add_argument("--require", action="append", default=[])
args = parser.parse_args()
counts = collections.Counter()
passed = set()
bad_packages = []
with open(args.log, encoding="utf-8") as stream:
    for line in stream:
        if not line.strip():
            continue
        event = json.loads(line)
        action = event.get("Action")
        if event.get("Test") and action in ("pass", "fail", "skip"):
            counts[action] += 1
            if action == "pass":
                passed.add(event["Test"])
        elif action == "fail":
            bad_packages.append(event.get("Package", "?"))
missing = sorted(set(args.require) - passed)
print(f"PASS={counts['pass']} FAIL={counts['fail']} SKIP={counts['skip']}")
if counts["pass"] == 0 or counts["fail"] or counts["skip"] or bad_packages or missing:
    print(f"FAIL: package_failures={bad_packages}; missing_scenarios={missing}", file=sys.stderr)
    sys.exit(1)
