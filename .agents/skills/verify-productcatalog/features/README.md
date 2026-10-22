# Product catalog feature map

- [Browse and search the product catalog](catalog-browse.md)
- [Replace the catalog with revision validation](catalog-update.md)
- [Cache freshness and cold-miss behavior](catalog-cache.md)
- [Recover from backend and process restarts](catalog-recovery.md)
- `scripts/catalog-benchmark.py` runs real database/gateway processes with 9/500/5000 products, one/three gateways, no-cache/TTL/lease modes, 60-second/1ms TTLs and 100/1000/5000 offered requests per second. Five reversed-order repeats retain the 50% List, 40% Get, 10% Search mix and exact expected protobuf values. `catalogctl load` preserves scheduled arrivals, bounded workers/queue, rejections, status codes, timeouts, generator lag and raw latencies. A separate 60-second case restarts one gateway and the durable database while requests continue. CPU profiles require the separate `benchmark` build tag and profile workflow input.
