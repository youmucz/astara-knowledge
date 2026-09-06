import pathlib
import re
import unittest


class ComposeInventoryTest(unittest.TestCase):
    def test_closed_service_and_mount_inventory(self):
        text = pathlib.Path(__file__).with_name("compose.yml").read_text()
        for service in ("api", "web", "docreader", "db", "redis"):
            self.assertIn(f"  {service}:\n", text)
        for prohibited in ("sandbox:", "neo4j:", "mcp:", "searxng:", "skills:"):
            self.assertNotIn(prohibited, text)
        self.assertNotIn("ports:", text)
        self.assertNotIn("docker.sock", text)

    def test_no_plaintext_secret_values(self):
        """Every secret must arrive through an environment reference.

        A literal secret value in the compose file would leak into every
        deployment's revision history; only ``${...}`` indirections are
        allowed for secret-bearing keys.
        """
        text = pathlib.Path(__file__).with_name("compose.yml").read_text()
        secret_keys = (
            "ASTARA_SERVICE_AUTH_SECRET",
            "ASTARA_IDENTITY_EXCHANGE_SECRET",
            "ASTARA_IDENTITY_EXCHANGE_SECRET_PREVIOUS",
            "SYSTEM_AES_KEY",
            "DB_PASSWORD",
            "POSTGRES_PASSWORD",
            "GRPC_AUTH_TOKEN",
        )
        for key in secret_keys:
            for match in re.finditer(rf"^\s+{key}:\s*(.+)$", text, re.MULTILINE):
                value = match.group(1).strip()
                self.assertTrue(
                    value.startswith("${") or value == "",
                    f"{key} must reference the environment, found a literal value",
                )

    def test_no_execution_surfaces(self):
        """No service may run privileged, add caps, or mount host runtime."""
        text = pathlib.Path(__file__).with_name("compose.yml").read_text()
        for prohibited in ("privileged:", "cap_add:", "/var/run/docker.sock", "userns_mode:"):
            self.assertNotIn(prohibited, text, f"execution surface must stay absent: {prohibited}")

    def test_stateful_services_stay_private(self):
        """The stack defines no published ports and no host networking.

        Every service stays on the compose project's internal default
        network; exposure happens only through the Plane edge proxy.
        """
        text = pathlib.Path(__file__).with_name("compose.yml").read_text()
        self.assertNotIn("network_mode: host", text)


if __name__ == "__main__":
    unittest.main()
