# Cache freshness and cold-miss behavior

## Sub-features

- `none` reads the durable backend for every catalog call.
- `ttl` serves snapshots until expiry and provides the deliberately uncoalesced baseline.
- `lease` coalesces one cold load per gateway process and fences fills started before a local write.
- Metrics and tracing observe actual gRPC operations and request durations.

## How to get to it (user POV)

The existing product catalog service remains on port 3550. Its cache mode, TTL, backend address, and backend timeout are process configuration. The loopback `/metrics` endpoint and `catalogctl bench` expose measured behavior.

## Driving it with PowerShell and catalogctl

Run the verification skill's `verify.ps1`. It waits for a captured stale snapshot, writes through the admin path, observes the TTL baseline's stale fill until expiry, and verifies that lease mode discards and reloads the same fill. It also drives simultaneous cold misses with a controlled response delay and compares actual backend read counts for the TTL and lease modes.

## Gotchas

Lease fencing and coalescing are per process and cover one whole-catalog snapshot. Other gateway processes may serve an older snapshot until the configured TTL expires; there is no global linearizability claim.

The deterministic `TestLeaseCacheCanceledLeaderDoesNotFailLiveWaiter` test cancels the first caller while another caller waits on its shared flight. The live waiter elects a replacement fetch; unrelated backend errors still propagate to all callers.
