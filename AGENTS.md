# Project instructions

## Scope and provenance

- This repository is the complete Apache-2.0 GoogleCloudPlatform/microservices-demo upstream at `38e7348eb289eb5b87c0c6e8cb19ced0449dc389`. Preserve all 11 services, their languages, existing history, public protobuf APIs, UI, license, notices, and upstream documentation.
- P24 work extends the existing Go `src/productcatalogservice` request path. Keep the original file-backed behavior as the default. Do not add cloud credentials, paid services, public deployments, user-machine Docker/WSL setup, or unrelated project changes.
- Keep additions narrow to the catalog backend, its cache modes, the internal-only Kubernetes overlay, verification assets, and project documentation.

## Build and test

- The service is a standalone Go module at `src/productcatalogservice`.
- Use the pinned local Go 1.27.3 toolchain at `C:\Users\steve\.codex\tools\coursework-go\go\bin\go.exe` and the isolated shared cache at `C:\Users\steve\.codex\tools\coursework-go\gopath\pkg\mod` / `C:\Users\steve\.codex\tools\coursework-go\cache`. Do not change global Go settings or mutate the shared ML environment.
- The upstream CI runs `go test` in this module. Also run focused race-enabled tests where native Windows supports them, formatting, and the repository's Kustomize/Helm validators if available.
- Record commands and observed outcomes in `reports/P24-implementation.md`. Keep generated run evidence outside maintained verifier tooling and preserve it through cleanup.

## Behavior and safety

- Keep existing `ProductCatalogService` gRPC List/Get/Search behavior and consumers working. Backend unavailability, deadlines, invalid writes, and persistence errors must remain explicit.
- Catalog writes are internal-only and validated. Persist updates transactionally. Cache invalidation must fence an in-flight stale read; the lease mode coalesces cold fills and invalidates old fill tokens. State bounded cross-replica freshness accurately.
- Do not expose an unauthenticated write API publicly. The Kubernetes verification overlay must use internal services only and persistent storage.
- No LLM-generated wording is a deterministic test target. Do not claim live Kubernetes proof from manifest rendering alone.
