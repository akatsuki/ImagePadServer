"""Check the exact source/helper pair embedded by the Windows release build."""
import hashlib
import json
import pathlib
import struct
import sys

root = pathlib.Path(sys.argv[1]).resolve()
base = root / 'internal/nicorender/payload'
meta = json.loads((base / 'nico-compositor.json').read_text(encoding='utf-8-sig'))
exe = (base / 'nico-compositor.exe').read_bytes()
source = (root / 'native/nico-compositor/main.cpp').read_bytes()
assert meta['schema'] == 1 and meta['protocol'] == 'NPS3'
assert meta['architecture'] == 'amd64' and meta['backend'] == 'WARP'
assert meta['selfTest'] == 'passed'
assert hashlib.sha256(exe).hexdigest() == meta['sha256']
assert hashlib.sha256(source).hexdigest() == meta['sourceSha256']
assert exe[:2] == b'MZ'
pe = struct.unpack_from('<I', exe, 0x3c)[0]
assert exe[pe:pe+4] == b'PE\0\0' and struct.unpack_from('<H', exe, pe+4)[0] == 0x8664
print('Nico payload/source/architecture verified:', meta['sha256'])
