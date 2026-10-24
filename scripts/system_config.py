"""Resolve a separate, disposable Compose project without developer bind mounts."""
import copy
import json
import sys


def isolated(config, project, subnet):
    result = copy.deepcopy(config)
    result['name'] = project
    for network in result.get('networks', {}).values():
        network.pop('name', None)
        network.pop('external', None)
        network['ipam'] = {'config': [{'subnet': subnet + '.0/24', 'ip_range': subnet + '.128/25'}]}
    for volume in result.get('volumes', {}).values():
        volume.pop('name', None)
        volume.pop('external', None)
    for service in result['services'].values():
        for port in service.get('ports', []):
            port.pop('published', None)
            port['host_ip'] = '127.0.0.1'
        service['volumes'] = [v for v in service.get('volumes', []) if v['type'] != 'bind']
        for network in service.get('networks', {}).values():
            if 'ipv4_address' in network:
                network['ipv4_address'] = subnet + '.' + network['ipv4_address'].rsplit('.', 1)[1]
    return result


if __name__ == '__main__':
    json.dump(isolated(json.load(sys.stdin), sys.argv[1], sys.argv[2]), sys.stdout, indent=2)
