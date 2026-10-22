#!/usr/bin/env python3
"""Read-only clock-step probe. Never adjusts clocks or time services."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
from urllib.parse import parse_qsl, unquote, urlsplit
import time


class ClockSeries:
    def __init__(self, threshold_ns):
        self.threshold_ns = threshold_ns
        self.previous = None
        self.samples = 0
        self.backward_steps = 0
        self.forward_steps = 0
        self.max_gap_ns = 0
        self.events = []

    def add(self, wall_ns, monotonic_ns, uncertainty_ns=0):
        current = (wall_ns, monotonic_ns, uncertainty_ns)
        self.samples += 1
        if self.previous is not None:
            old_wall, old_mono, old_uncertainty = self.previous
            mono_delta = monotonic_ns - old_mono
            wall_delta = wall_ns - old_wall
            self.max_gap_ns = max(self.max_gap_ns, mono_delta)
            adjustment = wall_delta - mono_delta
            bound = self.threshold_ns + old_uncertainty + uncertainty_ns
            if abs(adjustment) > bound:
                direction = "backward" if adjustment < 0 else "forward"
                if adjustment < 0:
                    self.backward_steps += 1
                else:
                    self.forward_steps += 1
                if len(self.events) < 1000:
                    self.events.append({
                        "sample": self.samples,
                        "direction": direction,
                        "previous_wall_ns": old_wall,
                        "current_wall_ns": wall_ns,
                        "monotonic_delta_ms": mono_delta / 1e6,
                        "wall_delta_ms": wall_delta / 1e6,
                        "adjustment_ms": adjustment / 1e6,
                        "detection_bound_ms": bound / 1e6,
                    })
        self.previous = current

    def result(self):
        return {
            "samples": self.samples,
            "backward_steps": self.backward_steps,
            "forward_steps": self.forward_steps,
            "max_sample_gap_ms": self.max_gap_ns / 1e6,
            "events": self.events,
            "events_omitted": max(0, self.backward_steps + self.forward_steps - len(self.events)),
        }


def connection_environment(dsn):
    # libpq does not expand a conninfo string stored in PGDATABASE itself.
    # Parse to environment variables so passwords stay out of process arguments.
    options = {}
    if dsn.startswith(("postgresql://", "postgres://")):
        parsed = urlsplit(dsn)
        if parsed.fragment:
            raise ValueError("unsupported connection configuration")
        options["dbname"] = unquote(parsed.path.lstrip("/"))
        if parsed.hostname is not None:
            options["host"] = parsed.hostname
        if parsed.port is not None:
            options["port"] = str(parsed.port)
        if parsed.username is not None:
            options["user"] = unquote(parsed.username)
        if parsed.password is not None:
            options["password"] = unquote(parsed.password)
        options.update(parse_qsl(parsed.query, keep_blank_values=True))
    else:
        i = 0
        while i < len(dsn):
            while i < len(dsn) and dsn[i].isspace():
                i += 1
            if i == len(dsn):
                break
            start = i
            while i < len(dsn) and (dsn[i].isalnum() or dsn[i] == "_"):
                i += 1
            key = dsn[start:i]
            while i < len(dsn) and dsn[i].isspace():
                i += 1
            if not key or i == len(dsn) or dsn[i] != "=":
                raise ValueError("unsupported connection configuration")
            i += 1
            while i < len(dsn) and dsn[i].isspace():
                i += 1
            quoted = i < len(dsn) and dsn[i] == "'"
            if quoted:
                i += 1
            value = []
            closed = not quoted
            while i < len(dsn):
                char = dsn[i]
                if char == "\\":
                    i += 1
                    if i == len(dsn):
                        raise ValueError("unsupported connection configuration")
                    value.append(dsn[i])
                    i += 1
                elif quoted and char == "'":
                    i += 1
                    closed = True
                    break
                elif not quoted and char.isspace():
                    break
                else:
                    value.append(char)
                    i += 1
            if not closed or (i < len(dsn) and not dsn[i].isspace()):
                raise ValueError("unsupported connection configuration")
            options[key] = "".join(value)
    names = {
        "host": "PGHOST", "hostaddr": "PGHOSTADDR", "port": "PGPORT",
        "dbname": "PGDATABASE", "user": "PGUSER", "password": "PGPASSWORD",
        "sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT",
        "sslkey": "PGSSLKEY", "passfile": "PGPASSFILE", "service": "PGSERVICE",
        "connect_timeout": "PGCONNECT_TIMEOUT", "application_name": "PGAPPNAME",
        "options": "PGOPTIONS", "target_session_attrs": "PGTARGETSESSIONATTRS",
        "channel_binding": "PGCHANNELBINDING", "gssencmode": "PGGSSENCMODE",
    }
    if not options or any(key not in names for key in options):
        raise ValueError("unsupported connection configuration")
    return {names[key]: value for key, value in options.items()}


def postgres_sample(dsn):
    # Keep credentials out of process arguments, output, and exception messages.
    env = os.environ.copy()
    env.pop("DEEPSEEK_API_KEY", None)
    try:
        env.update(connection_environment(dsn))
    except ValueError:
        raise RuntimeError("connection configuration is unsupported by the read-only clock probe") from None
    env.update(PGCONNECT_TIMEOUT="2", PGAPPNAME="runtime-clock-probe",
               PAGER="cat", PGOPTIONS="-c default_transaction_read_only=on -c statement_timeout=2000")
    sql = ("BEGIN READ ONLY; SELECT current_database() || '|' || "
           "EXTRACT(EPOCH FROM clock_timestamp())::text; ROLLBACK;")
    started = time.monotonic_ns()
    try:
        run = subprocess.run(["psql", "-X", "-q", "-A", "-t", "-w", "-v", "ON_ERROR_STOP=1", "-c", sql],
                             env=env, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                             text=True, timeout=3, check=False)
        ended = time.monotonic_ns()
        if run.returncode != 0:
            raise ValueError()
        lines = [line for line in run.stdout.splitlines() if "|" in line]
        if len(lines) != 1:
            raise ValueError()
        database, epoch = lines[0].split("|")
        if not database.endswith("_test"):
            raise ValueError()
        whole, fraction = epoch.split(".")
        wall = int(whole) * 10**9 + int((fraction + "000000000")[:9])
    except (OSError, ValueError, subprocess.TimeoutExpired):
        raise RuntimeError("dedicated PostgreSQL clock sample unavailable or database name is not *_test") from None
    return wall, (started + ended) // 2, (ended - started + 1) // 2


def system_sample():
    # A scheduling pause between clock reads is measurement uncertainty, not a
    # wall-clock step. Bound the wall sample by monotonic reads on both sides.
    before = time.monotonic_ns()
    wall = time.time_ns()
    after = time.monotonic_ns()
    return wall, (before + after) // 2, (after - before + 1) // 2


def run_probe(args):
    system = ClockSeries(round(args.threshold_ms * 1e6))
    database = ClockSeries(round(args.threshold_ms * 1e6))
    failures = []
    stopped = threading.Event()
    dsn = os.environ.get("DAY12_TEST_DATABASE_URL", "") if args.postgres else ""
    if args.postgres and not dsn:
        raise RuntimeError("--postgres requires DAY12_TEST_DATABASE_URL for a dedicated *_test database")
    if args.postgres:
        database.add(*postgres_sample(dsn))

    def database_loop():
        while not stopped.wait(args.database_interval):
            try:
                database.add(*postgres_sample(dsn))
            except RuntimeError:
                failures.append("database sample unavailable")
                if len(failures) >= 10:
                    return

    worker = threading.Thread(target=database_loop, daemon=True)
    started = time.monotonic()
    if args.postgres:
        worker.start()
    try:
        while True:
            system.add(*system_sample())
            remaining = args.duration - (time.monotonic() - started)
            if remaining <= 0:
                break
            stopped.wait(min(args.interval, remaining))
    finally:
        stopped.set()
        if args.postgres:
            worker.join(timeout=4)
    anomaly = system.backward_steps + system.forward_steps + database.backward_steps + database.forward_steps
    status = "INCOMPLETE" if failures else ("ANOMALY" if anomaly else "NO_STEP_OBSERVED")
    return {
        "version": 1,
        "status": status,
        "scope": "read-only observation; does not adjust clocks, services, SQL rows or lease settings",
        "interpretation": "No observed step does not prove future clock stability or identify a time service as the cause.",
        "duration_monotonic_seconds": round(time.monotonic() - started, 6),
        "threshold_ms": args.threshold_ms,
        "postgres_uncertainty": "successive midpoint estimates include half of each request round-trip in the detection bound",
        "system": system.result(),
        "postgres": database.result() if args.postgres else {"status": "NOT_REQUESTED"},
        "sample_failures": len(failures),
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--duration", type=float, default=90, help="monotonic observation seconds, 5..300")
    parser.add_argument("--interval", type=float, default=.01, help="system sample interval seconds, .001..1")
    parser.add_argument("--database-interval", type=float, default=1, help="PostgreSQL sample interval seconds, .2..30")
    parser.add_argument("--threshold-ms", type=float, default=100, help="minimum step beyond timing uncertainty, 1..10000ms")
    parser.add_argument("--postgres", action="store_true", help="also read a dedicated *_test database using DAY12_TEST_DATABASE_URL")
    parser.add_argument("--output", required=True, help="new absolute private JSON output file")
    args = parser.parse_args(argv)
    if not (5 <= args.duration <= 300 and .001 <= args.interval <= 1 and
            .2 <= args.database_interval <= 30 and 1 <= args.threshold_ms <= 10000):
        parser.error("duration/interval/threshold outside bounded range")
    output = Path(args.output)
    if not output.is_absolute():
        parser.error("--output must be a new absolute file")
    try:
        fd = os.open(output, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except OSError:
        print("cannot create new private output file", file=sys.stderr)
        return 2
    with os.fdopen(fd, "w") as dest:
        try:
            result = run_probe(args)
        except RuntimeError as error:
            result = {"version": 1, "status": "INCOMPLETE", "error": str(error)}
        json.dump(result, dest, ensure_ascii=False, indent=2)
        dest.write("\n")
    print(json.dumps({"status": result["status"], "clock_or_service_changed": False}))
    return {"NO_STEP_OBSERVED": 0, "ANOMALY": 1, "INCOMPLETE": 2}[result["status"]]


if __name__ == "__main__":
    raise SystemExit(main())
