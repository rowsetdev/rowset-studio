#!/usr/bin/env python3
"""Run Rowset's existing live tests one engine at a time and retain go test JSON.

Use --existing with the ports documented in README.md for a prestarted lab.
Without it, a disposable, loopback-only Docker container is created per engine.
"""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "reports" / "audit-evidence"
PASSWORD = "RowsetAudit2026!Pass"
ENGINES = {
    "postgres": ("postgres:16", 55432, 5432, "ROWSET_MATRIX_POSTGRES_PASSWORD", "TestLiveLargePostgresSchema|TestLiveEngineDatatypeSyntaxAndLargeStreamMatrix|TestLiveExplainReturnsReadablePlans|TestLivePinnedSessionsCommitAndRollbackAcrossEngines|TestLiveDatabaseObjects|TestLiveAllTypes|TestLiveObjectDDL|TestLiveTableExport"),
    "mysql": ("mysql:8.4", 53306, 3306, "ROWSET_MATRIX_MYSQL_PASSWORD", "TestLiveEngineDatatypeSyntaxAndLargeStreamMatrix|TestLiveExplainReturnsReadablePlans|TestLivePinnedSessionsCommitAndRollbackAcrossEngines|TestLiveDatabaseObjects|TestLiveAllTypes|TestLiveObjectDDL|TestLiveTableExport"),
    "mariadb": ("mariadb:11", 53307, 3306, "ROWSET_MATRIX_MARIADB_PASSWORD", "TestLiveExplainReturnsReadablePlans|TestLivePinnedSessionsCommitAndRollbackAcrossEngines|TestLiveDatabaseObjects|TestLiveAllTypes|TestLiveObjectDDL|TestLiveTableExport"),
    "mssql": ("mcr.microsoft.com/mssql/server:2022-latest", 51433, 1433, "ROWSET_MATRIX_MSSQL_PASSWORD", "TestLiveEngineDatatypeSyntaxAndLargeStreamMatrix|TestLiveExplainReturnsReadablePlans|TestLivePinnedSessionsCommitAndRollbackAcrossEngines|TestLiveMSSQLCompositeFKAndCoveringIndexDDL|TestLiveDatabaseObjects|TestLiveAllTypes|TestLiveObjectDDL|TestLiveTableExport"),
    "cockroachdb": ("cockroachdb/cockroach:latest-v24.1", 26257, 26257, "ROWSET_TEST_COCKROACHDB_HOST", "TestLiveCockroachDB"),
    "clickhouse": ("clickhouse/clickhouse-server:25.8", 59000, 9000, "ROWSET_TEST_CLICKHOUSE_HOST", "TestLiveAdditionalServers"),
    "mongodb": ("mongo:8.0", 57017, 27017, "ROWSET_TEST_MONGODB_HOST", "TestLiveAdditionalServers"),
    "redis": ("redis:7", 6379, 6379, "ROWSET_TEST_REDIS_HOST", "TestLiveRedis"),
    "valkey": ("valkey/valkey:8.0", 6380, 6379, "ROWSET_TEST_REDIS_HOST", "TestLiveRedis"),
    "cassandra": ("cassandra:5", 9042, 9042, "ROWSET_TEST_CASSANDRA_HOST", "TestLiveCassandra"),
    "elasticsearch": ("docker.elastic.co/elasticsearch/elasticsearch:8.15.0", 9200, 9200, "ROWSET_TEST_ELASTICSEARCH_HOST", "TestLiveElasticsearch"),
}
LOCKED_IMAGES = json.loads((Path(__file__).with_name("images.lock.json")).read_text())


def call(*args, check=True):
    result = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if check and result.returncode:
        raise RuntimeError(f"{' '.join(args[:3])} failed: {result.stdout[-500:].strip()}")
    return result


def wait_port(port):
    for _ in range(120):
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                return
        except OSError:
            time.sleep(2)
    raise RuntimeError(f"port {port} did not open in 240 seconds")


def provision(engine, image, port, native):
    name = f"rowset-audit-{engine}"
    call("docker", "rm", "-f", name, check=False)
    cmd = ["docker", "run", "-d", "--name", name, "--restart=no", "--memory=1400m",
           "-p", f"127.0.0.1:{port}:{native}"]
    if engine == "postgres":
        cmd += ["-e", f"POSTGRES_PASSWORD={PASSWORD}", "-e", "POSTGRES_DB=rowset_e2e"]
    elif engine == "mysql":
        cmd += ["-e", f"MYSQL_ROOT_PASSWORD={PASSWORD}", "-e", "MYSQL_DATABASE=rowset_e2e"]
    elif engine == "mariadb":
        cmd += ["-e", f"MARIADB_ROOT_PASSWORD={PASSWORD}", "-e", "MARIADB_DATABASE=rowset_e2e"]
    elif engine == "mssql":
        cmd += ["-e", "ACCEPT_EULA=Y", "-e", f"MSSQL_SA_PASSWORD={PASSWORD}"]
    elif engine == "clickhouse":
        cmd += ["-e", "CLICKHOUSE_DB=default", "-e", f"CLICKHOUSE_PASSWORD={PASSWORD}"]
    elif engine == "cassandra":
        cmd += ["-e", "MAX_HEAP_SIZE=512M", "-e", "HEAP_NEWSIZE=100M"]
    elif engine == "elasticsearch":
        cmd += ["-e", "discovery.type=single-node", "-e", "xpack.security.enabled=false",
                "-e", "ES_JAVA_OPTS=-Xms512m -Xmx512m"]
    elif engine == "cockroachdb":
        cmd += ["--entrypoint", "/cockroach/cockroach"]
    cmd.append(image)
    if engine == "cockroachdb":
        cmd += ["start-single-node", "--insecure", "--listen-addr=:26257"]
    call(*cmd)
    wait_port(port)
    # A listening socket appears before these engines finish catalog recovery.
    if engine == "cassandra":
        for _ in range(90):
            probe = call("docker", "exec", name, "cqlsh", "-e", "SELECT release_version FROM system.local", check=False)
            if probe.returncode == 0:
                break
            time.sleep(2)
        else:
            raise RuntimeError("Cassandra CQL did not become query-ready")
    elif engine in ("cockroachdb", "elasticsearch"):
        time.sleep(12)
    if engine == "mssql":
        for _ in range(60):
            result = call("docker", "exec", name, "/opt/mssql-tools18/bin/sqlcmd", "-S", "localhost",
                          "-U", "sa", "-P", PASSWORD, "-C", "-Q",
                          "IF DB_ID('rowset_e2e') IS NULL CREATE DATABASE rowset_e2e", check=False)
            if result.returncode == 0:
                break
            time.sleep(2)
        else:
            raise RuntimeError("SQL Server test database could not be created")
    return name


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", choices=[*ENGINES, "sqlite", "duckdb", "all"])
    parser.add_argument("--existing", action="store_true", help="use existing localhost servers; supply their passwords via ROWSET_* variables")
    args = parser.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    chosen = [*ENGINES, "sqlite", "duckdb"] if args.engine == "all" else [args.engine]
    failed = False
    for engine in chosen:
        container = None
        env = os.environ.copy()
        env["GOCACHE"] = env.get("GOCACHE", "/tmp/rowset-audit-gocache")
        packages, pattern = ["./internal/engine"], ""
        try:
            if engine in ENGINES:
                image, port, native, variable, pattern = ENGINES[engine]
                if not args.existing:
                    container = provision(engine, LOCKED_IMAGES[engine], port, native)
                elif not os.getenv(variable):
                    raise RuntimeError(f"{variable} is required with --existing")
                if variable.endswith("_PASSWORD"):
                    env[variable] = os.getenv(variable, PASSWORD)
                else:
                    env[variable] = "127.0.0.1"
                if engine in ("clickhouse", "mongodb"):
                    env[f"ROWSET_MATRIX_{engine.upper()}_PORT"] = str(port)
                elif engine in ("cockroachdb", "redis", "valkey", "cassandra", "elasticsearch"):
                    key = "REDIS" if engine == "valkey" else engine.upper()
                    env[f"ROWSET_TEST_{key}_PORT"] = str(port)
                if engine == "clickhouse":
                    env["ROWSET_TEST_CLICKHOUSE_PASSWORD"] = os.getenv("ROWSET_TEST_CLICKHOUSE_PASSWORD", PASSWORD)
                if engine == "valkey":
                    # The Redis protocol test is also the Valkey wire-compatibility probe.
                    pattern = "TestLiveRedis"
                if engine in ("postgres", "mysql", "mariadb", "mssql"):
                    packages.append("./internal/api")
            else:
                pattern = "TestSQLiteFileQuerySchemaDDLAndRollback" if engine == "sqlite" else "TestDuckDBQuerySchemaDDLAndPrecision"
            result = subprocess.run(["go", "test", *packages, "-run", f"^({pattern})$", "-count=1", "-timeout=15m", "-json"],
                                    cwd=ROOT / "rowset-core", env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            (OUT / f"{engine}.jsonl").write_text(result.stdout)
            events = []
            for line in result.stdout.splitlines():
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if event.get("Test") and event.get("Action") in ("pass", "fail", "skip"):
                    events.append({"test": event["Test"], "result": event["Action"], "elapsed": event.get("Elapsed")})
            subtest = "sqlserver" if engine == "mssql" else engine
            relevant = [e for e in events if e["test"] == pattern or e["test"].endswith("/" + subtest)
                        or (engine in ("clickhouse", "mongodb") and e["test"].endswith("/" + engine))]
            if not relevant:
                relevant = [e for e in events if "/" not in e["test"]]
            status = "pass" if result.returncode == 0 and relevant and all(e["result"] == "pass" for e in relevant) else "fail"
            record = {"engine": engine, "status": status, "image": LOCKED_IMAGES[engine] if engine in ENGINES and not args.existing else (ENGINES[engine][0] if engine in ENGINES else None),
                      "mode": "existing" if args.existing else "disposable", "tests": events, "exit_code": result.returncode}
            (OUT / f"{engine}.json").write_text(json.dumps(record, indent=2) + "\n")
            print(f"{engine}: {status} ({len(events)} test results); evidence: {OUT / (engine + '.jsonl')}", flush=True)
            failed |= status != "pass"
        except Exception as error:
            failed = True
            (OUT / f"{engine}.json").write_text(json.dumps({"engine": engine, "status": "setup-failed", "error": str(error)}) + "\n")
            print(f"{engine}: setup-failed: {error}", file=sys.stderr, flush=True)
        finally:
            if container:
                call("docker", "rm", "-f", container, check=False)
            elif not args.existing and engine in ENGINES:
                # Also clean a container created before a readiness failure.
                call("docker", "rm", "-f", f"rowset-audit-{engine}", check=False)
    return int(failed)


if __name__ == "__main__":
    raise SystemExit(main())
