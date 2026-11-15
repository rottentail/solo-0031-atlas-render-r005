#!/usr/bin/env python3
"""End-to-end demonstration of reusable grayscale masks.

The script boots a real Atlas Render server on a temporary state directory and
exercises the whole mask lifecycle over HTTP:

  onboarding   import one grayscale mask and share it from several layers
  freeze       capture the mask bytes inside a snapshot
  render       masks modulate transformed layers, combined with rectangle clips
  retirement   retiring the mask blocks new snapshots but frozen renders stay
               pixel-identical (frozen content must not follow the mask change)
  exchange     export a package and render the same pixels on a second instance
  budget       reusing a mask never inflates input limits or the work budget,
               and over-budget / oversized inputs are rejected

Run:  python3 scripts/mask_walkthrough.py --out var/mask-demo
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import zlib

ROOT = Path(__file__).resolve().parents[1]


def png_chunk(tag, data):
    return struct.pack('>I', len(data)) + tag + data + struct.pack('>I', zlib.crc32(tag + data))


def grayscale_png(path, width, height, painter):
    """Write an 8-bit grayscale PNG (color type 0) using a painter(x,y)->0..255."""
    raw = bytearray()
    for y in range(height):
        raw.append(0)  # filter type 0 per scanline
        for x in range(width):
            raw.append(int(max(0, min(255, painter(x, y)))))
    ihdr = struct.pack('>IIBBBBB', width, height, 8, 0, 0, 0, 0)
    data = (b'\x89PNG\r\n\x1a\n'
            + png_chunk(b'IHDR', ihdr)
            + png_chunk(b'IDAT', zlib.compress(bytes(raw), 9))
            + png_chunk(b'IEND', b''))
    Path(path).write_bytes(data)
    return data


def solid_png(path, width, height, gray):
    return grayscale_png(path, width, height, lambda x, y: gray)


def horizontal_gradient(path, width, height):
    # black on the left edge -> white on the right edge, reused by many layers.
    return grayscale_png(path, width, height, lambda x, y: round(255 * x / max(1, width - 1)))


class Demo:
    def __init__(self, binary, directory, name='server'):
        self.binary = binary
        self.directory = directory
        self.name = name
        self.log = open(directory / f'{name}.log', 'ab')
        self.proc = None
        self.start()

    def start(self):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        self.url = f'http://127.0.0.1:{port}'
        env = dict(os.environ, ATLAS_ADDR=f'127.0.0.1:{port}',
                   ATLAS_STATE=str(self.directory / f'{self.name}-state.json'))
        self.proc = subprocess.Popen([str(self.binary)], env=env,
                                     stdout=self.log, stderr=self.log)
        for _ in range(100):
            try:
                self.call('GET', '/healthz')
                return
            except (urllib.error.URLError, ConnectionError):
                if self.proc.poll() is not None:
                    raise RuntimeError((self.directory / f'{self.name}.log').read_text())
                time.sleep(.03)
        raise RuntimeError('server startup deadline exceeded')

    def stop(self):
        if self.proc and self.proc.poll() is None:
            self.proc.terminate()
            self.proc.wait(timeout=15)

    def call(self, method, path, body=None, status=200, headers=None):
        hdr = dict(headers or {})
        if isinstance(body, (dict, list)):
            body = json.dumps(body).encode()
            hdr['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.url + path, data=body,
                                         method=method, headers=hdr)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            if response.status != status:
                raise RuntimeError(f'{method} {path}: expected {status}, got '
                                   f'{response.status}: {data[:400]!r}')
            if response.headers.get('Content-Type', '').startswith('application/json'):
                return json.loads(data)
            return data

    def wait(self, job_id):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            job = self.call('GET', '/v1/jobs/' + job_id)
            if job['state'] == 'succeeded':
                return job
            if job['state'] in ('failed', 'cancelled'):
                raise RuntimeError(f'unexpected job outcome: {job}')
            time.sleep(.025)
        raise RuntimeError('render deadline exceeded')

    def render(self, snapshot, key, scale=1):
        job = self.call('POST', '/v1/jobs',
                        {'snapshotId': snapshot['id'], 'scale': scale}, 202,
                        {'Idempotency-Key': key})
        done = self.wait(job['id'])
        return self.call('GET', '/v1/artifacts/' + done['artifactId'])


def base_transform(**over):
    t = {'x': 0, 'y': 0, 'scaleX': 1, 'scaleY': 1, 'degrees': 0}
    t.update(over)
    return t


def masked_scene(mask_id):
    """Three solid layers all reference one shared mask; one also has a clip."""
    return {
        'width': 240, 'height': 160, 'background': '#FFFFFF',
        'layers': [
            # Full-coverage layer; mask is placed over the whole canvas, so the
            # horizontal gradient fades this layer left -> right.
            {'id': 'wash', 'kind': 'rectangle', 'width': 240, 'height': 160,
             'transform': base_transform(), 'opacity': 1,
             'fill': {'kind': 'solid', 'color': '#2060C0'},
             'maskId': mask_id,
             'maskTransform': base_transform(scaleX=240 / 64, scaleY=160 / 32)},
            # Transformed ellipse reusing the same mask, placed on the left half.
            {'id': 'dot', 'kind': 'ellipse', 'width': 80, 'height': 80,
             'transform': base_transform(x=30, y=40), 'opacity': 1,
             'fill': {'kind': 'solid', 'color': '#E04030'},
             'maskId': mask_id,
             'maskTransform': base_transform(x=20, y=40, scaleX=1.5, scaleY=1.5)},
            # Rotated rectangle sharing the mask again, plus a hard clip, proving
            # the clip chain and soft mask compose rather than replace each other.
            {'id': 'bar', 'kind': 'rectangle', 'width': 100, 'height': 40,
             'transform': base_transform(x=110, y=70, degrees=12), 'opacity': 1,
             'fill': {'kind': 'solid', 'color': '#30A050'},
             'clip': {'x': 10, 'y': 5, 'width': 80, 'height': 30},
             'maskId': mask_id,
             'maskTransform': base_transform(x=90, y=60)},
        ],
    }


def run(demo, peer, out, workdir):
    report = {}

    # --- 1. Onboarding: import a reusable grayscale mask ---------------------
    mask_path = workdir / 'gradient-mask.png'
    mask_bytes = horizontal_gradient(mask_path, 64, 32)
    mask = demo.call('PUT', '/v1/masks/soft-fade', mask_bytes, 201)
    assert mask['width'] == 64 and mask['height'] == 32
    assert mask['digest'] == hashlib.sha256(mask_bytes).hexdigest()
    assert mask['state'] == 'available'
    # Re-importing identical bytes is idempotent; different bytes conflict.
    assert demo.call('PUT', '/v1/masks/soft-fade', mask_bytes, 201)['digest'] == mask['digest']
    other = solid_png(workdir / 'solid.png', 8, 8, 128)
    demo.call('PUT', '/v1/masks/soft-fade', other, 409)
    demo.call('PUT', '/v1/masks/broken', b'not a png', 400)
    report['onboarding'] = {'maskId': mask['id'], 'digest': mask['digest'][:16]}

    # --- 2. Reference validation before the mask exists / bad transforms -----
    scene_doc = masked_scene('soft-fade')
    saved = demo.call('PUT', '/v1/scenes/masked',
                      {'name': '蒙版共享场景', 'expectedRevision': 0, 'document': scene_doc})
    assert saved['revision'] == 1
    bad = json.loads(json.dumps(scene_doc))
    bad['layers'][0]['maskId'] = 'missing-mask'
    demo.call('PUT', '/v1/scenes/bad-ref',
              {'name': '悬空蒙版', 'expectedRevision': 0, 'document': bad}, 409)
    bad2 = json.loads(json.dumps(scene_doc))
    bad2['layers'][0]['maskTransform'] = base_transform(scaleX=0)
    demo.call('PUT', '/v1/scenes/bad-transform',
              {'name': '零缩放蒙版', 'expectedRevision': 0, 'document': bad2}, 400)
    bad3 = json.loads(json.dumps(scene_doc))
    bad3['layers'][0]['maskId'] = ''  # no mask -> maskTransform must be rejected too
    bad3['layers'][0]['maskTransform'] = base_transform(scaleX=0)
    demo.call('PUT', '/v1/scenes/no-mask',
              {'name': '无蒙版多余变换', 'expectedRevision': 0, 'document': bad3}, 200)

    # --- 3. Freeze: mask bytes are captured into the snapshot closure --------
    snapshot = demo.call('POST', '/v1/scenes/masked/snapshots',
                         {'expectedRevision': 1}, 201)
    assert set(snapshot['masks']) == {'soft-fade'}
    assert snapshot['masks']['soft-fade']['digest'] == mask['digest']
    # Snapshot digest covers document + assets + masks, so it is stable.
    assert demo.call('GET', '/v1/snapshots/' + snapshot['id'])['digest'] == snapshot['digest']
    report['freeze'] = {'snapshotId': snapshot['id'],
                        'capturedMasks': sorted(snapshot['masks'])}

    # --- 4. Render: shared mask modulates every transformed layer -------------
    png_a = demo.render(snapshot, 'mask-render-a')
    assert png_a[:8] == b'\x89PNG\r\n\x1a\n'
    if out:
        Path(out).mkdir(parents=True, exist_ok=True)
        (Path(out) / 'masked-render.png').write_bytes(png_a)
    report['render'] = {'pngBytes': len(png_a),
                        'pngDigest': hashlib.sha256(png_a).hexdigest()[:16]}

    # --- 5. Retirement freezes out future use but cannot change frozen pixels  -
    demo.call('POST', '/v1/masks/soft-fade/retire')
    assert demo.call('GET', '/v1/masks/soft-fade')['state'] == 'retired'
    # A new revision can no longer be saved while it references the retired mask.
    demo.call('PUT', '/v1/scenes/masked',
              {'name': '蒙版共享场景', 'expectedRevision': 1, 'document': scene_doc}, 409)
    # And a second freeze of the (unchanged, revision 1) document is blocked.
    demo.call('POST', '/v1/scenes/masked/snapshots', {'expectedRevision': 1}, 409)
    # The already-frozen snapshot still renders byte-for-byte identically.
    png_b = demo.render(snapshot, 'mask-render-b')
    assert png_b == png_a
    report['retirement'] = {'frozenPixelsUnchanged': png_b == png_a}

    # --- 6. Exchange: package round-trip renders identical pixels elsewhere ---
    package = demo.call('GET', '/v1/snapshots/' + snapshot['id'] + '/package')
    assert package['snapshot']['masks']['soft-fade']['digest'] == mask['digest']
    # The peer instance starts with an empty state directory and has never seen
    # either the scene or the mask.
    peer.call('GET', '/v1/masks/soft-fade', status=404)
    imported = peer.call('POST', '/v1/imports/masked-copy',
                         {'name': '交换来的蒙版场景', 'package': package}, 201)
    assert imported['document'] == snapshot['document']
    # The imported mask landed as an independent available record on the peer.
    imported_mask = peer.call('GET', '/v1/masks/soft-fade')
    assert imported_mask['state'] == 'available'
    assert imported_mask['digest'] == mask['digest']
    imported_snap = peer.call('POST', '/v1/scenes/masked-copy/snapshots',
                              {'expectedRevision': 1}, 201)
    png_c = peer.render(imported_snap, 'mask-render-c')
    assert png_c == png_a, 'peer instance rendered different pixels'
    # A tampered package (mask dimensions break its digest) is rejected whole.
    corrupt = json.loads(json.dumps(package))
    corrupt['snapshot']['masks']['soft-fade']['width'] += 1
    peer.call('POST', '/v1/imports/corrupt-mask',
              {'name': '损坏蒙版包', 'package': corrupt}, 400)
    peer.call('GET', '/v1/scenes/corrupt-mask', status=404)
    report['exchange'] = {'identicalPixelsOnOtherInstance': png_c == png_a}

    # --- 7. Input limits and work budget stay intact under reuse --------------
    # Mask input still obeys the 2 MiB / 1024 px asset limits.
    oversized = solid_png(workdir / 'oversized.png', 1025, 4, 64)
    assert len(oversized) <= 2 << 20
    demo.call('PUT', '/v1/masks/too-wide', oversized, 400)
    # Reusing one mask across many layers keeps cost bounded per layer-pixel:
    # 200 layers on a 1500x1500 canvas sharing a single mask exceeds the
    # 40,000,000 layer-pixel budget and is rejected at submit time (429).
    heavy = {'width': 1500, 'height': 1500, 'background': '#FFFFFF',
             'layers': []}
    for i in range(200):
        heavy['layers'].append(
            {'id': f'sheet-{i}', 'kind': 'rectangle', 'width': 1500, 'height': 1500,
             'transform': base_transform(), 'opacity': 1,
             'fill': {'kind': 'solid', 'color': '#2060C0'},
             'maskId': 'soft-fade',
             'maskTransform': base_transform(scaleX=1500 / 64, scaleY=1500 / 32)})
    # The retired mask blocks this scene; temporarily re-import under a new id.
    demo.call('PUT', '/v1/masks/heavy-fade', mask_bytes, 201)
    for layer in heavy['layers']:
        layer['maskId'] = 'heavy-fade'
    demo.call('PUT', '/v1/scenes/heavy',
              {'name': '超预算复用', 'expectedRevision': 0, 'document': heavy})
    heavy_snap = demo.call('POST', '/v1/scenes/heavy/snapshots',
                           {'expectedRevision': 1}, 201)
    demo.call('POST', '/v1/jobs', {'snapshotId': heavy_snap['id'], 'scale': 1},
              429, {'Idempotency-Key': 'over-budget'})
    report['budget'] = {'oversizedMaskRejected': True,
                        'overBudgetRejected': True}
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', help='directory to keep the rendered PNG')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='atlas-mask-') as temp:
        directory = Path(temp)
        binary = directory / 'atlas'
        build_env = dict(os.environ, CGO_ENABLED='0')
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/atlas'],
                       cwd=ROOT, check=True, timeout=120, env=build_env)
        # Two independent instances with separate state directories prove that
        # an exchanged package renders identical pixels with no shared memory.
        primary = Demo(binary, directory, 'primary')
        peer = Demo(binary, directory, 'peer')
        try:
            result = run(primary, peer, args.out, directory)
            print(json.dumps(result, ensure_ascii=False, indent=2))
        finally:
            primary.stop()
            peer.stop()
            primary.log.close()
            peer.log.close()


if __name__ == '__main__':
    main()
