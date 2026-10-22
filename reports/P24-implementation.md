# P24 implementation evidence

## Scope and baseline

- Full clone of `https://github.com/GoogleCloudPlatform/microservices-demo` at `38e7348eb289eb5b87c0c6e8cb19ced0449dc389` (2,699 commits, Apache-2.0), in `C:\Users\steve\Projects\cached-catalog-microservices`. The upstream services, languages, proto contracts, frontend, and Git history remain present. The existing public ProductCatalog RPC contract and UI flow were preserved.
- Go is pinned to the existing isolated `C:\Users\steve\.codex\tools\coursework-go\go\bin\go.exe` (`go1.27.3 windows/amd64`). Go build/module caches use `C:\Users\steve\.codex\tools\coursework-go\cache` and `...\gopath\pkg\mod`. No global install or shared ML environment change.
- Baseline native Windows `go test ./...` failed because upstream `server.go` used Unix-only `syscall.SIGUSR1` and `SIGUSR2`. `catalog_signal_unix.go` retains the Unix reload hook; `catalog_signal_windows.go` makes that unavailable hook compile safely on Windows. Afterward the upstream test baseline passed.
- The initial free space was 25,671,163,904 bytes (~23.9 GiB); checkout is about 12.7 MB and disk remained above the 10 GiB reserve.

## Implementation

- `src/productcatalogservice/catalog_store.go` implements bbolt persistence of the protobuf catalog and monotonically increasing revision in a single transaction. It seeds only a new database, validates replacement catalogs, enforces size/count/field limits and unique IDs, and supports expected-revision writes.
- `src/productcatalogservice/catalogdb_service.go` exposes the persistent backend through gRPC. Existing `ListProducts`, `GetProduct`, and `SearchProducts` calls now route through the cache gateway when configured. `protos/catalog_admin.proto` and generated `genproto/catalog_admin*.go` define internal admin operations without changing upstream `demo.proto`.
- `catalog_cache.go` implements `none`, intentionally uncoalesced `ttl`, and `lease` modes. TTL bounds entries to one minute. Lease coalesces concurrent cold fills and uses a generation fence so an in-flight old snapshot cannot overwrite a local post-write invalidation. Each process owns its own cache: this is bounded eventual consistency, not cross-replica linearizability. Cache responses clone protobuf values.
- Loopback-only HTTP admin endpoints support strict JSON catalog writes, revision, backend stats, and Prometheus-format metrics. gRPC instrumentation records RPC/cache activity and request duration. `cmd/catalogctl` drives the actual gRPC and loopback paths and reports observed concurrency latency percentiles.
- `kustomize/overlays/catalog-cache/` adds a persistent DB StatefulSet/PVC, internal ClusterIP service, DB ingress NetworkPolicy, and the lease-mode gateway configuration. It removes the upstream public frontend LoadBalancer for this internal overlay and uses non-root/read-only-rootfs pod settings. Source instructions and cache coherence limits are in `src/productcatalogservice/README.md`.
- Added root `AGENTS.md`, `.agents/skills/verify-productcatalog/{SKILL.md,verify.ps1}`, and feature maps for browse, update, cache, and recovery. The maintained PowerShell driver exercises the real Windows product processes, keeps logs/evidence, then cleans only its own processes and temporary DB directory.
- New direct dependency: `go.etcd.io/bbolt v1.4.3` (MIT, Go 1.23 module baseline; official release/tag and license checked). The upstream `go.sum` already contained its checksums as a transitive dependency; direct-use checksums are included. No other runtime dependency was added.

## Verification

Commands run in `src/productcatalogservice` with the isolated Go caches:

```powershell
go test -count=1 -mod=readonly ./...
go vet -mod=readonly ./...
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go build -mod=readonly .
```

All passed with Go 1.27.3, including the Linux cross-build. The native Windows end-to-end driver passed:

```powershell
pwsh -NoProfile -File .agents/skills/verify-productcatalog/verify.ps1
```

Its evidence is in [`reports/evidence/20261022T024904Z/evidence.json`](evidence/20261022T024904Z/evidence.json); server/client logs in that folder survive scratch cleanup. The run used separate real DB and gateway processes and proved:

- Existing gRPC List/Get/Search returned the seeded nine products.
- Invalid duplicate-ID updates returned HTTP 400 without changing revision; valid updates atomically advanced revision and were visible through normal reads.
- Stopping the DB process produced bounded gRPC `Unavailable` (15.5 ms observed); restarting against the same bbolt file retained revision and updated catalog data.
- TTL expiry caused a backend reread. A controlled captured-snapshot race reproduced stale TTL data until expiration. In the same race, lease fencing returned the fresh revision and recorded one fenced fill.
- On a 500 ms controlled post-snapshot delay, 40 concurrent lease cold misses caused one actual DB read and 39 waiters; 20 TTL misses caused 20 reads.
- Restarting a warmed cache process caused the replacement process to refill from the DB.
- Six interleaved warmed runs of 100 requests / 16 workers across TTL, lease, and no-cache modes all completed 100/100 without failure. Measured p50/p95/p99 in ms: TTL 0.517/2.668/2.668 then 0.523/2.613/2.613; lease 0.525/2.619/2.619 then 0.521/2.135/2.135; no-cache 0.537/2.743/2.743 then 0.523/3.136/3.136. These are local observations, not an SLO claim.

Kustomize v5.8.2 was run from the project-local ignored `.tools` directory:

```powershell
.tools\bin\kustomize.exe build kustomize\overlays\catalog-cache
```

Rendered output is preserved at [`reports/catalog-cache-rendered.yaml`](catalog-cache-rendered.yaml) (24,375 bytes). Inspection found no LoadBalancer, NodePort, Ingress, or `frontend-external` resource. `protoc` 36.2 and Go protobuf plugins were also kept in ignored `.tools`; the Protobuf GitHub release SHA256 for the downloaded Windows archive was checked before use.

## Limits and remaining checks

- This host has no Docker, kubectl, Helm, or Kustomize on PATH; Kustomize rendering used the project-local binary. No cluster exists here, so Kubernetes behavior is not claimed as deployed or runtime-tested. The overlay is internal-only by rendered-resource inspection.
- `go test -race` was not run: Go reports `CGO_ENABLED=0` and no GCC compiler is installed. Unit tests and real concurrent multiprocess races ran without the race detector.
- No public deployment, cloud resource, user-machine container runtime, WSL, privileged action, remote branch, push, or commit was created. The local clone and artifacts are ready for review.
- The end-to-end driver retains two earlier failed diagnostic run directories as well as the passing run; only `20261022T024904Z` is the acceptance evidence. Earlier assertions were corrected to reflect the upstream nine-product seed and actual stats JSON shape.

## Review follow-up and CI packaging

- The parent agent's independent review identified a canceled lease leader broadcasting cancellation to live waiters, and corrupt nonempty revision bytes passing startup validation. Both were reproduced with new deterministic tests before the fixes: `TestLeaseCacheCanceledLeaderDoesNotFailLiveWaiter` failed because its live waiter did not re-elect; `TestCatalogStoreRejectsMalformedRevisionWithoutOverwritingDatabase` failed because `Replace` accepted revision lengths 0, 1, 4, and 9 and rewrote the stored data. The prior full-process verification run also passed, but those targeted failing reproducers established these defects separately.
- Lease flights now record whether the owner returned an error caused by its canceled/deadlined request context. A still-live waiter loops to elect/coalesce a new flight only in that case; other backend failures remain explicit. Catalog store open/read/replace now validate a positive, exactly-eight-byte revision in the bbolt transaction, reject exhausted revision counters, and avoid mutating the snapshot on corruption. Regression tests pass for missing, short, zero-valued, and oversized revisions and verify original bytes remain unchanged.
- The real-process driver now selects Go/cache paths and process launch settings for either Windows or Linux. Its native Windows rerun passed; Linux driver execution is not yet claimed and is pending hosted CI after parent inspection.
- Disk free space remains 21.75 GiB after local implementation and evidence, above the required 10 GiB reserve.
- Root `README.md` now describes the catalog extension; the original README is byte-identical at `README.upstream.md`. All nine inherited GitHub workflow YAML files and the inherited workflows README are byte-identically preserved in `archive/upstream-github-workflows`; only new read-only Go validation and an explicitly manual hosted kind smoke workflow remain active.
- `.github/workflows/go-productcatalog.yml` pins checkout/setup-go/upload-artifact by commit, checks Go formatting/tests/race/vet, runs the actual process driver, and retains evidence for seven days. `.github/workflows/catalog-cache-kind.yml` is `workflow_dispatch` only. Its runner script checks official kind and kubectl binaries, uses the pinned `kindest/node` digest, builds and loads only the catalog image, renders the internal overlay, selects only gateway/backend/PVC/network policy objects, proves gRPC browse/update/cache/metrics and update persistence through backend pod restart, and deletes the exact cluster it created. No hosted run has been triggered; parent review is the gate.
- Kustomize render-and-select was validated locally against all seven expected named resources. Bash is unavailable natively on this Windows host; invoking its `bash` command resolved to WSL and reported no installed distribution. WSL was not installed or launched. Hosted shell syntax/runtime and the K8s smoke remain unverified pending the first reviewed hosted run.
- Latest native driver evidence after the review fixes and portable-path edits: `reports/evidence/20261022T030526Z/evidence.json` (passed; artifacts survive cleanup). Also passed after fixes: `go test -count=1 -mod=readonly ./...`, `go vet -mod=readonly ./...`, direct `gofmt -l` check, and Linux `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly .`. The local `go test -race` attempt confirms `-race requires cgo`; this Windows toolchain has `CGO_ENABLED=0` and no GCC, so race execution is delegated to the prepared hosted Ubuntu CI after review.
- The next primary-source review caught that a missing snapshot in an existing database bucket (or a missing catalog bucket in a database with other data) was treated as an empty database and silently seeded at revision 1. The original behavior was reproduced before the fix: `TestCatalogStoreDoesNotReseedExistingMalformedDatabase` failed in both cases while the logs showed seed parsing succeeded. Initialization now checks whether the database path existed before bbolt opens it; only a previously absent path may be seeded. Existing catalog buckets missing a snapshot, and existing databases missing the catalog bucket, fail closed. Tests reopen the file and verify revision/unrelated bucket bytes were preserved.
- Both workflow concurrency groups now use valid GitHub expressions (`${{ github.workflow }}` and `${{ github.ref }}`); the manual kind workflow serializes runs without canceling its active cluster. YAML parsing passed after correction.
- Latest native driver evidence after the storage initialization fix: `reports/evidence/20261022T031117Z/evidence.json` (passed; artifacts survive cleanup). Focused catalog store tests for initial seed, update/reopen, malformed revisions, missing snapshot, and missing bucket passed. No hosted workflows, cluster, Linux driver, or local race detector have been run.
- The follow-up hosted kind review reproduced the root-module build failure (`go build ... ./src/productcatalogservice/cmd/catalogctl` reports that no main module exists from the repository root). The smoke script now builds the client with `go -C src/productcatalogservice`, builds the catalog image from that service's Dockerfile before loading it into kind, and leaves the rendered PVC on the cluster's default storage class. It no longer creates a hostPath PV or mutates the PVC to disable provisioning.
- The post-database-restart check now waits on an actual direct gRPC catalog `List` through a backend Service port-forward, verifies the updated item, and only then restarts the gateway; the fixed three-second delay is removed. The cleanup trap captures nodes, StorageClasses, namespaced resource state, events, descriptions, and both service logs before deleting only the uniquely named cluster. GitHub uploads this evidence even on a failed smoke step. Local validation: Git Bash `bash -n scripts/catalog-cache-kind-smoke.sh`, YAML parsing for both workflows, and `git diff --check` passed. The native end-to-end driver passed again at `reports/evidence/20261022T031449Z`; focused `TestCatalogStore` passed. No Docker/kind runtime is available locally, and neither hosted workflow has been dispatched, so hosted storage provisioning and cluster behavior remain unverified pending parent review.
- First hosted execution exposed two harness defects. In run [39716905349](https://github.com/STEVVVU/cached-catalog-microservices/actions/runs/39716905349), the captured cluster snapshot confirmed the default `standard` StorageClass existed, but the generated selected manifest parsed as a single `NetworkPolicy`: joining stripped documents with `---\n` omitted the newline before each separator. The first failure artifact showed only that NetworkPolicy in the namespace and no PVC or pods; the kind image and cluster were cleaned up. The writer now joins with `\n---\n`; applying the same selection to the preserved full render parses into exactly the seven expected YAML objects.
- Hosted Go checks (run [39716902726](https://github.com/STEVVVU/cached-catalog-microservices/actions/runs/39716902726)) passed formatting, tests, race detection, and vet. The real-process driver then failed in its `finally` block while recursively removing Go's readonly module-cache files inside its task-owned temp directory; that exception prevented the evidence JSON and `GITHUB_OUTPUT` value from being written. The verifier now passes `-modcacherw` for its isolated module cache, catches/report cleanup errors, always writes a terminal evidence summary and output path, and returns failure status when cleanup fails. The independent Windows driver rerun passed at `reports/evidence/20261022T033206Z`. Linux cleanup and kind cluster integration have not been rerun; no workflow was dispatched for these latest fixes.
