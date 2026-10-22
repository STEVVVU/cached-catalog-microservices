import importlib.util
from pathlib import Path
import socket
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("catalog_benchmark", Path(__file__).with_name("catalog-benchmark.py"))
benchmark = importlib.util.module_from_spec(spec)
spec.loader.exec_module(benchmark)


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


if __name__ == "__main__":
    unittest.main()
