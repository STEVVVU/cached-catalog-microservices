# Browse and search the product catalog

## Sub-features

- List the seeded or persisted catalog.
- Get one product by ID.
- Search product name and description text.

## How to get to it (user POV)

The Online Boutique frontend, checkout service, and recommendation service use the existing `ProductCatalogService` gRPC API. The native driver reaches those same List/Get/Search methods through `catalogctl`.

## Driving it with PowerShell and catalogctl

Run the verification skill's `verify.ps1`. It calls `catalogctl list`, reads the first returned ID, calls `catalogctl get <id>`, and searches the returned product name. The resulting protobuf JSON contains the complete products list, matching product ID, and at least one search result.

## Gotchas

When `CATALOG_BACKEND_ADDR` is unset, the upstream file-backed mode remains active. Backend mode has no empty-catalog fallback: a failed gRPC read is an explicit error.
