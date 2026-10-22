# Initial catalog measurements with the Go oracle driver

[Hosted run 39730138237](https://github.com/STEVVVU/cached-catalog-microservices/actions/runs/39730138237) measured source `d692907c20033ebf39b5c3599f2eae4aa6ebc66e`. The [raw artifact](https://github.com/STEVVVU/cached-catalog-microservices/actions/runs/39730138237/artifacts/12407578113), `catalog-benchmark-39730138237`, retains every request CSV, fixture/binary hash, command, process resource sample, server counter window, restart event and error. The result manifest SHA-256 is `e0e9f65b4196031608e4972a893d8cee53dce2befb380826df4bf998c8162fac`.

The isolated Ubuntu runner used Linux 6.17.0-1022-azure, glibc 2.39, Intel Xeon Platinum 8370C, two assigned physical cores/four logical CPUs, 16.77 GB RAM and Go 1.27.3 optimized builds. Gateway, durable SQLite backend and generator share this host. Catalogs contain 9, 500 or 5,000 synthetic products. The mix is 50% List, 40% Get and 10% Search with exact protobuf equality for successful responses. One/three gateways are selected round-robin. The Go generator offers independently of completion, caps workers at 64 and its waiting queue at 64, and uses a deadline three seconds after scheduled arrival. The gateway also has a two-second backend timeout.

All 543 trials completed and all 111 groups are present: 108 groups have five reversed-order repetitions; three are single stability trials. Across 6,768,000 offers, 3,599,398 succeeded, 3,166,201 were rejected by the generator queue and 2,401 failed RPCs, including 56 DeadlineExceeded and 2,345 Unavailable. No correctness mismatch occurred. All 543 receipts balance offered/outcome counts. All 540,000 raw records from the 23 trials containing RPC failures were checked against their receipt code counts. Cleanup confirms every owned process stopped and temporary state was removed.

## Complete comparison matrix

Each cell contains successful requests/s median [minimum, maximum]; successful scheduled-arrival-to-RPC-response p99 ms median [minimum, maximum]; then median rejected/failed offers. Each trial offers the indicated rate for six seconds. Rejections are included in outcomes and have no fabricated latency. Scheduled response duration includes producer and bounded-queue waiting but excludes subsequent exact protobuf validation. Validation still occupies a generator worker and affects achieved throughput. Dispatch-lag metrics also include that queue, so they are not pure scheduler-lateness measurements. The native k6 path uses a different timing definition; these runs are not a before/after comparison with it.

| Products | Gateways | TTL | Offers/s | No cache | TTL cache | Lease cache |
|---:|---:|---|---:|---|---|---|
| 9 | 1 | 60s | 100 | 99.98 [99.98, 99.99]; 1.89 [1.84, 2.03]; 0/0 | 99.99 [99.98, 100.00]; 1.51 [1.42, 1.60]; 0/0 | 100.00 [99.99, 100.00]; 1.53 [1.46, 1.55]; 0/0 |
| 9 | 1 | 60s | 1000 | 999.89 [999.87, 999.99]; 1.59 [1.55, 1.59]; 0/0 | 999.94 [999.83, 999.96]; 1.33 [1.33, 1.33]; 0/0 | 999.89 [999.83, 999.97]; 1.34 [1.33, 1.34]; 0/0 |
| 9 | 1 | 60s | 5000 | 4999.76 [4998.96, 4999.81]; 1.95 [1.87, 2.05]; 0/0 | 4999.99 [4999.95, 4999.99]; 1.38 [1.33, 1.41]; 0/0 | 4999.97 [4999.55, 4999.99]; 1.37 [1.35, 1.38]; 0/0 |
| 9 | 1 | 1ms | 100 | 99.99 [99.98, 100.00]; 1.91 [1.84, 2.03]; 0/0 | 100.00 [99.98, 100.00]; 1.97 [1.83, 1.99]; 0/0 | 99.99 [99.98, 99.99]; 1.94 [1.80, 2.01]; 0/0 |
| 9 | 1 | 1ms | 1000 | 999.89 [999.88, 999.98]; 1.56 [1.54, 1.59]; 0/0 | 999.88 [999.83, 999.92]; 1.64 [1.64, 1.67]; 0/0 | 999.99 [999.88, 999.99]; 1.62 [1.61, 1.62]; 0/0 |
| 9 | 1 | 1ms | 5000 | 4999.78 [4999.63, 4999.84]; 2.00 [1.91, 2.21]; 0/0 | 4999.92 [4999.82, 4999.99]; 1.79 [1.77, 1.87]; 0/0 | 4999.96 [4999.31, 4999.97]; 1.61 [1.57, 1.62]; 0/0 |
| 9 | 3 | 60s | 100 | 100.00 [99.99, 100.00]; 2.01 [1.83, 2.15]; 0/0 | 99.99 [99.98, 100.00]; 1.57 [1.49, 1.64]; 0/0 | 99.99 [99.98, 100.00]; 1.55 [1.46, 1.71]; 0/0 |
| 9 | 3 | 60s | 1000 | 999.85 [999.83, 1000.00]; 1.70 [1.70, 1.76]; 0/0 | 999.93 [999.92, 999.98]; 1.37 [1.35, 1.38]; 0/0 | 999.91 [999.87, 999.94]; 1.36 [1.35, 1.39]; 0/0 |
| 9 | 3 | 60s | 5000 | 4999.68 [4999.51, 4999.76]; 2.72 [2.62, 3.20]; 0/0 | 4999.97 [4999.93, 4999.98]; 0.85 [0.84, 0.88]; 0/0 | 4999.94 [4999.90, 4999.96]; 0.88 [0.82, 0.89]; 0/0 |
| 9 | 3 | 1ms | 100 | 99.99 [99.98, 100.00]; 1.90 [1.79, 1.97]; 0/0 | 99.99 [99.98, 99.99]; 1.98 [1.85, 2.16]; 0/0 | 99.99 [99.98, 100.00]; 1.93 [1.89, 2.11]; 0/0 |
| 9 | 3 | 1ms | 1000 | 999.88 [999.86, 999.97]; 1.58 [1.57, 1.64]; 0/0 | 999.87 [999.86, 999.95]; 1.63 [1.60, 1.64]; 0/0 | 999.97 [999.94, 1000.00]; 1.65 [1.60, 1.66]; 0/0 |
| 9 | 3 | 1ms | 5000 | 4999.57 [4999.47, 4999.76]; 2.63 [2.53, 2.80]; 0/0 | 4999.73 [4999.11, 4999.80]; 1.79 [1.72, 2.49]; 0/0 | 4999.66 [4999.35, 4999.98]; 1.56 [1.54, 1.59]; 0/0 |
| 500 | 1 | 60s | 100 | 99.99 [99.98, 99.99]; 4.01 [3.81, 4.20]; 0/0 | 99.99 [99.98, 99.99]; 2.77 [2.59, 2.81]; 0/0 | 99.99 [99.98, 99.99]; 2.73 [2.58, 2.79]; 0/0 |
| 500 | 1 | 60s | 1000 | 999.58 [999.28, 999.69]; 9.96 [9.51, 10.62]; 0/0 | 999.93 [999.85, 999.99]; 2.97 [2.78, 3.00]; 0/0 | 999.93 [999.90, 1000.00]; 2.91 [2.83, 2.92]; 0/0 |
| 500 | 1 | 60s | 5000 | 1315.74 [1300.64, 1321.38]; 147.64 [145.94, 155.52]; 21995/0 | 2595.43 [2542.90, 2680.14]; 72.65 [69.80, 72.89]; 14292/0 | 2602.05 [2551.52, 2651.29]; 71.89 [70.12, 73.50]; 14276/0 |
| 500 | 1 | 1ms | 100 | 99.99 [99.98, 99.99]; 3.83 [3.62, 4.00]; 0/0 | 99.99 [99.99, 100.00]; 4.00 [3.88, 4.11]; 0/0 | 99.99 [99.99, 100.00]; 4.20 [3.85, 4.26]; 0/0 |
| 500 | 1 | 1ms | 1000 | 999.72 [999.41, 999.88]; 10.29 [9.99, 12.12]; 0/0 | 999.83 [999.59, 999.90]; 7.04 [6.76, 7.32]; 0/0 | 999.90 [999.77, 999.98]; 5.06 [4.90, 5.35]; 0/0 |
| 500 | 1 | 1ms | 5000 | 1320.38 [1312.35, 1325.29]; 146.57 [142.97, 158.49]; 21971/0 | 1470.20 [1441.03, 1475.93]; 140.66 [138.66, 149.98]; 21059/0 | 1571.21 [1555.84, 1597.81]; 284.89 [277.96, 300.42]; 20443/0 |
| 500 | 3 | 60s | 100 | 99.99 [99.98, 100.00]; 3.75 [3.66, 3.97]; 0/0 | 99.99 [99.98, 99.99]; 2.64 [2.46, 2.80]; 0/0 | 99.99 [99.98, 100.00]; 2.62 [2.60, 2.63]; 0/0 |
| 500 | 3 | 60s | 1000 | 999.53 [999.36, 999.76]; 15.31 [14.92, 15.55]; 0/0 | 999.97 [999.88, 999.99]; 2.97 [2.83, 3.03]; 0/0 | 999.90 [999.83, 999.91]; 2.94 [2.87, 3.14]; 0/0 |
| 500 | 3 | 60s | 5000 | 1232.05 [1176.58, 1239.34]; 189.78 [183.43, 204.99]; 22498/0 | 2514.07 [2476.77, 2552.26]; 114.15 [107.05, 126.32]; 14813/0 | 2526.63 [2451.60, 2535.52]; 120.31 [103.78, 143.24]; 14715/0 |
| 500 | 3 | 1ms | 100 | 99.99 [99.98, 100.00]; 3.87 [3.66, 4.02]; 0/0 | 99.99 [99.98, 100.00]; 4.05 [3.88, 4.46]; 0/0 | 99.99 [99.98, 100.00]; 4.14 [3.92, 4.24]; 0/0 |
| 500 | 3 | 1ms | 1000 | 999.50 [999.31, 999.74]; 14.93 [14.00, 15.46]; 0/0 | 999.71 [999.71, 999.92]; 13.44 [12.61, 13.56]; 0/0 | 999.65 [999.60, 999.76]; 8.73 [8.43, 8.96]; 0/0 |
| 500 | 3 | 1ms | 5000 | 1239.99 [1219.21, 1248.46]; 183.83 [177.61, 198.42]; 22456/0 | 1237.57 [1231.11, 1248.54]; 205.77 [191.32, 209.65]; 22468/0 | 1657.40 [1651.94, 1665.70]; 235.25 [224.55, 282.43]; 19930/0 |
| 5000 | 1 | 60s | 100 | 99.99 [99.97, 100.00]; 28.97 [28.19, 29.72]; 0/0 | 99.99 [99.99, 100.00]; 9.97 [9.71, 10.12]; 0/0 | 99.99 [99.98, 100.00]; 9.80 [9.63, 9.97]; 0/0 |
| 5000 | 1 | 60s | 1000 | 149.73 [147.10, 154.13]; 1172.55 [1140.66, 1215.75]; 4996/0 | 262.28 [259.01, 266.42]; 546.67 [537.56, 581.08]; 4297/0 | 265.63 [262.39, 270.78]; 543.04 [531.39, 561.45]; 4289/0 |
| 5000 | 1 | 60s | 5000 | 148.45 [146.12, 150.17]; 1219.88 [1133.17, 1306.20]; 28997/0 | 268.65 [261.18, 270.42]; 545.63 [531.27, 578.73]; 28263/0 | 264.01 [257.56, 278.89]; 543.82 [531.03, 557.46]; 28288/0 |
| 5000 | 1 | 1ms | 100 | 99.98 [99.98, 100.00]; 28.94 [28.82, 30.13]; 0/0 | 99.98 [99.93, 99.99]; 37.42 [35.29, 37.70]; 0/0 | 99.98 [99.95, 99.99]; 26.63 [25.83, 27.58]; 0/0 |
| 5000 | 1 | 1ms | 1000 | 150.45 [145.80, 152.26]; 1214.87 [1056.72, 1275.12]; 4984/0 | 137.46 [136.17, 139.77]; 1305.62 [1236.29, 1363.47]; 5067/0 | 134.38 [134.04, 136.23]; 2346.57 [2171.66, 2384.28]; 5067/2 |
| 5000 | 1 | 1ms | 5000 | 148.44 [147.50, 152.38]; 1163.19 [1071.26, 1312.59]; 28994/0 | 138.97 [136.43, 140.83]; 1257.83 [1126.69, 1343.67]; 29060/0 | 136.90 [136.24, 137.85]; 2255.64 [2050.39, 2303.50]; 29048/2 |
| 5000 | 3 | 60s | 100 | 99.99 [99.98, 100.00]; 29.74 [29.58, 30.83]; 0/0 | 99.98 [99.98, 100.00]; 10.04 [9.96, 10.30]; 0/0 | 99.99 [99.99, 100.00]; 10.09 [9.75, 10.40]; 0/0 |
| 5000 | 3 | 60s | 1000 | 142.87 [139.36, 144.83]; 1414.44 [1383.38, 1659.21]; 5038/0 | 287.37 [282.01, 294.20]; 1141.68 [961.05, 1298.23]; 4140/0 | 280.54 [277.89, 295.64]; 1201.78 [973.08, 1424.97]; 4187/0 |
| 5000 | 3 | 60s | 5000 | 144.86 [143.67, 146.30]; 1379.70 [1341.43, 1439.79]; 29023/0 | 288.03 [284.07, 297.38]; 1143.27 [1044.91, 1259.93]; 28140/0 | 290.31 [284.07, 294.04]; 1270.18 [1108.40, 1318.46]; 28133/0 |
| 5000 | 3 | 1ms | 100 | 99.99 [99.96, 100.00]; 30.13 [29.19, 30.86]; 0/0 | 99.95 [99.93, 99.96]; 40.12 [39.46, 45.19]; 0/0 | 99.94 [99.90, 99.97]; 39.95 [39.18, 43.24]; 0/0 |
| 5000 | 3 | 1ms | 1000 | 145.33 [140.27, 146.41]; 1394.92 [1350.58, 1537.83]; 5018/0 | 129.27 [127.17, 130.03]; 1551.89 [1469.46, 1617.95]; 5122/0 | 147.45 [144.81, 152.43]; 2222.10 [2204.77, 2549.54]; 4985/2 |
| 5000 | 3 | 1ms | 5000 | 143.73 [142.68, 144.56]; 1401.99 [1343.09, 1435.16]; 29027/0 | 130.91 [129.13, 132.23]; 1512.70 [1470.21, 1689.25]; 29101/0 | 148.78 [147.52, 150.75]; 2168.29 [2072.40, 2398.81]; 28979/5 |

For 500 products, one gateway and a 60-second TTL at 1,000 offers/s, all five trials completed all 6,000 offers without errors/rejections. Successful scheduled-response p99 was 9.96 ms [9.51, 10.62] with no cache, 2.97 ms [2.78, 3.00] with TTL and 2.91 ms [2.83, 2.92] with lease. Server counter deltas show 6,001 backend reads for no-cache versus zero for either cached mode; both cached modes recorded 6,001 hits and zero misses. Those counter windows include the generator's one unmeasured warmup request. They establish the achieved warm-hit workload, not a hit ratio inferred from the TTL setting.

At higher load, the generator queue rejects work and the large protobuf fixture imposes substantial validation/serialization cost. For 5,000 products, one gateway, a 60-second TTL and 1,000 offers/s, TTL achieved only 262.28 successful/s [259.01, 266.42] and rejected a median 4,297 of 6,000 offers. Its dispatch/queue-lag p99 was 288.55 ms [278.08, 297.05]. These are limits of the combined service/generator experiment, not isolated server capacity guarantees.

## Short-expiry regression and restart stability

Every one of the 56 non-restart DeadlineExceeded errors occurred in lease mode with 5,000 products and a 1 ms TTL at 1,000 or 5,000 offers/s, across one and three gateways. At one gateway and 1,000 offers/s, lease scheduled-response p99 was 2346.57 ms [2171.66, 2384.28], versus TTL's 1305.62 ms [1236.29, 1363.47]. This is a material adverse case and is retained in the full matrix. The source clones catalog snapshots while holding the cache mutex and rechecks cache expiry when lease waiters wake. These are hypotheses for the regression until separate CPU/mutex/block profiles reproduce the large, short-expiry case. No cause-dependent cache fix has been made.

The separate stability case has 500 products, three gateways, a 50 ms TTL and 1,000 offers/s for 60 seconds. It restarts one gateway near 15 seconds and the durable backend near 30 seconds. Each mode is one trial; no repeated-run spread is claimed. All 2,345 Unavailable errors in the campaign occurred in these intentional restart trials. There were no queue rejections or deadlines in them, and all successful responses still matched the exact fixture.

| Mode | Successful / 60,000 offers | Unavailable | Scheduled-response p50 / p95 / p99 ms | Dispatch/queue lag p99 ms |
|---|---:|---:|---|---:|
| none | 58658 | 1342 | 4.49 / 9.44 / 12.75 | 2.51 |
| ttl | 59666 | 334 | 1.18 / 2.68 / 3.96 | 1.07 |
| lease | 59331 | 669 | 1.19 / 2.67 / 3.88 | 1.07 |

The new gateway's readiness check completed about 0.13–0.14 seconds after restart began, and database readiness about 0.12–0.13 seconds afterward. These process-readiness events do not measure when every pooled client connection recovered; the retained failed requests capture the broader disruption. Across the full campaign, maximum sampled VmRSS was 364.71 MiB for the database, 302.20 MiB for an individual gateway and 219.59 MiB for the generator. These periodic samples are not process high-water marks. No general service SLO, multi-host capacity, universal lease advantage or zero-error restart claim follows.

Reproduce the original path by building `go build -trimpath -o ../../bin/productcatalogservice .` and `go build -trimpath -o ../../bin/catalogctl ./cmd/catalogctl` from `src/productcatalogservice`, then running `python3 scripts/catalog-benchmark.py --output reports/evidence/benchmark` from the repository root. The maintained workflow is `.github/workflows/catalog-benchmark.yml`. Preserve original data/configuration and distinguish separate diagnostic profiles from reported timings. The native k6 campaign remains a separately labeled measurement path.
