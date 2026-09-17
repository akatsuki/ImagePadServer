"""Protocol, lifetime, readback and exact-RGBA checks for the shipped helper."""
import argparse
import hashlib
import struct
import subprocess

U = lambda *v: struct.pack('<' + 'I' * len(v), *v)
IDENTITY = (1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1)

def command(i, texture=0):
    color = [0., 0., 0., 1.]
    color[i % 3] = 1.
    if texture:
        color = [1., 0., 0., 0.]
    return U(texture) + struct.pack('<24f', -1, -1, 2, 2, *IDENTITY, *color)

def make_scene(n, with_texture=False):
    tex = U(1, 1, 1) + bytes((50, 70, 90, 255)) if with_texture else b''
    frames = [command(i, int(with_texture)) for i in range(n)]
    data = U(0x3353504e, 33, 19, n, 30, 1)
    for start in range(0, n, 30):
        end = min(start+30, n)
        batch = U(int(with_texture and start == 0)) + (tex if start == 0 else b'') + U(end-start)
        for i in range(start, end):
            batch += struct.pack('<QQI', i, i*1000//30, 1) + frames[i]
        batch += U(int(with_texture and end == n)) + (U(1) if with_texture and end == n else b'')
        data += U(len(batch)) + batch
    return data + U(0), frames, tex

def main():
    p = argparse.ArgumentParser()
    p.add_argument('exe')
    p.add_argument('--reference')
    a = p.parse_args()
    checks = 0
    for n in (1, 2, 3, 5, 31):
        for textured in (False, True):
            data, frames, tex = make_scene(n, textured)
            reference = None
            for flags in ([], ['--copy-output']):
                r = subprocess.run([a.exe, '--stdin', *flags], input=data, capture_output=True, timeout=15)
                assert r.returncode == 0, (r.returncode, r.stderr)
                assert len(r.stdout) == n*33*19*4
                assert ('NICO_DONE %d' % n).encode() in r.stderr
                if reference is None:
                    reference = r.stdout
                assert r.stdout == reference, 'copy/direct pixels differ'
                for i in range(n):
                    expected = bytes((50,70,90,255)) if textured else bytes([255 if j == i%3 or j == 3 else 0 for j in range(4)])
                    assert r.stdout[i*33*19*4:(i*33*19+1)*4] == expected
                checks += 1
            # Tests reject truncation, extra bytes, unknown/deleted ID, or duplicate sequence.
            invalid = [data[:-1], data+b'x', data[:16]+U(0,1)+data[24:]]
            if n > 1:
                bad = bytearray(data)
                offset = 24+4+4+len(tex)+4 + 20+100
                bad[offset:offset+8] = struct.pack('<Q', 0)
                invalid.append(bytes(bad))
            if textured:
                bad = bytearray(data)
                bad[24+4+4+len(tex)+4+20:24+4+4+len(tex)+4+24] = U(99)
                invalid.append(bytes(bad))
            for bad in invalid:
                r = subprocess.run([a.exe, '--stdin'], input=bad, capture_output=True, timeout=15)
                assert r.returncode != 0, 'invalid stream accepted'
                assert b'NICO_DONE ' not in r.stderr
                checks += 1
    # A retired ID must not be recreated or referenced in a later batch.
    data, frames, tex = make_scene(1, True)
    header = U(0x3353504e,33,19,2,30,1)
    for readd in (False,True):
        batch = U(int(readd))+(tex if readd else b'')+U(1)+struct.pack('<QQI',1,33,1)+command(0,1)+U(0)
        bad=header+data[24:-4]+U(len(batch))+batch+U(0)
        r=subprocess.run([a.exe,'--stdin'],input=bad,capture_output=True,timeout=15)
        assert r.returncode != 0
        checks += 1
    print('PASS', checks, 'protocol/pixel/copy/lifetime checks')

if __name__ == '__main__':
    main()
