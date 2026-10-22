# Replace the catalog with revision validation

## Sub-features

- Replace the complete immutable catalog snapshot.
- Reject duplicate IDs, invalid prices, oversized catalogs, and stale expected revisions.
- Persist one revision increment with the replacement in one bbolt write transaction.
- Invalidate the writing gateway's local cache after a committed update.

## How to get to it (user POV)

`catalogctl update <catalog-json> <expected-revision>` sends a request to the product catalog's loopback admin endpoint, which uses the internal CatalogAdmin gRPC API to commit the snapshot.

## Driving it with PowerShell and catalogctl

Run the verification skill's `verify.ps1`. It submits a duplicate-ID snapshot and confirms HTTP 400 with the original revision unchanged, then submits a valid snapshot at revision 1 and reads the new product through `ListProducts` at revision 2.

## Gotchas

The admin listener binds only to `127.0.0.1` and is not exposed through the Kubernetes Service. Cross-replica cache invalidation is bounded by the configured TTL.
