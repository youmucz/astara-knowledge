import pathlib
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


if __name__ == "__main__":
    unittest.main()
