import pathlib
import subprocess
import unittest
import os
import tempfile

class ChaosArguments(unittest.TestCase):
    def test_driver_kill_reaches_docker(self):
        with tempfile.TemporaryDirectory() as directory:
            docker = pathlib.Path(directory) / 'docker'
            docker.write_text('#!/bin/sh\ncase "$*" in *"ps -q"*) echo test-owned-container;; esac\nexit 0\n')
            docker.chmod(0o755)
            env = dict(os.environ, PATH=directory + ':' + os.environ['PATH'])
            result = subprocess.run(['bash', 'scripts/chaos.sh', 'kill', 'incubator-1'], env=env, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr.decode())

if __name__ == '__main__':
    unittest.main()
