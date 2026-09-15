"""Read-only H.264 contract gate for the exact ZIP embedded in a release EXE."""
import hashlib
import json
import sys
import zipfile

BRIDGE = 'gstreamer/airplay-gstreamer-bridge.exe'
PROOF = 'gstreamer/imagepad-airplay-gstreamer-bridge-build.json'


def verify(path):
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        if names.count(BRIDGE) != 1 or names.count(PROOF) != 1:
            raise ValueError('exactly one bridge and H.264 build provenance are required')
        if archive.getinfo(PROOF).file_size > 1024 * 1024:
            raise ValueError('oversized H.264 build provenance')
        record = json.loads(archive.read(PROOF))
        if not isinstance(record, dict):
            raise ValueError('invalid H.264 build provenance')
        contract = record.get('videoContract')
        if (record.get('schema') != 1 or record.get('configuration') != 'Release'
                or record.get('executable') != 'airplay-gstreamer-bridge.exe'
                or not isinstance(contract, dict)
                or contract.get('id') != 'rtsp-h264-single-slice-v1'
                or type(contract.get('slicesPerFrame')) is not int
                or contract['slicesPerFrame'] != 1
                or contract.get('testName') != 'airplay_source_clock_single_slice'
                or contract.get('testPassed') is not True):
            raise ValueError('missing or unverified H.264 single-slice contract')
        digest = hashlib.sha256()
        with archive.open(BRIDGE) as bridge:
            for block in iter(lambda: bridge.read(1024 * 1024), b''):
                digest.update(block)
        if str(record.get('executableSha256', '')).lower() != digest.hexdigest():
            raise ValueError('H.264 bridge hash does not match the tested executable')


if __name__ == '__main__':
    try:
        verify(sys.argv[1])
    except (OSError, ValueError, KeyError, zipfile.BadZipFile) as error:
        sys.exit('H.264 archive contract rejected: ' + str(error))
    print('H.264 archive contract: PASS (rtsp-h264-single-slice-v1)')
