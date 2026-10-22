"""Owned local gRPC service benchmark. Build binaries before invoking this driver."""
import argparse
import copy
import contextlib
import hashlib
import json
import os
from pathlib import Path
import platform
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


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--server", type=Path, default=ROOT / "bin/productcatalogservice")
    parser.add_argument("--client", type=Path, default=ROOT / "bin/catalogctl")
    parser.add_argument("--smoke", action="store_true")
    parser.add_argument("--profile", action="store_true")
    parser.add_argument("--profile-expiry", action="store_true", help="profile the observed 5000-product/1ms-expiry adverse case")
    parser.add_argument("--k6", action="store_true", help="use k6 native gRPC arrival scheduling and statistics")
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
                        if paired:
                            assert hashlib.sha256(fixture.read_bytes()).hexdigest() == CATALOG_HASHES[count], "original catalog changed"
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
                            native = json.loads(summary_path.read_text())["native"]
                            metrics = native["metrics"]
                            value = lambda name: metrics.get(name, {}).get("values", {})
                            started = value("catalog_started")["count"]
                            completed = value("catalog_completed")["count"]
                            successful = value("catalog_successful").get("count", 0)
                            failed = value("catalog_failed").get("count", 0)
                            dropped = value("dropped_iterations").get("count", 0)
                            latency = value("grpc_req_duration{status:0}")
                            wall = native["state"]["testRunDurationMs"] / 1000
                            sample = {"engine":"k6", "planned_offers":rate*duration, "offered":started+dropped, "started":started, "successful":successful, "failed":failed, "rejected":dropped, "dropped_iterations":dropped, "timeouts":value("catalog_timeouts").get("count",0), "correctness_errors":value("catalog_correctness_errors").get("count",0), "interrupted":started-completed, "codes":{"OK":successful,"FAILED":failed,"DROPPED":dropped,"INTERRUPTED":started-completed}, "wall_s":wall,"offered_rate":rate,"duration_s":duration,"successful_per_s":successful/wall,"p50_ms":latency.get("med"),"p95_ms":latency.get("p(95)"),"p99_ms":latency.get("p(99)"),"iteration_p99_ms":value("iteration_duration").get("p(99)"),"validation_p99_ms":value("group_duration").get("p(99)"),"generator_lag_p99_ms":None,"catalog_products":count,"latency_policy":"Native successful-RPC duration, with native iteration and validation-group durations retained. Dropped offers have no invented latency. Timeouts count RPC DeadlineExceeded; connection exceptions remain failures."}
                        else:
                            sample = json.loads(stdout)
                        assert sample["offered"] == sum(sample["codes"].values())
                        assert sample["correctness_errors"] == 0
                        sample.update(label=label, mode=mode, repeat=repeat, ttl=ttl, replicas=replicas, catalog_sha256=hashlib.sha256(fixture.read_bytes()).hexdigest(), fixture_bytes=fixture.stat().st_size, command=command, metrics_before=before, metrics_after=[metric(admin) for admin in admins], resources=resource_samples)
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


if __name__ == "__main__":
    main()
