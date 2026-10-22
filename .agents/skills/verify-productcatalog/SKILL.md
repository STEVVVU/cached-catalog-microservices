---
name: verify-productcatalog
description: Drive and verify the Online Boutique product catalog over real local gRPC processes on Windows or Linux, including persistent writes, cache modes, invalidation, and restart behavior.
---

# Verify the product catalog

Use this driver after changing the product catalog, its bbolt backend, admin API, cache modes, or their Kubernetes overlay. It runs only local operating-system processes and uses a temporary bbolt database. It does not start Docker, WSL, Kubernetes, or a browser. Go is selected from `P24_GO`, the existing isolated Windows toolchain, or `PATH`; caches use the existing isolated Windows cache when available and a run-owned temporary cache on other hosts.

## Launch

From the repository root on Windows or Linux with PowerShell 7 and Go 1.27.3 available, run:

```powershell
pwsh -NoProfile -File .agents/skills/verify-productcatalog/verify.ps1
```

The script first checks Go 1.27.3 and runs `go test ./...` in `src/productcatalogservice`. It then builds the real product catalog server and `cmd/catalogctl`, starts the database process with a fresh scratch bbolt file, and starts the existing ProductCatalog gRPC server in each requested cache mode. Each listener gets a free loopback TCP port. A successful gRPC list and loopback admin stats response are readiness checks. Logs and action records are written to a unique `reports/evidence/<UTC timestamp>/` directory.

## Doctor

The script verifies the Go executable path, test/build exit codes, actual database gRPC readiness, gateway loopback admin readiness, and process liveness during controlled stale-fill races. Process launch and cleanup work on native Windows and Linux. If a gateway request fails, run `catalogctl stats` against that same gateway before retrying; if the database is unavailable, inspect its corresponding `db-*.stdout.log` and `db-*.stderr.log` in the saved evidence directory.

## Drive

The maintained client binary supports real gRPC catalog operations and the loopback update/metrics path:

```powershell
$env:CATALOGCTL_GRPC_ADDR = '127.0.0.1:3550'
$env:CATALOGCTL_ADMIN_URL = 'http://127.0.0.1:3552'
catalogctl list
catalogctl get <product-id>
catalogctl search <query>
catalogctl update <catalog-json> <expected-revision>
catalogctl revision
catalogctl stats
catalogctl metrics
catalogctl bench 100 16
```

`verify.ps1` exercises the default seeded list, Get and Search, valid and invalid writes, TTL expiry, a deterministic TTL stale-fill reproduction, a lease/fence update race, cold-miss coalescing, unavailable backend timeouts, database and gateway restarts, and interleaved warmed `none`/`ttl`/`lease` latency runs. Its 500 ms database response delay occurs after the backend captures its snapshot and exists only to make the stale-fill race observable. Benchmark values are measured request durations and counts; the driver does not assert an SLO.

## Evidence
For the standard load framework, install k6 2.3.0 only in the isolated runner, then run `python3 scripts/catalog-benchmark.py --k6 --output reports/evidence/k6`. The manual workflow `.github/workflows/catalog-k6.yml` pins Grafana's setup action to verified commit `43b9fc21641a76002687994433dd586f56e791b1` and disables browser installation and usage reporting. Its default smoke must pass before a full campaign. The Go exact protobuf oracle runs before each k6 trial. Native gRPC status-zero request duration, native iteration duration, validation-group duration and dropped iterations remain separate. The script never reconstructs scheduled latency from started iteration ordinals. The initial Go campaign remains reproducible with the original command and its own latency semantics.

For sustained measurement, build `go build -trimpath -o ../../bin/productcatalogservice .` and `go build -trimpath -o ../../bin/catalogctl ./cmd/catalogctl` from the service module. From the repository root run `python3 scripts/catalog-benchmark.py --output reports/evidence/benchmark`. The maintained driver creates disposable databases and real gateway processes and retains raw scheduled-arrival CSVs, exact outcomes, before/after server metrics, resource samples, medians/ranges and restart events. Use `--smoke` only for setup. Run `.github/workflows/catalog-benchmark.yml` with the profile input for a separate source-level pprof run; that binary has the `benchmark` build tag and automatically closes its eight-second profiles. Normal product builds have no profiling addition. Counters expose achieved hit/miss ratios; TTL settings do not imply exact ratios. Rejections and timeouts remain in every run, and generator saturation must be ruled out before reporting a service limit.


Read `evidence.json` plus the server logs in the named run directory. Each proof records an action and its resulting state. `stats` reports the database process's actual ProductCatalog read count and persisted revision; the gateway metrics endpoint reports offered/completed/failed requests, backend RPC attempts, cache activity, and observed duration histogram buckets. The benchmark records client-observed p50/p95/p99. The driver's process logs and JSON proof are the action record, and the database revision/product reread after restart proves the durable side effect.

## Cleanup

The script tracks each `System.Diagnostics.Process` it started and stops those process objects in `finally`. It removes only its uniquely named temporary directory under the host's temp directory; the evidence directory remains. The script verifies that evidence files still exist after scratch cleanup. It never kills processes by name.

## Feature map

See [the feature index](features/README.md). After changing a mapped user path, rerun this driver and keep the resulting evidence with the report.
