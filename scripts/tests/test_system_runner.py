import importlib.util
import io
import pathlib
import sys
import tempfile
import types
import unittest
from unittest import mock

sys.path.insert(0, str(pathlib.Path(__file__).parents[1]))
import system_test

class RunnerSafetyTests(unittest.TestCase):
    def test_reads_retry_but_mutations_do_not(self):
        with mock.patch.object(system_test.time, 'sleep'), mock.patch.object(system_test.urllib.request, 'urlopen', side_effect=[OSError('partition'), io.BytesIO(b'{"status":"running"}')]) as opener:
            self.assertEqual(system_test.request('http://example.test/runs'), {'status': 'running'})
            self.assertEqual(opener.call_count, 2)
        with mock.patch.object(system_test.urllib.request, 'urlopen', side_effect=OSError('lost response')) as opener:
            with self.assertRaises(system_test.urllib.error.URLError):
                system_test.request('http://example.test/runs', {})
            self.assertEqual(opener.call_count, 1)

    def test_read_only_graphql_retries_without_replaying_mutations(self):
        suite = object.__new__(system_test.Suite)
        suite.executor = 'http://example.test'
        with mock.patch.object(system_test.time, 'sleep'), mock.patch.object(system_test, 'request', side_effect=[OSError('not ready'), {'data': {}}]) as request:
            self.assertEqual(suite.graph('{ __schema { queryType { name } } }', read_only=True), {'data': {}})
            self.assertEqual(request.call_count, 2)
        with mock.patch.object(system_test, 'request', side_effect=OSError('uncertain response')) as request:
            with self.assertRaises(OSError):
                suite.graph('mutation { createRun { id } }')
            self.assertEqual(request.call_count, 1)

    def test_proven_dial_failure_can_retry_creation(self):
        failure = system_test.urllib.error.URLError(system_test.DialFailure('connect refused before send'))
        with mock.patch.object(system_test.time, 'sleep'), mock.patch.object(system_test.urllib.request, 'urlopen', side_effect=[failure, io.BytesIO(b'{"id":"run-1"}')]) as opener:
            self.assertEqual(system_test.request('http://example.test/runs', {}), {'id': 'run-1'})
            self.assertEqual(opener.call_count, 2)

    def test_first_failure_collects_cleanup_and_stops(self):
        with tempfile.TemporaryDirectory() as directory:
            suite = object.__new__(system_test.Suite)
            suite.args = types.SimpleNamespace(http_only=False, suite='routine', scenario=None)
            suite.artifacts = pathlib.Path(directory)
            suite.results = []
            suite.log = io.StringIO()
            suite.compose = mock.Mock()
            suite.start = mock.Mock(side_effect=AssertionError('injected scenario failure'))
            suite.exercise = mock.Mock()
            suite.cleanup = mock.Mock(return_value=[])
            with mock.patch("builtins.print"):
                self.assertEqual(suite.run(), 1)
            self.assertEqual(suite.start.call_count, 1)
            suite.exercise.assert_not_called()
            suite.cleanup.assert_called_once()
            self.assertEqual(suite.results[0]['status'], 'failed')
            self.assertTrue((suite.artifacts / 'results.json').exists())


class RetryOracleTests(unittest.TestCase):
    def test_exhaustion_is_per_step_not_per_device(self):
        executions = [{'step_name': 'incubate_samples'}] * 3 + [{'step_name': 'warm_reagent_plate'}] * 2
        system_test.assert_retry_exhaustion(executions)
        with self.assertRaises(AssertionError):
            system_test.assert_retry_exhaustion([{'step_name': 'incubate_samples'}] * 4)

if __name__ == "__main__":
    unittest.main()
