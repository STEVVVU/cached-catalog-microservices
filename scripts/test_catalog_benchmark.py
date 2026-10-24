import importlib.util
import gzip
import json
from pathlib import Path
import socket
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("catalog_benchmark", Path(__file__).with_name("catalog-benchmark.py"))
benchmark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(benchmark)


def complete_focused_receipt(output):
    plan = benchmark.FOCUSED_CACHE_PLAN
    measurement = {**plan, "preset": "focused-cache", "fixture_sha256": benchmark.CATALOG_HASHES[500], "order": "none/ttl/lease on even repeats, reversed on odd repeats", "duration_claim": "moderate fixed offered load, not a capacity claim", "native_latency_claim_requires": "all successful RPCs have native status-zero duration samples"}
    identity = {
        "measurement": measurement, "source_sha": "a" * 40,
        "source_sha256": {"src/productcatalogservice/server.go": "1" * 64, "src/productcatalogservice/go.mod": "1" * 64, "src/productcatalogservice/go.sum": "1" * 64, "src/productcatalogservice/products.json": "1" * 64},
        "binary_sha256": {"productcatalogservice": "2" * 64, "catalogctl": "3" * 64},
        "go_build_info": {"productcatalogservice": "go1.27.3 normal", "catalogctl": "go1.27.3 normal"},
        "k6_version": "k6 v2.3.0 (test)", "k6_binary_sha256": "4" * 64,
        "framework_sha256": {name: str(i) * 64 for i, name in enumerate(("scripts/catalog-benchmark.py", "scripts/catalog-k6.js", "scripts/catalog-oracle.mjs", "protos/demo.proto", ".github/workflows/catalog-k6.yml"), 5)},
    }
    samples = []
    for repeat in range(plan["repeats"]):
        modes = plan["modes"] if repeat % 2 == 0 else list(reversed(plan["modes"]))
        for mode in modes:
            label = f"n500-r1-ttl60s-rate100-rep{repeat}-{mode}"
            workload = {"rate": 100, "seconds": 20, "vus": 64, "targets": ["127.0.0.1:3550"], "products": 500, "planned_offers": 2000, "mix": plan["mix"]}
            oracle = {"offered": 1, "successful": 1, "failed": 0, "rejected": 0, "timeouts": 0, "correctness_errors": 0, "attempted": 1, "codes": {"OK": 1}}
            (output / f"{label}.oracle.json").write_text(json.dumps(oracle))
            (output / f"{label}.oracle.csv").write_text("index,scheduled_ms,start_ms,latency_from_schedule_ms,rpc_ms,validation_ms,code,response_bytes\n0,0,0,1,1,0,OK,10\n")
            metrics = {name: {"values": {"count": count}} for name, count in (("catalog_started", 2000), ("catalog_successful", 2000), ("catalog_correctness_errors", 0), ("catalog_completed", 2000))}
            for name in benchmark.FOCUSED_TRENDS:
                metrics[name] = {"values": {"count": 2000, "med": 1.0, "p(95)": 1.0, "p(99)": 1.0}}
            summary = {"native": {"metrics": metrics}, "workload": workload}
            (output / f"{label}.k6-summary.json").write_text(json.dumps(summary))
            raw_path = output / f"{label}.k6-raw.gz"
            with gzip.open(raw_path, "wt", encoding="utf-8") as stream:
                def point(name, value, tags=None):
                    stream.write(json.dumps({"type": "Point", "metric": name, "data": {"value": value, "tags": tags or {}}}) + "\n")

                for method, count in (("ListProducts", 1000), ("GetProduct", 800), ("SearchProducts", 200)):
                    point("catalog_started", count, {"method": method})
                for name, count in (("catalog_successful", 2000), ("catalog_correctness_errors", 0), ("catalog_completed", 2000)):
                    if count:
                        point(name, count)
                point("catalog_rpc_codes", 2000, {"code": "OK"})
                for name in benchmark.FOCUSED_TRENDS:
                    for _ in range(2000):
                        point(name.split("{")[0], 1.0, {"status": "0"} if name.startswith("grpc_req_duration") else {})
            for suffix in (".client.stdout.log", ".client.stderr", "-db.log", "-gateway0.log"):
                (output / f"{label}{suffix}").write_bytes(b"")
            samples.append({
                "label": label, "mode": mode, "repeat": repeat, "catalog_products": 500, "replicas": 1, "ttl": "60s", "offered_rate": 100, "duration_s": 20, "catalog_sha256": benchmark.CATALOG_HASHES[500],
                "workload": workload, "oracle": oracle, "planned_offers": 2000, "offered": 2000, "started": 2000, "successful": 2000,
                "failed": 0, "rejected": 0, "dropped_iterations": 0, "timeouts": 0, "correctness_errors": 0, "interrupted": 0,
                "native_status_zero_count": 2000, "native_duration_gap": 0, "codes": {"OK": 2000, "FAILED": 0, "DROPPED": 0, "INTERRUPTED": 0},
                "p50_ms": 1.0, "p95_ms": 1.0, "p99_ms": 1.0, "resources": [{"generator": {}, "db": {}, "gateways": [{}]}], "metrics_before": ["ok"], "metrics_after": ["ok"],
            })
    result = {"source_sha": identity["source_sha"], "focused_cache_identity": identity, "samples": samples, "cleanup": {"owned_processes_stopped": True, "scratch_removed": True}, "measurement_status": "inconclusive", "passed": False}
    (output / "result.json").write_text(json.dumps(result))
    return result


class GatewayPortTests(unittest.TestCase):
    def test_allocator_can_reuse_a_released_port_but_gateway_pair_is_distinct(self):
        reserved = set()

        class Socket:
            def __enter__(self):
                return self

            def bind(self, address):
                self.port = next(port for port in (41000, 41001) if port not in reserved)
                reserved.add(self.port)

            def getsockname(self):
                return "127.0.0.1", self.port

            def __exit__(self, *error):
                reserved.remove(self.port)

        with patch.object(benchmark.socket, "socket", Socket):
            self.assertEqual((benchmark.port(), benchmark.port()), (41000, 41000))
            grpc, admin = benchmark.gateway_ports()
            self.assertNotEqual(grpc, admin)
        self.assertEqual(reserved, set())

    def test_real_gateway_ports_are_released_for_owned_servers(self):
        grpc, admin = benchmark.gateway_ports()
        self.assertNotEqual(grpc, admin)
        with socket.socket() as first, socket.socket() as second:
            first.bind(("127.0.0.1", grpc))
            second.bind(("127.0.0.1", admin))


class FocusedCacheReceiptTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.output = Path(self.temp.name)
        self.result = complete_focused_receipt(self.output)

    def tearDown(self):
        self.temp.cleanup()

    def test_complete_15_cell_receipt_passes_and_hashes_artifacts(self):
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertEqual([], errors)
        self.assertEqual(15, len(self.result["samples"]))
        summary = json.loads((self.output / f"{self.result['samples'][0]['label']}.k6-summary.json").read_text())
        self.assertNotIn("catalog_failed", summary["native"]["metrics"])
        self.assertNotIn("catalog_timeouts", summary["native"]["metrics"])
        self.assertNotIn("dropped_iterations", summary["native"]["metrics"])
        for sample in self.result["samples"]:
            self.assertEqual(8, len(sample["artifact_receipts"]))
            self.assertTrue(all(len(item["sha256"]) == 64 for item in sample["artifact_receipts"].values()))

    def test_empty_native_stream_is_not_accepted(self):
        raw = self.output / f"{self.result['samples'][0]['label']}.k6-raw.gz"
        raw.write_bytes(b"")
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertTrue(any("raw trend" in error or "stream contains no metric points" in error for error in errors))

    def test_extra_native_iteration_is_not_accepted(self):
        sample = self.result["samples"][0]
        raw = self.output / f"{sample['label']}.k6-raw.gz"
        with gzip.open(raw, "at", encoding="utf-8") as stream:
            stream.write(json.dumps({"type": "Point", "metric": "catalog_started", "data": {"value": 1, "tags": {"method": "ListProducts"}}}) + "\n")
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertTrue(any("counts do not match the focused plan" in error for error in errors))

    def test_non_ok_native_rpc_code_is_not_accepted(self):
        raw = self.output / f"{self.result['samples'][0]['label']}.k6-raw.gz"
        with gzip.open(raw, "rt", encoding="utf-8") as stream:
            records = [json.loads(line) for line in stream]
        for record in records:
            if record.get("metric") == "catalog_rpc_codes":
                record["data"]["tags"]["code"] = "Unavailable"
        with gzip.open(raw, "wt", encoding="utf-8") as stream:
            stream.writelines(json.dumps(record) + "\n" for record in records)
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertTrue(any("counts do not match the focused plan" in error for error in errors))

    def test_persistence_accepts_linux_load_average_tuple(self):
        for status in ("inconclusive", "complete"):
            with self.subTest(status=status):
                result = {"measurement_status": status, "load": (0.1, 0.2, 0.3)}
                benchmark.persist_focused_cache_result(result, self.output)
                self.assertEqual([0.1, 0.2, 0.3], json.loads((self.output / "result.json").read_text())["load"])

    def test_persistence_rejects_changed_readback(self):
        with patch.object(Path, "read_text", return_value="{}"):
            with self.assertRaisesRegex(RuntimeError, "failed to persist inconclusive"):
                benchmark.persist_focused_cache_result({"measurement_status": "inconclusive"}, self.output)

    def test_truncated_native_stream_is_not_accepted(self):
        raw = self.output / f"{self.result['samples'][0]['label']}.k6-raw.gz"
        raw.write_bytes(b"\x1f\x8b\x08\x00")
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertTrue(any("raw receipt audit failed" in error for error in errors))

    def test_malformed_summary_json_is_not_accepted(self):
        summary = self.output / f"{self.result['samples'][0]['label']}.k6-summary.json"
        summary.write_text("{")
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertTrue(any("raw receipt audit failed" in error for error in errors))

    def test_invalid_identity_hash_is_not_accepted(self):
        self.result["focused_cache_identity"]["framework_sha256"]["scripts/catalog-k6.js"] = "bad"
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertTrue(any("SHA-256 identity is malformed" in error for error in errors))

    def test_missing_nonzero_native_counter_is_not_accepted(self):
        summary = self.output / f"{self.result['samples'][0]['label']}.k6-summary.json"
        data = json.loads(summary.read_text())
        del data["native"]["metrics"]["catalog_successful"]
        summary.write_text(json.dumps(data))
        errors = benchmark.focused_cache_receipt_errors(self.result, self.output)
        self.assertTrue(any("omits nonzero or required catalog_successful" in error for error in errors))


if __name__ == "__main__":
    unittest.main()
