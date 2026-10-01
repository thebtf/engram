"""Check the shipped Compose entrypoints' operator peer contract."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
COMPOSE_FILES = (ROOT / "docker-compose.yml", ROOT / "deploy/docker-compose.runtime.yml")
REQUIRED = (
    "OPERATOR_CONSOLE_NETWORK_SUBNET",
    "OPERATOR_CONSOLE_NETWORK_IP_RANGE",
    "OPERATOR_CONSOLE_TRUSTED_PROXY_IP",
    "OPERATOR_CONSOLE_PUBLIC_ORIGIN",
)
INPUTS = {
    "ENGRAM_SERVER_IMAGE": "example.test/engram:check",
    "ENGRAM_OPERATOR_IMAGE": "example.test/operator:check",
    "ENGRAM_POSTGRES_IMAGE": "example.test/postgres:check",
    "ENGRAM_BUILD_VERSION": "sha-" + "a" * 40,
    "POSTGRES_PASSWORD": "test-password",
    "OPERATOR_CONSOLE_NETWORK_SUBNET": "10.241.250.0/24",
    "OPERATOR_CONSOLE_NETWORK_IP_RANGE": "10.241.250.128/25",
    "OPERATOR_CONSOLE_TRUSTED_PROXY_IP": "10.241.250.10",
    "OPERATOR_CONSOLE_PUBLIC_ORIGIN": "http://localhost:3000",
}


class OperatorNetworkComposeTest(unittest.TestCase):
    def setUp(self):
        self.env_file = tempfile.TemporaryDirectory()
        self.addCleanup(self.env_file.cleanup)
        self.empty_env = Path(self.env_file.name) / "empty.env"
        self.empty_env.touch()

    def render(self, compose_file, inputs):
        env = os.environ.copy()
        for key in INPUTS:
            env.pop(key, None)
        env.update(inputs)
        return subprocess.run(
            ["docker", "compose", "--env-file", str(self.empty_env), "-f", str(compose_file),
             "config", "--format", "json"],
            cwd=ROOT, env=env, text=True, capture_output=True, check=False,
        )

    def test_required_inputs_fail_without_deployment(self):
        for compose_file in COMPOSE_FILES:
            for key in REQUIRED:
                for missing in (True, False):
                    with self.subTest(compose_file=compose_file.name, key=key, unset=missing):
                        inputs = INPUTS.copy()
                        if missing:
                            inputs.pop(key)
                        else:
                            inputs[key] = ""
                        result = self.render(compose_file, inputs)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn(key, result.stderr)

    def test_exact_peer_is_outside_dynamic_pool_on_dedicated_bridge(self):
        for compose_file in COMPOSE_FILES:
            with self.subTest(compose_file=compose_file.name):
                result = self.render(compose_file, INPUTS)
                self.assertEqual(result.returncode, 0, result.stderr)
                config = json.loads(result.stdout)
                services = config["services"]
                self.assertEqual(set(services["postgres"]["networks"]), {"default"})
                self.assertEqual(set(services["server"]["networks"]), {"default", "operator"})
                self.assertEqual(set(services["operator-console"]["networks"]), {"operator"})
                self.assertEqual(services["server"]["environment"]["ENGRAM_AUTH_TRUSTED_PROXY"], INPUTS["OPERATOR_CONSOLE_TRUSTED_PROXY_IP"])
                self.assertEqual(services["operator-console"]["networks"]["operator"]["ipv4_address"], INPUTS["OPERATOR_CONSOLE_TRUSTED_PROXY_IP"])
                self.assertEqual(services["operator-console"]["environment"]["NUXT_OPERATOR_PUBLIC_ORIGIN"], INPUTS["OPERATOR_CONSOLE_PUBLIC_ORIGIN"])
                self.assertEqual(services["operator-console"]["environment"]["NUXT_OPERATOR_API_TARGET"], "http://server:37777")
                self.assertEqual(config["networks"]["operator"]["ipam"]["config"], [{"subnet": INPUTS["OPERATOR_CONSOLE_NETWORK_SUBNET"], "ip_range": INPUTS["OPERATOR_CONSOLE_NETWORK_IP_RANGE"]}])
                self.assertEqual(config["networks"]["default"].get("ipam", {}), {})

if __name__ == "__main__":
    unittest.main()
