# Cached Catalog Microservices

An Apache-2.0 extension of Google's Online Boutique that makes its existing product catalog durable and observable, then compares three real read paths: uncached, TTL, and bounded lease-based caching. The lease path coalesces concurrent cold fills and fences stale in-flight snapshots after local catalog writes.

The app remains the complete upstream 11-service, multi-language Online Boutique. The existing `ProductCatalogService` gRPC API and storefront are unchanged. A separate bbolt-backed catalog process serves that API through the original Go product-catalog service; writes are validated and revision-checked. Each gateway owns its cache, so TTL bounds cross-replica staleness while lease fencing protects writes within one gateway. This is not a claim of global linearizability.

## Verify locally

The maintained native Windows driver launches real database and gateway processes, exercises the gRPC/UI-facing API path, races, persistent updates, backend failure and restart, cache rebuild, and latency measurements, then retains logs and evidence while cleaning its own scratch processes and files:

```powershell
pwsh -NoProfile -File .agents/skills/verify-productcatalog/verify.ps1
```

It uses the project's isolated Go 1.27.3 toolchain on this Windows host, or Go 1.27.3 from `PATH` on Linux. To run the Go package checks directly from `src/productcatalogservice`:

```powershell
go test ./...
go vet ./...
```

## Local service configuration

The product catalog service preserves its upstream file-backed behavior when `CATALOG_BACKEND_ADDR` is unset. To use the durable backend, run the product-catalog server once in database mode and once as the gateway. Configure the backend address, cache mode (`none`, `ttl`, or `lease`), TTL, and optional loopback admin port with `CATALOG_BACKEND_ADDR`, `CATALOG_CACHE_MODE`, `CATALOG_CACHE_TTL`, and `CATALOG_ADMIN_PORT`. The database persists to `CATALOG_DB_PATH` and seeds from `CATALOG_SEED_PATH` only when that database is new. See [service configuration and admin endpoints](src/productcatalogservice/README.md).

The `src/productcatalogservice/cmd/catalogctl` client provides gRPC `list`, `get`, `search`, concurrent `bench`, and loopback-only admin `update`, `revision`, `stats`, and `metrics` commands. Admin HTTP is bound to loopback; the Kubernetes overlay exposes it only inside the product-catalog pod network namespace.

## Kubernetes

Render the internal-only persistent-catalog overlay with:

```powershell
.tools\bin\kustomize.exe build kustomize\overlays\catalog-cache
```

The overlay adds a one-replica database StatefulSet and persistent volume, restricts database ingress to product-catalog pods, and removes the storefront LoadBalancer. The manually triggered `catalog-cache-kind` workflow renders that overlay, applies only the catalog gateway and database resource set to its disposable kind cluster, then deletes the cluster in a hosted GitHub runner; it is not a production or cloud deployment.

## Upstream source and attribution

This repository is a full-history clone of [GoogleCloudPlatform/microservices-demo](https://github.com/GoogleCloudPlatform/microservices-demo) at `38e7348eb289eb5b87c0c6e8cb19ced0449dc389`. Its Apache license, notices, application source, manifests, and history are retained. The original upstream root README is preserved verbatim as [README.upstream.md](README.upstream.md); dangerous inherited deployment, release, and cleanup workflows are byte-identically archived under [archive/upstream-github-workflows](archive/upstream-github-workflows). The active workflows are read-only Go validation and an explicit manual disposable-cluster smoke test.

The lease design is informed by Nishtala et al., [Scaling Memcache at Facebook (NSDI 2013), §3.2.1](https://www.usenix.org/system/files/conference/nsdi13/nsdi13-final170_update.pdf). This implementation is a bounded catalog-specific adaptation, not a reproduction of Facebook's infrastructure.

## Verification evidence

The [native Windows process result](docs/evidence/windows-process.json) records the executed journey and observed cache behavior. Hosted Linux race checks and the disposable Kubernetes journey are pending. The small warmed latency samples do not establish an SLO or a general performance advantage.
