# productcatalogservice

Run the following command to restore dependencies to `vendor/` directory:

    go mod vendor

## Dynamic catalog reloading / artificial delay

This service has a "dynamic catalog reloading" feature that is purposefully
not well implemented. The goal of this feature is to allow you to modify the
`products.json` file and have the changes be picked up without having to
restart the service.

However, this feature is bugged: the catalog is actually reloaded on each
request, introducing a noticeable delay in the frontend. This delay will also
show up in profiling tools: the `parseCatalog` function will take more than 80%
of the CPU time.

You can trigger this feature (and the delay) by sending a `USR1` signal and
remove it (if needed) by sending a `USR2` signal:

```
# Trigger bug
kubectl exec \
    $(kubectl get pods -l app=productcatalogservice -o jsonpath='{.items[0].metadata.name}') \
    -c server -- kill -USR1 1
# Remove bug
kubectl exec \
    $(kubectl get pods -l app=productcatalogservice -o jsonpath='{.items[0].metadata.name}') \
    -c server -- kill -USR2 1
```

## Latency injection

This service has an `EXTRA_LATENCY` environment variable. This will inject a sleep for the specified [time.Duration](https://golang.org/pkg/time/#ParseDuration) on every call to
to the server.

For example, use `EXTRA_LATENCY="5.5s"` to sleep for 5.5 seconds on every request.

## Persistent catalog mode

The default mode above remains the upstream JSON-backed product catalog. To run the P24 path, start a separate database process with `CATALOG_DB_MODE=1`, `CATALOG_DB_PATH` set to a writable bbolt file, `CATALOG_DB_BIND_ADDRESS=127.0.0.1`, and `CATALOG_DB_PORT=3551`. On first startup it validates and imports `CATALOG_SEED_PATH` (defaults to `products.json`); later starts load the durable snapshot and revision from bbolt. The database process exposes the unchanged `ProductCatalogService` read RPCs and an internal `CatalogAdminService` gRPC API.

Start the existing product catalog service with `CATALOG_BACKEND_ADDR=127.0.0.1:3551`. `CATALOG_CACHE_MODE` selects `none` (read the backend on every call), `ttl` (bounded TTL snapshot with intentionally uncoalesced cold misses), or `lease` (bounded per-process fill lease, request coalescing, and generation fencing on local writes). `CATALOG_CACHE_TTL` defaults to `2s` and is limited to `1m`; `CATALOG_BACKEND_TIMEOUT` defaults to `2s` and is limited to `30s`. A local admin update invalidates that gateway's snapshot. Other gateway replicas learn writes after their TTL expires, so the configured TTL bounds cross-replica staleness; this is not global linearizability.

When backend mode is active, the gateway starts a loopback-only HTTP listener on `127.0.0.1:3552` by default. `GET /admin/revision` reads the persistent revision, `GET /admin/stats` reports the database process's actual read count and revision, `GET /metrics` reports gateway request counts, cache behavior, and observed latency histograms, and `PUT /admin/catalog` validates and atomically replaces the catalog. Updates accept `{"expectedRevision":1,"catalog":{"products":[...]}}`; the expected revision is optional, and a mismatch returns HTTP 409. This listener is never exposed on a non-loopback address.

The local `cmd/catalogctl` binary drives the existing gRPC `ListProducts`, `GetProduct`, and `SearchProducts` methods. It also provides `update`, `revision`, `stats`, `metrics`, and `bench` commands. `bench REQUESTS CONCURRENCY` reports the offered, completed, failed, and observed p50/p95/p99 request latency for that run. These are measurements without an SLO assertion. `CATALOG_DB_RESPONSE_DELAY` is an optional 0–30 second response delay applied after the database has captured its snapshot; it supports controlled local stale-fill and cold-miss experiments and defaults to zero.

The internal write protocol source is `protos/catalog_admin.proto`; its generated Go stubs are in `genproto`. Regenerate with `protoc` and the pinned `protoc-gen-go@v1.36.12` and `protoc-gen-go-grpc@v1.5.1` plugins. The existing multilingual `demo.proto` and `ProductCatalogService` API remain unchanged.
