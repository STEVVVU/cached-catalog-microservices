"""Owned local gRPC service benchmark. Build binaries before invoking this driver."""
import argparse
from collections import Counter
import copy
import contextlib
import csv
import gzip
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import shutil
import socket
import statistics
import subprocess
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SERVICE = ROOT / "src/productcatalogservice"
BASELINE = "a0bd7a086fab59c81b73c00e4c3b8d0d62129a7c"
CATALOG_HASHES = {9: "81ba327e2b676f9e823c605bd7d9cbaae2904a9bbb3ce56c40414a19322bd0a7", 500: "44ddd89e5224e5a50f85ab96e03199c9da3f328f62946b6a410b951f03a69649", 5000: "e8c0d8d50355385602fb7f18a55678871c288b3c3a8873494e1a16328758f1a0"}
FOCUSED_CACHE_PLAN = {"catalog_products": 500, "replicas": 1, "ttl": "60s", "rate": 100, "duration_s": 20, "repeats": 5, "modes": ["none", "ttl", "lease"], "vus": 64, "mix": "50% List, 40% Get, 10% Search among started iterations"}
FOCUSED_COUNTER_FIELDS = {"catalog_started": "started", "catalog_successful": "successful", "catalog_failed": "failed", "catalog_timeouts": "timeouts", "catalog_correctness_errors": "correctness_errors", "dropped_iterations": "dropped_iterations"}
FOCUSED_TRENDS = ("grpc_req_duration{status:0}", "iteration_duration", "group_duration")
FOCUSED_RECEIPTS = (".oracle.csv", ".oracle.json", ".k6-summary.json", ".k6-raw.gz", ".client.stdout.log", ".client.stderr", "-db.log", "-gateway0.log")


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def gateway_ports():
    with socket.socket() as grpc, socket.socket() as admin:
        grpc.bind(("127.0.0.1", 0))
        admin.bind(("127.0.0.1", 0))
        return grpc.getsockname()[1], admin.getsockname()[1]


def resources(process):
    base = Path(f"/proc/{process.pid}")
    try:
        stat = (base / "stat").read_text().split(")", 1)[1].split()
        return {"pid": process.pid, "user_ticks": int(stat[11]), "sys_ticks": int(stat[12]), "rss_pages": int(stat[21]), "io": (base / "io").read_text(), "status": (base / "status").read_text()}
    except (FileNotFoundError, PermissionError):
        return {"pid": process.pid, "exit": process.poll()}


def focused_cache_receipt_errors(result, output):
    errors = []
    expected = {(mode, repeat) for repeat in range(FOCUSED_CACHE_PLAN["repeats"]) for mode in FOCUSED_CACHE_PLAN["modes"]}
    samples = result.get("samples", [])
    actual = {(sample.get("mode"), sample.get("repeat")) for sample in samples}
    if actual != expected or len(samples) != len(expected):
        errors.append(f"expected exactly 15 unique mode/repeat cells, found {len(samples)} samples and {len(actual)} unique cells")
    for repeat in range(FOCUSED_CACHE_PLAN["repeats"]):
        order = [sample.get("mode") for sample in samples if sample.get("repeat") == repeat]
        expected_order = FOCUSED_CACHE_PLAN["modes"] if repeat % 2 == 0 else list(reversed(FOCUSED_CACHE_PLAN["modes"]))
        if order != expected_order:
            errors.append(f"repeat {repeat}: cache arms were not run in the declared alternating order")
    if not result.get("cleanup", {}).get("owned_processes_stopped") or not result.get("cleanup", {}).get("scratch_removed"):
        errors.append("owned processes or scratch workspace remain after cleanup")
    identity = result.get("focused_cache_identity", {})
    if not identity.get("source_sha256") or set(identity.get("binary_sha256", {})) != {"productcatalogservice", "catalogctl"}:
        errors.append("current source or normal-build binary hashes are missing")
    if not identity.get("framework_sha256") or not identity.get("k6_binary_sha256"):
        errors.append("benchmark framework hashes are missing")
    hash_values = list(identity.get("source_sha256", {}).values()) + list(identity.get("binary_sha256", {}).values()) + list(identity.get("framework_sha256", {}).values()) + [identity.get("k6_binary_sha256")]
    if any(not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value) for value in hash_values):
        errors.append("source, binary, or framework SHA-256 identity is malformed")
    required_sources = {"src/productcatalogservice/go.mod", "src/productcatalogservice/go.sum", "src/productcatalogservice/products.json"}
    if not required_sources.issubset(identity.get("source_sha256", {})) or not any(path.endswith(".go") for path in identity.get("source_sha256", {})):
        errors.append("product source hash inventory is incomplete")
    if not re.fullmatch(r"[0-9a-f]{40}", identity.get("source_sha", "")) or identity.get("source_sha") != result.get("source_sha"):
        errors.append("source revision identity is malformed")
    required_frameworks = {"scripts/catalog-benchmark.py", "scripts/catalog-k6.js", "scripts/catalog-oracle.mjs", "protos/demo.proto", ".github/workflows/catalog-k6.yml"}
    if not required_frameworks.issubset(identity.get("framework_sha256", {})):
        errors.append("benchmark framework hash inventory is incomplete")
    expected_measurement = {**FOCUSED_CACHE_PLAN, "preset": "focused-cache", "fixture_sha256": CATALOG_HASHES[500], "order": "none/ttl/lease on even repeats, reversed on odd repeats", "duration_claim": "moderate fixed offered load, not a capacity claim", "native_latency_claim_requires": "all successful RPCs have native status-zero duration samples"}
    if identity.get("measurement") != expected_measurement:
        errors.append("focused-cache measurement identity does not match its fixed plan")
    build_info = identity.get("go_build_info", {})
    if set(build_info) != {"productcatalogservice", "catalogctl"} or any(not isinstance(info, str) or "-race=true" in info or "-tags=benchmark" in info for info in build_info.values()):
        errors.append("normal uninstrumented Go build metadata is missing or invalid")
    if not str(identity.get("k6_version", "")).startswith("k6 v2.3.0"):
        errors.append("native k6 version does not match the pinned framework")
    for sample in samples:
        label = sample.get("label", "unknown")
        try:
            errors.extend(audit_focused_cache_sample(sample, output))
        except Exception as error:
            errors.append(f"{label}: raw receipt audit failed: {type(error).__name__}: {error}")
    try:
        saved = json.loads((output / "result.json").read_text(encoding="utf-8"))
        receipt_fields = ("label", "mode", "repeat", "catalog_products", "replicas", "ttl", "offered_rate", "duration_s", "planned_offers", "offered", "started", "successful", "failed", "rejected", "dropped_iterations", "timeouts", "correctness_errors", "interrupted", "native_status_zero_count", "native_duration_gap", "catalog_sha256", "p50_ms", "p95_ms", "p99_ms", "workload")
        saved_samples = saved.get("samples", [])
        samples_match = len(saved_samples) == len(samples) and all(all(before.get(field) == after.get(field) for field in receipt_fields) for before, after in zip(saved_samples, samples))
        if saved.get("source_sha") != result.get("source_sha") or saved.get("focused_cache_identity") != identity or not samples_match or saved.get("cleanup") != result.get("cleanup") or saved.get("measurement_status") != "inconclusive":
            errors.append("saved result.json does not match the audited source, plan, cells, and cleanup state")
    except Exception as error:
        errors.append(f"saved result.json is invalid: {type(error).__name__}: {error}")
    return errors


def audit_focused_cache_sample(sample, output):
    label = sample["label"]
    errors = []
    artifacts = {suffix: output / f"{label}{suffix}" for suffix in FOCUSED_RECEIPTS}
    receipts = {}
    for suffix, path in artifacts.items():
        if not path.is_file():
            raise FileNotFoundError(path.name)
        with path.open("rb") as stream:
            receipts[path.name] = {"size_bytes": path.stat().st_size, "sha256": hashlib.file_digest(stream, "sha256").hexdigest()}
    sample["artifact_receipts"] = receipts

    workload = sample.get("workload", {})
    if any(sample.get(field) != value for field, value in (("catalog_products", 500), ("replicas", 1), ("ttl", "60s"), ("offered_rate", 100), ("duration_s", 20))) or any(workload.get(field) != value for field, value in (("rate", 100), ("seconds", 20), ("vus", 64), ("products", 500), ("planned_offers", 2000), ("mix", FOCUSED_CACHE_PLAN["mix"]))):
        errors.append(f"{label}: workload does not match the focused-cache plan")
    if sample.get("catalog_sha256") != CATALOG_HASHES[500]:
        errors.append(f"{label}: fixture hash does not match the frozen 500-product catalog")

    oracle = json.loads(artifacts[".oracle.json"].read_text(encoding="utf-8"))
    oracle_fields = ("offered", "successful", "failed", "rejected", "timeouts", "correctness_errors", "attempted", "codes")
    sample["oracle"] = {key: oracle.get(key) for key in oracle_fields}
    if sample["oracle"] != {"offered": 1, "successful": 1, "failed": 0, "rejected": 0, "timeouts": 0, "correctness_errors": 0, "attempted": 1, "codes": {"OK": 1}}:
        errors.append(f"{label}: exact one-operation Go protobuf oracle did not pass")
    with artifacts[".oracle.csv"].open(newline="", encoding="utf-8") as stream:
        reader = csv.DictReader(stream)
        rows = list(reader)
    if reader.fieldnames != ["index", "scheduled_ms", "start_ms", "latency_from_schedule_ms", "rpc_ms", "validation_ms", "code", "response_bytes"] or len(rows) != 1 or rows[0].get("index") != "0" or rows[0].get("code") != "OK" or int(rows[0].get("response_bytes", "0")) <= 0:
        errors.append(f"{label}: Go oracle CSV does not contain exactly one correct response row")

    summary = json.loads(artifacts[".k6-summary.json"].read_text(encoding="utf-8"))
    native = summary["native"]
    native_workload = summary["workload"]
    for field, value in (("rate", 100), ("seconds", 20), ("vus", 64), ("products", 500), ("planned_offers", 2000), ("mix", FOCUSED_CACHE_PLAN["mix"])):
        if native_workload.get(field) != value:
            errors.append(f"{label}: k6 summary workload field {field} is unexpected")
    if len(native_workload.get("targets", [])) != 1 or not re.fullmatch(r"127\.0\.0\.1:[0-9]+", native_workload["targets"][0]):
        errors.append(f"{label}: k6 summary target is not the owned loopback gateway")
    metrics = native["metrics"]
    raw_counters, raw_codes, raw_methods = Counter(), Counter(), Counter()
    trends = {name: [] for name in FOCUSED_TRENDS}
    raw_points = 0
    with gzip.open(artifacts[".k6-raw.gz"], "rb") as stream:
        for line in stream:
            record = json.loads(line)
            if record.get("type") != "Point":
                continue
            raw_points += 1
            name, data = record["metric"], record["data"]
            value, tags = data["value"], data["tags"]
            if not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
                raise ValueError(f"invalid {name} point value")
            if name in FOCUSED_COUNTER_FIELDS or name == "catalog_completed":
                raw_counters[name] += value
            if name == "catalog_started":
                raw_methods[tags["method"]] += value
            if name == "catalog_rpc_codes":
                raw_codes[tags["code"]] += value
            if name == "grpc_req_duration" and tags.get("status") == "0":
                trends["grpc_req_duration{status:0}"].append(value)
            elif name in ("iteration_duration", "group_duration"):
                trends[name].append(value)

    counter_values = {}
    for metric, field in FOCUSED_COUNTER_FIELDS.items():
        count = raw_counters[metric]
        native_values = metrics.get(metric, {}).get("values", {})
        if "count" not in native_values:
            if count != 0 or metric not in {"catalog_failed", "catalog_timeouts", "dropped_iterations"}:
                errors.append(f"{label}: native summary omits nonzero or required {metric} count")
        elif count != native_values["count"]:
            errors.append(f"{label}: raw {metric} count disagrees with native summary")
        counter_values[field] = count
    completed = raw_counters["catalog_completed"]
    if completed != metrics["catalog_completed"]["values"]["count"]:
        errors.append(f"{label}: raw completion count disagrees with native summary")
    expected_counts = {"started": 2000, "successful": 2000, "failed": 0, "timeouts": 0, "correctness_errors": 0, "dropped_iterations": 0}
    if counter_values != expected_counts or completed != 2000 or raw_methods != {"ListProducts": 1000, "GetProduct": 800, "SearchProducts": 200} or raw_codes != {"0": 2000}:
        errors.append(f"{label}: raw outcome, completion, RPC-code, or method counts do not match the focused plan")
    sample_counts = {**counter_values, "interrupted": counter_values["started"] - completed, "offered": counter_values["started"] + counter_values["dropped_iterations"], "planned_offers": native_workload.get("planned_offers")}
    sample_counts["rejected"] = counter_values["dropped_iterations"]
    if any(sample.get(field) != value for field, value in sample_counts.items()):
        errors.append(f"{label}: saved outcome counts disagree with raw metric points")
    expected_codes = {"OK": counter_values["successful"], "FAILED": counter_values["failed"], "DROPPED": counter_values["dropped_iterations"], "INTERRUPTED": counter_values["started"] - completed}
    if sample.get("codes") != expected_codes:
        errors.append(f"{label}: saved RPC outcome codes disagree with raw metric points")
    if sample.get("native_status_zero_count") != len(trends[FOCUSED_TRENDS[0]]) or sample.get("native_duration_gap") != counter_values["successful"] - len(trends[FOCUSED_TRENDS[0]]):
        errors.append(f"{label}: saved native successful-RPC duration coverage disagrees with raw points")

    checked_trends = {}
    for name, values in trends.items():
        if not values or any(not math.isfinite(value) or value < 0 for value in values):
            errors.append(f"{label}: raw trend {name} is missing or invalid")
            continue
        values.sort()
        summary_values = metrics[name]["values"]
        if len(values) != summary_values.get("count") or len(values) != 2000:
            errors.append(f"{label}: raw trend {name} count disagrees with native summary or plan")
            continue
        for key, fraction in (("med", .5), ("p(95)", .95), ("p(99)", .99)):
            position = (len(values) - 1) * fraction
            lower, upper = math.floor(position), math.ceil(position)
            calculated = values[lower] + (values[upper] - values[lower]) * (position - lower)
            reported = summary_values.get(key)
            if not isinstance(reported, (int, float)) or not math.isclose(calculated, reported, rel_tol=1e-9, abs_tol=1e-7):
                errors.append(f"{label}: raw trend {name} {key} disagrees with native summary")
            sample_key = {"med": "p50_ms", "p(95)": "p95_ms", "p(99)": "p99_ms"}.get(key) if name == FOCUSED_TRENDS[0] else None
            if sample_key and not math.isclose(calculated, sample.get(sample_key, -1), rel_tol=1e-9, abs_tol=1e-7):
                errors.append(f"{label}: saved {sample_key} disagrees with raw successful-RPC durations")
        checked_trends[name] = {"count": len(values), "p50_ms": summary_values["med"], "p95_ms": summary_values["p(95)"], "p99_ms": summary_values["p(99)"]}
    if not raw_points:
        errors.append(f"{label}: native k6 stream contains no metric points")
    sample["raw_metric_points"] = raw_points
    sample["native_rpc_codes"] = dict(raw_codes)
    sample["observed_method_counts"] = dict(raw_methods)
    sample["audited_native_trends"] = checked_trends
    if not sample.get("resources") or len(sample.get("metrics_before", [])) != 1 or len(sample.get("metrics_after", [])) != 1:
        errors.append(f"{label}: resource or server metric samples are missing")
    return errors


def focused_cache_identity(args, k6_path):
    def digest(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    source_paths = sorted(path for path in SERVICE.rglob("*.go") if not path.name.endswith("_test.go"))
    source_paths += [SERVICE / "go.mod", SERVICE / "go.sum", SERVICE / "products.json"]
    source_hashes = {path.relative_to(ROOT).as_posix(): digest(path) for path in source_paths}
    framework_paths = [ROOT / "scripts/catalog-benchmark.py", ROOT / "scripts/catalog-k6.js", ROOT / "scripts/catalog-oracle.mjs", ROOT / "protos/demo.proto", ROOT / ".github/workflows/catalog-k6.yml"]
    framework_hashes = {path.relative_to(ROOT).as_posix(): digest(path) for path in framework_paths}
    build_info = {path.stem: subprocess.check_output(["go", "version", "-m", str(path)], text=True).strip() for path in (args.server, args.client)}
    if any("-race=true" in info or "-tags=benchmark" in info for info in build_info.values()):
        raise RuntimeError("focused cache measurement requires normal uninstrumented product binaries")
    return {
        "measurement": {**FOCUSED_CACHE_PLAN, "preset": "focused-cache", "fixture_sha256": CATALOG_HASHES[500], "order": "none/ttl/lease on even repeats, reversed on odd repeats", "duration_claim": "moderate fixed offered load, not a capacity claim", "native_latency_claim_requires": "all successful RPCs have native status-zero duration samples"},
        "source_sha": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "source_sha256": source_hashes,
        "binary_sha256": {path.stem: digest(path) for path in (args.server, args.client)},
        "go_build_info": build_info,
        "k6_version": subprocess.check_output(["k6", "version"], text=True).strip(),
        "k6_binary_sha256": digest(k6_path),
        "framework_sha256": framework_hashes,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--server", type=Path, default=ROOT / "bin/productcatalogservice")
    parser.add_argument("--client", type=Path, default=ROOT / "bin/catalogctl")
    parser.add_argument("--smoke", action="store_true")
    parser.add_argument("--profile", action="store_true")
    parser.add_argument("--profile-expiry", action="store_true", help="profile the observed 5000-product/1ms-expiry adverse case")
    parser.add_argument("--k6", action="store_true", help="use k6 native gRPC arrival scheduling and statistics")
    parser.add_argument("--focused-cache", action="store_true", help="run the bounded 500-product current-build cache comparison")
    parser.add_argument("--baseline-server", type=Path)
    parser.add_argument("--baseline-source", type=Path)
    parser.add_argument("--baseline-archive", type=Path)
    args = parser.parse_args()
    if args.k6 and args.profile:
        parser.error("k6 measurement and the existing Go reference profile are separate runs")
    if args.profile_expiry and (not args.profile or args.smoke):
        parser.error("--profile-expiry requires --profile and cannot use --smoke")
    paired = args.baseline_server is not None
    if any([args.baseline_server, args.baseline_source, args.baseline_archive]) and not all([args.baseline_server, args.baseline_source, args.baseline_archive]):
        parser.error("all three baseline inputs are required")
    if paired and (not args.k6 or args.profile):
        parser.error("paired comparison requires native k6 without profiling")
    if args.focused_cache and (not args.k6 or args.smoke or args.profile or args.profile_expiry or paired):
        parser.error("--focused-cache requires native k6 and cannot combine with smoke, profiling, or paired-build inputs")
    args.server, args.client, args.output = args.server.resolve(), args.client.resolve(), args.output.resolve()
    if shutil.disk_usage(ROOT).free < 10 * 1024**3:
        raise RuntimeError("10 GiB disk reserve required")
    args.output.mkdir(parents=True, exist_ok=False)
    result = {"source_sha": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(), "platform": platform.platform(), "cpu_count": os.cpu_count(), "load": os.getloadavg() if hasattr(os, "getloadavg") else None, "cpu": Path("/proc/cpuinfo").read_text() if Path("/proc/cpuinfo").exists() else platform.processor(), "clock_ticks": os.sysconf("SC_CLK_TCK") if hasattr(os, "sysconf") else None, "page_size": os.sysconf("SC_PAGE_SIZE") if hasattr(os, "sysconf") else None, "binary_sha256": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in [args.server, args.client]}, "smoke": args.smoke, "profile": args.profile, "samples": [], "events": [], "passed": False}
    owned, logs = [], []
    servers = {"current": args.server} if not paired else {"baseline": args.baseline_server.resolve(), "candidate": args.server}
    result["load_generator"] = "k6" if args.k6 else "catalogctl"
    if args.profile:
        result["profile_expiry"] = args.profile_expiry
        result["diagnostic_policy"] = "Separate CPU/mutex/block profiles; all instrumented timing excluded from performance comparisons. Expiry profile uses the frozen 5000-product, one-gateway, 1ms TTL, 1000-offer/s adverse cell for a bounded 10s diagnostic window."
    if args.k6:
        result["k6_version"] = subprocess.check_output(["k6","version"],text=True).strip()
    if args.focused_cache:
        result["focused_cache_identity"] = focused_cache_identity(args, Path(shutil.which("k6")).resolve())
        result["measurement_status"] = "inconclusive"

    def start(name, env):
        log = (args.output / f"{name}.log").open("wb")
        logs.append(log)
        profile = {"CATALOG_BENCH_PROFILE": str(args.output / f"{name}.pprof")} if args.profile else {}
        process = subprocess.Popen([str(servers[build])], cwd=SERVICE, env=os.environ | {"DISABLE_PROFILER": "1", "ENABLE_TRACING": "0", "EXTRA_LATENCY": "0s"} | env | profile, stdout=log, stderr=log)
        owned.append(process)
        return process

    def stop(process):
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)

    def ctl(arguments, address, timeout=10):
        return subprocess.check_output([str(args.client), *arguments], cwd=SERVICE, env=os.environ | {"CATALOGCTL_GRPC_ADDR": address}, timeout=timeout, stderr=subprocess.STDOUT)

    @contextlib.contextmanager
    def workspace():
        directory = tempfile.TemporaryDirectory(prefix="catalog-benchmark-")
        try:
            yield directory.name
        finally:
            for process in owned:
                stop(process)
            directory.cleanup()

    def wait_catalog(process, address):
        until = time.monotonic()+15
        last = ""
        while time.monotonic() < until:
            if process.poll() is not None:
                raise RuntimeError("owned catalog process exited before readiness")
            try:
                return json.loads(ctl(["list"], address, timeout=4))
            except (subprocess.SubprocessError, json.JSONDecodeError) as error:
                last = str(error)
                time.sleep(.1)
        raise RuntimeError(f"catalog readiness failed: {last}")

    def metric(admin):
        with urllib.request.urlopen(f"http://127.0.0.1:{admin}/metrics", timeout=3) as response:
            return response.read().decode()

    try:
        if paired:
            digest = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
            source = args.baseline_source.resolve()
            baseline_service = source / "src/productcatalogservice"
            runtime_files = sorted(path.relative_to(SERVICE) for path in SERVICE.rglob("*.go") if not path.name.endswith("_test.go"))
            assert runtime_files == sorted(path.relative_to(baseline_service) for path in baseline_service.rglob("*.go") if not path.name.endswith("_test.go")), "runtime source inventory changed"
            changed = [str(path) for path in runtime_files if digest(SERVICE/path) != digest(baseline_service/path)]
            assert changed == ["catalog_cache.go"], f"unexpected runtime changes: {changed}"
            fixed_inputs = ["src/productcatalogservice/go.mod", "src/productcatalogservice/go.sum", "src/productcatalogservice/products.json", "protos/demo.proto", "scripts/catalog-k6.js", "scripts/catalog-oracle.mjs"]
            for name in fixed_inputs:
                assert digest(ROOT/name) == digest(source/name), f"unmatched workload/dependency input: {name}"
            hashes = {name: digest(binary) for name, binary in servers.items()}
            assert len(set(hashes.values())) == 2, "baseline and candidate binaries are identical"
            build_info = {name: subprocess.check_output(["go", "version", "-m", str(binary)], text=True) for name, binary in servers.items()}
            assert build_info["baseline"].splitlines()[1:] == build_info["candidate"].splitlines()[1:], "unmatched embedded Go build/dependency settings"
            for info in build_info.values():
                assert info.splitlines()[0].split()[-1] == "go1.27.3", "unexpected embedded compiler version"
                assert "-trimpath=true" in info and "vcs.revision" not in info, "require identical normal -trimpath -buildvcs=false builds"
                assert "-race=true" not in info and "-tags=benchmark" not in info, "instrumented server cannot be measured"
            result["comparison"] = {"baseline_sha":BASELINE,"archive_sha256":digest(args.baseline_archive),"binary_sha256":hashes,"runtime_source_sha256":{build_name:{str(path):digest(root/path) for path in runtime_files} for build_name,root in [("baseline",baseline_service),("candidate",SERVICE)]},"fixed_input_sha256":{name:digest(ROOT/name) for name in fixed_inputs},"go_version":subprocess.check_output(["go","version"],text=True).strip(),"build_info":build_info,"common_arguments":["build","-trimpath","-buildvcs=false"],"policy":"Same host, independent baseline/candidate normal builds and fresh owned service/database lifecycle per arm; original full native k6 matrix, five reversed rounds, oracle before each trial. Instrumentation disabled."}
        with workspace() as scratch:
            work = Path(scratch)
            sizes = [9] if args.smoke else [9, 500, 5000]
            repeats = 1 if args.smoke or args.profile else 5
            scenarios = [(n, replicas, ttl, rate, 6) for n in sizes for replicas in ([1] if args.smoke else [1, 3]) for ttl in (["60s"] if args.smoke else ["60s", "1ms"]) for rate in ([10] if args.smoke else [100, 1000, 5000])]
            if args.smoke:
                scenarios = [(9, 1, "60s", 10, 1)]
            elif args.profile:
                scenarios = [(5000, 1, "1ms", 1000, 10)] if args.profile_expiry else [(500, 1, "60s", 1000, 10)]
            elif args.focused_cache:
                plan = FOCUSED_CACHE_PLAN
                scenarios = [(plan["catalog_products"], plan["replicas"], plan["ttl"], plan["rate"], plan["duration_s"])]
            else:
                scenarios += [(500, 3, "50ms", 1000, 60)]
            for count, replicas, ttl, rate, duration in scenarios:
                for repeat in range(1 if duration == 60 else repeats):
                    modes = [(mode, build_name) for mode in ["none", "ttl", "lease"] for build_name in servers]
                    if repeat % 2:
                        modes.reverse()
                    for mode, build in modes:
                        label = f"n{count}-r{replicas}-ttl{ttl}-rate{rate}-rep{repeat}-{mode}"
                        if paired:
                            label += f"-{build}"
                        fixture = work / f"{label}.json"
                        template = json.loads((SERVICE / "products.json").read_text())["products"][0]
                        products = []
                        for i in range(count):
                            product = copy.deepcopy(template)
                            product["id"] = f"bench-{i:06d}"
                            product["name"] = f"benchmark-needle-{i}" if i % 100 == 0 else f"product-{i}"
                            products.append(product)
                        catalog = {"products": products}
                        fixture.write_text(json.dumps(catalog))
                        fixture_sha256 = hashlib.sha256(fixture.read_bytes()).hexdigest()
                        if args.focused_cache:
                            assert fixture_sha256 == CATALOG_HASHES[500], "focused-cache fixture changed"
                        if paired:
                            assert fixture_sha256 == CATALOG_HASHES[count], "original catalog changed"
                        if args.profile_expiry:
                            assert hashlib.sha256(fixture.read_bytes()).hexdigest() == "e8c0d8d50355385602fb7f18a55678871c288b3c3a8873494e1a16328758f1a0", "original adverse-cell catalog changed"
                        dbport = port()
                        dbenv = {"CATALOG_DB_MODE": "1", "CATALOG_DB_BIND_ADDRESS": "127.0.0.1", "CATALOG_DB_PORT": str(dbport), "CATALOG_DB_PATH": str(work / f"{label}.db"), "CATALOG_SEED_PATH": str(fixture)}
                        db = start(f"{label}-db", dbenv)
                        wait_catalog(db, f"127.0.0.1:{dbport}")
                        gateways, addresses, admins, gateway_env = [], [], [], []
                        for replica in range(replicas):
                            gp, admin = gateway_ports()
                            env = {"PORT": str(gp), "CATALOG_BACKEND_ADDR": f"127.0.0.1:{dbport}", "CATALOG_CACHE_MODE": mode, "CATALOG_CACHE_TTL": ttl, "CATALOG_ADMIN_PORT": str(admin), "CATALOG_BACKEND_TIMEOUT": "2s"}
                            gateway = start(f"{label}-gateway{replica}", env)
                            wait_catalog(gateway, f"127.0.0.1:{gp}")
                            gateways.append(gateway)
                            addresses.append(f"127.0.0.1:{gp}")
                            admins.append(admin)
                            gateway_env.append(env)
                        if args.k6:
                            oracle = ctl(["load", "-targets", ",".join(addresses), "-catalog", str(fixture), "-rate", "10", "-duration", "100ms", "-concurrency", "1", "-out", str(args.output / f"{label}.oracle.csv")], addresses[0])
                            (args.output / f"{label}.oracle.json").write_bytes(oracle)
                            if args.focused_cache:
                                oracle_result = json.loads(oracle)
                        before = [metric(admin) for admin in admins]
                        command = [str(args.client), "load", "-targets", ",".join(addresses), "-catalog", str(fixture), "-rate", str(rate), "-duration", f"{duration}s", "-concurrency", "64", "-out", str(args.output / f"{label}.csv")]
                        load_env = os.environ.copy()
                        if args.k6:
                            summary_path = args.output / f"{label}.k6-summary.json"
                            load_env.update(K6_NO_USAGE_REPORT="true", CATALOG_TARGETS=",".join(addresses), CATALOG_FIXTURE=str(fixture), CATALOG_PROTO_DIR=str(ROOT / "protos"), CATALOG_RATE=str(rate), CATALOG_DURATION_SECONDS=str(duration), CATALOG_VUS="64", CATALOG_SUMMARY_PATH=str(summary_path))
                            command = ["k6", "run", "--quiet", "--no-color", "--no-usage-report", "--out", f"json={args.output / (label+'.k6-raw.gz')}", str(ROOT / "scripts/catalog-k6.js")]
                        if args.profile:
                            command += ["-cpu-profile", str(args.output / f"{label}.generator.pprof")]
                            if not args.profile_expiry:
                                command = ["strace", "-f", "-c", "-I2", "--kill-on-exit", "-o", str(args.output / f"{label}.generator.strace"), *command]
                        log = (args.output / f"{label}.client.stderr").open("wb")
                        logs.append(log)
                        generator = subprocess.Popen(command, cwd=SERVICE, env=load_env, stdout=subprocess.PIPE, stderr=log)
                        owned.append(generator)
                        resource_samples = []
                        load_started = time.monotonic()
                        load_started_unix = time.time()
                        gateway_restarted = database_restarted = False
                        deadline = time.monotonic()+duration+15
                        while generator.poll() is None:
                            if time.monotonic() >= deadline:
                                raise TimeoutError(f"load generator stuck: {label}")
                            resource_samples.append({"time": time.monotonic(), "db": resources(db), "gateways": [resources(p) for p in gateways], "generator": resources(generator)})
                            elapsed = time.monotonic()-load_started
                            if duration == 60 and elapsed >= 15 and not gateway_restarted:
                                stop(gateways[0])
                                gateways[0] = start(f"{label}-gateway0-restart", gateway_env[0])
                                wait_catalog(gateways[0], addresses[0])
                                result["events"].append({"label":label,"action":"gateway-restart","at_s":elapsed,"ready_at_s":time.monotonic()-load_started})
                                gateway_restarted = True
                            if duration == 60 and elapsed >= 30 and not database_restarted:
                                stop(db)
                                db = start(f"{label}-db-restart", dbenv)
                                wait_catalog(db, f"127.0.0.1:{dbport}")
                                result["events"].append({"label":label,"action":"durable-db-restart","at_s":elapsed,"ready_at_s":time.monotonic()-load_started})
                                database_restarted = True
                            time.sleep(.5)
                        stdout = generator.stdout.read()
                        (args.output / (f"{label}.client.stdout.log" if args.k6 else f"{label}.json")).write_bytes(stdout)
                        if generator.returncode:
                            result["failure"] = {"label":label,"generator_exit":generator.returncode,"command":command,"stderr":str(args.output / f"{label}.client.stderr"),"native_summary":str(summary_path) if args.k6 else None}
                            raise RuntimeError(f"load generator failed: {label}")
                        if args.k6:
                            native_summary = json.loads(summary_path.read_text())
                            native = native_summary["native"]
                            metrics = native["metrics"]
                            value = lambda name: metrics.get(name, {}).get("values", {})
                            started = value("catalog_started")["count"]
                            completed = value("catalog_completed")["count"]
                            successful = value("catalog_successful").get("count", 0)
                            failed = value("catalog_failed").get("count", 0)
                            dropped = value("dropped_iterations").get("count", 0)
                            latency = value("grpc_req_duration{status:0}")
                            wall = native["state"]["testRunDurationMs"] / 1000
                            sample = {"engine":"k6", "planned_offers":rate*duration, "offered":started+dropped, "started":started, "successful":successful, "failed":failed, "rejected":dropped, "dropped_iterations":dropped, "timeouts":value("catalog_timeouts").get("count",0), "correctness_errors":value("catalog_correctness_errors").get("count",0), "interrupted":started-completed, "codes":{"OK":successful,"FAILED":failed,"DROPPED":dropped,"INTERRUPTED":started-completed}, "wall_s":wall,"offered_rate":rate,"duration_s":duration,"successful_per_s":successful/wall,"native_status_zero_count":latency.get("count",0),"native_duration_gap":successful-latency.get("count",0),"p50_ms":latency.get("med"),"p95_ms":latency.get("p(95)"),"p99_ms":latency.get("p(99)"),"iteration_p99_ms":value("iteration_duration").get("p(99)"),"validation_p99_ms":value("group_duration").get("p(99)"),"generator_lag_p99_ms":None,"catalog_products":count,"latency_policy":"Native successful-RPC duration, with native iteration and validation-group durations retained. Dropped offers have no invented latency. Timeouts count RPC DeadlineExceeded; connection exceptions remain failures."}
                        else:
                            sample = json.loads(stdout)
                        assert sample["offered"] == sum(sample["codes"].values())
                        assert sample["correctness_errors"] == 0
                        sample.update(label=label, mode=mode, repeat=repeat, ttl=ttl, replicas=replicas, catalog_sha256=fixture_sha256, fixture_bytes=fixture.stat().st_size, command=command, metrics_before=before, metrics_after=[metric(admin) for admin in admins], resources=resource_samples)
                        if args.focused_cache:
                            sample["workload"] = native_summary["workload"]
                            sample["oracle"] = {key: oracle_result.get(key) for key in ("offered", "successful", "failed", "rejected", "timeouts", "correctness_errors", "attempted", "codes")}
                        if paired:
                            sample["build"] = build
                        sample["generator_started_unix_s"] = load_started_unix
                        sample["final_server_resources"] = {"db":resources(db),"gateways":[resources(p) for p in gateways]}
                        result["samples"].append(sample)
                        (args.output / "result.json").write_text(json.dumps(result, indent=2))
                        for process in gateways + [db]:
                            stop(process)
                        print(label, flush=True)
            if paired:
                assert len(result["samples"]) == (6 if args.smoke else 1086), "incomplete paired matrix"
            if not args.focused_cache:
                result["passed"] = True
    except Exception as error:
        result.setdefault("failure", {"type":type(error).__name__,"message":str(error)})
        raise
    finally:
        for process in owned:
            stop(process)
        for log in logs:
            log.close()
        result["cleanup"] = {"owned_processes_stopped": all(p.poll() is not None for p in owned), "scratch_removed": not Path(scratch).exists() if "scratch" in locals() else True}
        groups = {}
        for sample in result["samples"]:
            key = (sample["catalog_products"], sample["replicas"], sample["ttl"], sample["offered_rate"], sample["mode"])
            if paired:
                key += (sample["build"],)
            groups.setdefault(str(key), []).append(sample)
        result["summary"] = {key: {"n":len(samples), **{field:{"median":statistics.median(s[field] for s in samples),"min":min(s[field] for s in samples),"max":max(s[field] for s in samples)} for field in ["successful_per_s","p50_ms","p95_ms","p99_ms","generator_lag_p99_ms","rejected","timeouts"] if all(s.get(field) is not None for s in samples)}} for key,samples in groups.items()}
        (args.output / "result.json").write_text(json.dumps(result, indent=2)+"\n")

    if args.focused_cache:
        result["focused_cache_receipt_errors"] = focused_cache_receipt_errors(result, args.output)
        if result["focused_cache_receipt_errors"]:
            result["failure"] = {"type": "incomplete-focused-cache-receipt", "message": "; ".join(result["focused_cache_receipt_errors"])}
            result["passed"] = False
            result_path = args.output / "result.json"
            result_path.write_text(json.dumps(result, indent=2)+"\n")
            if json.loads(result_path.read_text(encoding="utf-8")) != result:
                raise RuntimeError("failed to persist inconclusive focused-cache result")
            raise RuntimeError(result["failure"]["message"])
        result["measurement_status"] = "complete"
        result["passed"] = True
        result_path = args.output / "result.json"
        result_path.write_text(json.dumps(result, indent=2)+"\n")
        if json.loads(result_path.read_text(encoding="utf-8")) != result:
            raise RuntimeError("failed to persist complete focused-cache result")


if __name__ == "__main__":
    main()
