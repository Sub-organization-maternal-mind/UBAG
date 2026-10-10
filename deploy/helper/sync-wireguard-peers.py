#!/usr/bin/env python3
"""Primary root timer: apply only bounded UBAG tunnel peers published by the fleet manager."""
import base64
import ipaddress
import json
import re
import subprocess
from pathlib import Path

directory = Path('/var/lib/docker/volumes/oet-fleet_fleet_data/_data/ubag-wireguard-peers')
for path in sorted(directory.glob('*.json'))[:64]:
    if path.is_symlink() or path.stat().st_size > 2048:
        raise SystemExit('invalid peer document')
    value = json.loads(path.read_text())
    if set(value) != {'address', 'public_key'}:
        raise SystemExit('invalid peer fields')
    address = ipaddress.IPv4Address(value['address'])
    if address not in ipaddress.IPv4Network('10.253.240.0/24') or address in (ipaddress.IPv4Address('10.253.240.0'), ipaddress.IPv4Address('10.253.240.1'), ipaddress.IPv4Address('10.253.240.255')):
        raise SystemExit('invalid peer address')
    key = value['public_key']
    if not re.fullmatch(r'[A-Za-z0-9+/]{43}=', key) or len(base64.b64decode(key, validate=True)) != 32:
        raise SystemExit('invalid peer key')
    subprocess.run(['/usr/bin/wg', 'set', 'ubagwg', 'peer', key, 'allowed-ips', str(address) + '/32'], check=True)
