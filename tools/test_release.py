"""Bootstrap boundary checks: a second invocation must preserve private state."""
import base64
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

INIT = Path(__file__).with_name("init-config.py")


class BootstrapTest(unittest.TestCase):
    def test_private_configuration_and_existing_directory_rejection(self):
        with tempfile.TemporaryDirectory() as parent:
            target = Path(parent) / "instance"
            argv = [sys.executable, str(INIT), "--dir", str(target), "--authority", "test", "--port", "0"]
            result = subprocess.run(argv, capture_output=True, check=True)
            output = json.loads(result.stdout)
            config = json.loads((target / "server.json").read_text())
            token = (target / "owner-token").read_text().strip()
            secret = base64.urlsafe_b64decode(token + "=")
            self.assertEqual(len(secret), 32)
            self.assertEqual(config["credentials"][0]["digest"], hashlib.sha256(secret).hexdigest())
            self.assertNotIn(token, result.stdout.decode())
            self.assertFalse(output["initialized"])
            self.assertEqual(target.stat().st_mode & 0o777, 0o700)
            before = {p.name: p.read_bytes() for p in target.iterdir() if p.is_file()}
            self.assertTrue(all(p.stat().st_mode & 0o077 == 0 for p in target.iterdir()))
            rejected = subprocess.run(argv, capture_output=True)
            self.assertNotEqual(rejected.returncode, 0)
            self.assertEqual(before, {p.name: p.read_bytes() for p in target.iterdir() if p.is_file()})

    def test_invalid_port_does_not_create_directory(self):
        with tempfile.TemporaryDirectory() as parent:
            target = Path(parent) / "instance"
            result = subprocess.run([sys.executable, str(INIT), "--dir", str(target), "--port", "65536"], capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(target.exists())


if __name__ == "__main__":
    unittest.main()
