import importlib.util
import pathlib
import unittest
spec = importlib.util.spec_from_file_location('system_config', pathlib.Path(__file__).parents[1] / 'system_config.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class IsolationTests(unittest.TestCase):
    def test_configuration_owns_ports_networks_and_volumes(self):
        original = {'name': 'developer', 'services': {'executor': {'ports': [{'target': 5001, 'published': '5002'}], 'volumes': [{'type': 'bind', 'source': '/private/config', 'target': '/app/config'}], 'networks': {'lab-network': {'ipv4_address': '172.31.250.20'}}}}, 'networks': {'lab-network': {'name': 'developer', 'ipam': {'config': [{'subnet': '172.31.250.0/24'}]}}}, 'volumes': {'ledger': {'name': 'developer_ledger'}}}
        result = module.isolated(original, 'lab-test-1', '172.29.42')
        self.assertEqual(result['name'], 'lab-test-1')
        self.assertNotIn('published', result['services']['executor']['ports'][0])
        self.assertEqual(result['services']['executor']['ports'][0]['host_ip'], '127.0.0.1')
        self.assertEqual(result['services']['executor']['networks']['lab-network']['ipv4_address'], '172.29.42.20')
        self.assertEqual(result['services']['executor']['volumes'], [])
        self.assertNotIn('name', result['volumes']['ledger'])
        self.assertEqual(original['name'], 'developer')

if __name__ == '__main__':
    unittest.main()
