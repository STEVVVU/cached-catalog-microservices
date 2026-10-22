# Recover from backend and process restarts

## Sub-features

- Return bounded explicit errors while the durable gRPC backend is unavailable.
- Recover the committed catalog and revision after database process restart.
- Rebuild the in-memory cache after the product catalog process restarts.

## How to get to it (user POV)

The bbolt file path is configured through `CATALOG_DB_PATH`; the database service and cache gateway are separate processes. Existing callers continue to use the product catalog gRPC service.

## Driving it with PowerShell and catalogctl

Run the verification skill's `verify.ps1`. It stops only the database process it launched, confirms a bounded gRPC deadline/unavailable error, restarts the database on the same temporary file, and rereads the updated product and revision. It also restarts a warmed gateway and confirms the new process performs a backend read to populate its cache.

## Gotchas

The Kustomize overlay uses a PVC and a StatefulSet, but this Windows host has no Kubernetes CLI or container runtime. Manifest rendering is verified separately and does not prove a running cluster or persistent-volume attachment.

Startup, reads, and writes reject revisions that are missing, zero, or not exactly eight bytes. A missing catalog snapshot or bucket in an existing database also fails closed; only a database path that did not exist before open is initialized from seed data. `TestCatalogStoreRejectsMalformedRevisionWithoutOverwritingDatabase` proves malformed revision data and the original snapshot remain unchanged after rejected reads and writes. `TestCatalogStoreDoesNotReseedExistingMalformedDatabase` proves a retained revision and unrelated bucket bytes survive failed open without reseeding.
