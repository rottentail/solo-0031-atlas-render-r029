#!/usr/bin/env python3
"""Run bounded production flows against a temporary Atlas Render server."""
import argparse
import base64
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


def sample():
    return {"width": 320, "height": 240, "background": "#F5F1E8", "layers": [
        {"id": "plate", "kind": "rectangle", "width": 220, "height": 150,
         "transform": {"x": 45, "y": 35, "scaleX": 1, "scaleY": 1, "degrees": 5},
         "opacity": 1, "fill": {"kind": "linear", "color": "#145A70", "endColor": "#32A5A0", "from": {"x": 0, "y": 0}, "to": {"x": 220, "y": 150}}},
        {"id": "accent", "kind": "ellipse", "width": 130, "height": 130,
         "transform": {"x": 150, "y": 95, "scaleX": 1, "scaleY": 1, "degrees": 0},
         "opacity": .85, "fill": {"kind": "solid", "color": "#E87E4F"}},
        {"id": "inset", "kind": "group", "transform": {"x": 150, "y": 100, "scaleX": 1, "scaleY": 1, "degrees": -8}, "opacity": .9,
         "clip": {"x": 0, "y": 0, "width": 90, "height": 75}, "children": [
             {"id": "facet", "kind": "polygon", "width": 100, "height": 100,
              "transform": {"scaleX": 1, "scaleY": 1}, "opacity": .8,
              "fill": {"kind": "solid", "color": "#FFF4D5"}, "points": [{"x": 10, "y": 90}, {"x": 50, "y": 0}, {"x": 100, "y": 90}]}]}]}


def tiny_png():
    def chunk(tag, data):
        return struct.pack('>I', len(data)) + tag + data + struct.pack('>I', zlib.crc32(tag + data))
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 2, 2, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(b'\0' + bytes([240, 40, 80, 255] * 2) + b'\0' + bytes([40, 80, 240, 128] * 2))) + chunk(b'IEND', b'')


def solid_png(rgb):
    def chunk(tag, data):
        return struct.pack('>I', len(data)) + tag + data + struct.pack('>I', zlib.crc32(tag + data))
    raw = b''.join(b'\0' + bytes(list(rgb) + [255]) * 2 for _ in range(2))
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 2, 2, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(raw)) + chunk(b'IEND', b'')


class Demo:
    def __init__(self, binary, directory):
        self.binary = binary
        self.directory = directory
        self.proc = None
        self.log = open(directory / 'server.log', 'ab')
        self.start()

    def start(self):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        self.url = f'http://127.0.0.1:{port}'
        env = dict(os.environ, ATLAS_ADDR=f'127.0.0.1:{port}', ATLAS_STATE=str(self.directory / 'state.json'))
        self.proc = subprocess.Popen([str(self.binary)], env=env, stdout=self.log, stderr=self.log)
        for _ in range(100):
            try:
                self.call('GET', '/healthz')
                return
            except (urllib.error.URLError, ConnectionError):
                if self.proc.poll() is not None:
                    raise RuntimeError((self.directory / 'server.log').read_text())
                time.sleep(.03)
        raise RuntimeError('server startup deadline exceeded')

    def stop(self, abrupt=False):
        if self.proc and self.proc.poll() is None:
            (self.proc.kill if abrupt else self.proc.terminate)()
            self.proc.wait(timeout=15)

    def call(self, method, path, body=None, status=200, headers=None):
        hdr = dict(headers or {})
        if isinstance(body, (dict, list)):
            body = json.dumps(body).encode()
            hdr['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.url + path, data=body, method=method, headers=hdr)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            if response.status != status:
                raise RuntimeError(f'{method} {path}: expected {status}, received {response.status}: {data[:500]!r}')
            if response.headers.get('Content-Type', '').startswith('application/json'):
                return json.loads(data)
            return data

    def wait(self, job_id, wanted=('succeeded',)):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            job = self.call('GET', '/v1/jobs/' + job_id)
            if job['state'] in wanted:
                return job
            if job['state'] in ('failed', 'cancelled'):
                raise RuntimeError(f'unexpected job outcome: {job}')
            time.sleep(.025)
        raise RuntimeError('render deadline exceeded')

    def submit(self, snapshot, key='render-one', scale=1):
        return self.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': scale}, 202, {'Idempotency-Key': key})


def run_recover_archived(demo):
    import threading

    png_beta = solid_png((20, 160, 120))
    png_gamma = solid_png((10, 10, 10))
    png_delta = solid_png((90, 90, 200))
    demo.call('PUT', '/v1/assets/beta', png_beta, 201)
    demo.call('PUT', '/v1/assets/gamma', png_gamma, 201)
    demo.call('PUT', '/v1/assets/delta', png_delta, 201)
    demo.call('PUT', '/v1/assets/alpha', solid_png((200, 30, 60)), 201)

    d = sample()
    d['layers'].append({'id': 'alpha-img', 'kind': 'image', 'width': 40, 'height': 40, 'assetId': 'alpha',
                        'opacity': 1, 'transform': {'x': 10, 'y': 10, 'scaleX': 1, 'scaleY': 1, 'degrees': 0}})
    d['layers'].append({'id': 'gamma-img', 'kind': 'image', 'width': 20, 'height': 20, 'assetId': 'gamma',
                        'opacity': 1, 'transform': {'x': 80, 'y': 80, 'scaleX': 1, 'scaleY': 1, 'degrees': 0}})
    scene = demo.call('PUT', '/v1/scenes/emblem', {'name': '归档印记', 'expectedRevision': 0, 'document': d})
    rev = scene['revision']

    # A frozen snapshot captured before archival must remain renderable and unchanged.
    frozen = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': rev}, 201)
    frozen_digest = frozen['digest']

    archived = demo.call('POST', '/v1/scenes/emblem/state',
                         {'expectedRevision': rev, 'state': 'archived'})
    arch_rev = archived['revision']
    demo.call('PUT', '/v1/scenes/emblem',
              {'name': '归档印记', 'expectedRevision': arch_rev, 'document': d}, 409)
    demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': arch_rev}, 409)

    # The asset goes retired while the scene is archived.
    demo.call('POST', '/v1/assets/alpha/retire')

    # 1. Recovery preview: only the alpha layer is blocked; the gamma layer stays freezable.
    plan = demo.call('GET', '/v1/scenes/emblem/recovery-plan')
    assert plan['state'] == 'archived' and plan['revision'] == arch_rev
    assert plan['recoverable'] is False
    assert len(plan['blockedLayers']) == 1
    blocked = plan['blockedLayers'][0]
    assert blocked['layerId'] == 'alpha-img' and blocked['assetId'] == 'alpha'
    assert blocked['reason'] == 'asset retired' and blocked['path'][-1] == 'alpha-img'
    assert set(plan['availableAssets']) >= {'beta', 'gamma', 'delta'}
    assert 'alpha' not in plan['availableAssets']
    # Preview is read-only: nothing moved.
    assert demo.call('GET', '/v1/scenes/emblem')['revision'] == arch_rev

    # 2. Failures keep the archived context and commit nothing.
    no_replace = demo.call('POST', '/v1/scenes/emblem/recovery',
                           {'expectedRevision': arch_rev, 'replacements': {}}, 409)
    assert no_replace['details']['blockedLayers'][0]['layerId'] == 'alpha-img'
    demo.call('POST', '/v1/scenes/emblem/recovery',
              {'expectedRevision': arch_rev, 'replacements': {'alpha': 'alpha'}}, 400)
    demo.call('POST', '/v1/scenes/emblem/recovery',
              {'expectedRevision': arch_rev, 'replacements': {'alpha': 'bad id!'}}, 400)
    demo.call('POST', '/v1/scenes/emblem/recovery',
              {'expectedRevision': arch_rev, 'replacements': {'alpha': 'ghost'}}, 409)
    still = demo.call('GET', '/v1/scenes/emblem')
    assert still['state'] == 'archived' and still['revision'] == arch_rev

    # Replacement target retired between planning and confirmation: refreshed 409, no partial write.
    demo.call('POST', '/v1/assets/delta/retire')
    raced = demo.call('POST', '/v1/scenes/emblem/recovery',
                      {'expectedRevision': arch_rev, 'replacements': {'alpha': 'delta'}}, 409)
    assert 'delta' not in raced['details']['availableAssets']
    assert demo.call('GET', '/v1/scenes/emblem')['state'] == 'archived'

    # 3. Parallel recovery with the same expected revision: transactions serialize,
    #    exactly one confirmation wins and the other's stale attempt cannot overwrite it.
    barrier = threading.Barrier(2)
    results = []

    def attempt():
        barrier.wait(timeout=15)
        try:
            results.append(demo.call('POST', '/v1/scenes/emblem/recovery',
                                     {'expectedRevision': arch_rev,
                                      'replacements': {'alpha': 'beta'}}, 200))
        except RuntimeError as error:
            results.append(error)

    threads = [threading.Thread(target=attempt) for _ in range(2)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(timeout=20)
    successes = [r for r in results if not isinstance(r, RuntimeError)]
    failures = [r for r in results if isinstance(r, RuntimeError)]
    assert len(successes) == 1 and len(failures) == 1, failures
    recovered = successes[0]
    assert recovered['state'] == 'active' and recovered['revision'] == arch_rev + 1
    refs = [layer['assetId'] for layer in recovered['document']['layers'] if layer['kind'] == 'image']
    assert 'alpha' not in refs and 'beta' in refs and 'gamma' in refs

    events = demo.call('GET', '/v1/events')
    recovered_events = [e for e in events if e['kind'] == 'scene.recovered']
    assert len(recovered_events) == 1
    assert [e['sequence'] for e in events] == sorted(e['sequence'] for e in events)

    # Recovery plan is only meaningful while archived.
    demo.call('GET', '/v1/scenes/emblem/recovery-plan', status=409)

    # 4. Another user's revision is protected: stale expectedRevision cannot overwrite.
    current = demo.call('GET', '/v1/scenes/emblem')
    changed = sample()
    changed['background'] = '#102030'
    changed['layers'] = list(current['document']['layers'])
    next_rev = demo.call('PUT', '/v1/scenes/emblem',
                         {'name': '归档印记', 'expectedRevision': current['revision'],
                          'document': changed})['revision']
    demo.call('PUT', '/v1/scenes/emblem',
              {'name': '过期编辑', 'expectedRevision': current['revision'], 'document': changed}, 409)

    # 5. The old snapshot keeps its own bytes and still renders after asset retirement.
    assert demo.call('GET', '/v1/snapshots/' + frozen['id'])['digest'] == frozen_digest
    pinned = demo.wait(demo.submit(frozen, key='frozen-after-retire')['id'])
    assert pinned['state'] == 'succeeded'

    # 6. A fresh freeze of the recovered document succeeds, then retirement blocks it again.
    new_snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots',
                             {'expectedRevision': next_rev}, 201)
    assert new_snapshot['digest'] != frozen_digest
    demo.call('POST', '/v1/assets/beta/retire')
    demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': next_rev}, 409)
    return {'workflow': 'recover-archived', 'recoveredRevision': recovered['revision'],
            'blockedLayers': 1, 'replacement': 'beta',
            'oldSnapshotDigest': frozen_digest, 'newSnapshotDigest': new_snapshot['digest']}


def run(demo, workflow, out):
    if workflow == 'recover-archived':
        return run_recover_archived(demo)
    d = sample()
    put = lambda doc, rev=0: demo.call('PUT', '/v1/scenes/emblem', {'name': '双色印记', 'expectedRevision': rev, 'document': doc})
    scene = put(d)
    if workflow == 'create-scene':
        assert demo.call('GET', '/v1/scenes/emblem')['document'] == scene['document']
        bad = sample(); bad['layers'][0]['transform']['scaleX'] = 0
        demo.call('PUT', '/v1/scenes/invalid', {'name': '无效几何', 'expectedRevision': 0, 'document': bad}, 400)
    elif workflow == 'revise-scene':
        d['background'] = '#F0F0F0'; assert put(d, 1)['revision'] == 2
        demo.call('PUT', '/v1/scenes/emblem', {'name': '旧版本', 'expectedRevision': 1, 'document': d}, 409)
    elif workflow == 'archive-scene':
        archived = demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': 1, 'state': 'archived'})
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': archived['revision']}, 409)
        assert demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': archived['revision'], 'state': 'active'})['revision'] == 3
    if workflow in ('import-asset', 'retire-asset'):
        asset = demo.call('PUT', '/v1/assets/ribbon', tiny_png(), 201)
        assert asset['width'] == 2 and asset['digest'] == hashlib.sha256(tiny_png()).hexdigest()
        demo.call('PUT', '/v1/assets/broken', b'not png', 400)
        layer = {'id': 'ribbon', 'kind': 'image', 'width': 60, 'height': 60, 'assetId': 'ribbon', 'opacity': 1, 'transform': {'x': 20, 'y': 30, 'scaleX': 1, 'scaleY': 1}}
        d['layers'].append(layer); put(d, 1)
        pinned = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 201)
        demo.call('POST', '/v1/assets/ribbon/retire')
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 409)
        result = demo.wait(demo.submit(pinned)['id']); assert result['state'] == 'succeeded'
        return {'workflow': workflow, 'assetDigest': asset['digest'], 'pinnedRender': result['state']}
    current = demo.call('GET', '/v1/scenes/emblem')
    snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision']}, 201)
    if workflow == 'freeze-scene':
        original_digest = snapshot['digest']; d['background'] = '#000000'; put(d, current['revision'])
        assert demo.call('GET', '/v1/snapshots/' + snapshot['id'])['digest'] == original_digest
    if workflow in ('export-package', 'import-package'):
        package = demo.call('GET', '/v1/snapshots/' + snapshot['id'] + '/package')
        restored = demo.call('POST', '/v1/imports/restored', {'name': '恢复的印记', 'package': package}, 201)
        assert restored['document'] == snapshot['document']
        corrupt = json.loads(json.dumps(package)); corrupt['snapshot']['document']['width'] += 1
        demo.call('POST', '/v1/imports/corrupt', {'name': '损坏包', 'package': corrupt}, 400)
        demo.call('GET', '/v1/scenes/corrupt', status=404)
    if workflow in ('cancel-render', 'retry-render', 'recover-render'):
        heavy = sample(); heavy['width'] = 1500; heavy['height'] = 1500
        heavy['layers'] = [dict(heavy['layers'][0], id=f'sheet-{i}', width=1500, height=1500, transform={'scaleX': 1, 'scaleY': 1}) for i in range(16)]
        put(heavy, current['revision'])
        snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
    job = demo.submit(snapshot)
    if workflow == 'submit-render':
        assert demo.submit(snapshot)['id'] == job['id']
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 2}, 409, {'Idempotency-Key': 'render-one'})
    if workflow == 'revoke-snapshot':
        demo.call('POST', '/v1/snapshots/' + snapshot['id'] + '/revoke')
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 1}, 409, {'Idempotency-Key': 'blocked-after-revoke'})
    if workflow in ('cancel-render', 'retry-render'):
        stopped = demo.call('POST', '/v1/jobs/' + job['id'] + '/cancel')
        assert stopped['state'] == 'cancelled'
        if workflow == 'cancel-render':
            time.sleep(.1); assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'cancelled'
            return {'workflow': workflow, 'state': 'cancelled'}
        job = demo.call('POST', '/v1/jobs/' + job['id'] + '/retry', status=202)
        assert job['attempt'] == 2
    if workflow == 'recover-render':
        demo.wait(job['id'], wanted=('running',)); demo.stop(abrupt=True); demo.start()
    result = demo.wait(job['id'])
    png = demo.call('GET', '/v1/artifacts/' + result['artifactId'])
    assert png[:8] == b'\x89PNG\r\n\x1a\n'
    assert struct.unpack('>II', png[16:24]) == (snapshot['document']['width'], snapshot['document']['height'])
    if out:
        Path(out).mkdir(parents=True, exist_ok=True); (Path(out) / 'atlas-sample.png').write_bytes(png)
    if workflow == 'compare-renders':
        other = demo.wait(demo.submit(snapshot, 'render-two')['id'])
        comparison = demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': other['artifactId']})
        assert comparison['changedPixels'] == 0
        d['background'] = '#000000'; put(d, current['revision'])
        altered = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
        changed = demo.wait(demo.submit(altered, 'render-three')['id'])
        assert demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': changed['artifactId']})['changedPixels'] > 0
    if workflow == 'purge-artifact':
        demo.call('DELETE', '/v1/artifacts/' + result['artifactId'])
        demo.call('GET', '/v1/artifacts/' + result['artifactId'], status=410)
        assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'succeeded'
    if workflow == 'observe-service':
        summary = demo.call('GET', '/v1/status'); assert summary['jobs']['succeeded'] >= 1
        events = demo.call('GET', '/v1/events'); assert any(e['kind'] == 'job.succeeded' for e in events)
    return {'workflow': workflow, 'state': result['state'], 'pngBytes': len(png), 'pngDigest': hashlib.sha256(png).hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', default='submit-render')
    parser.add_argument('--out')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='atlas-walkthrough-') as temp:
        directory = Path(temp); binary = directory / 'atlas'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/atlas'], cwd=ROOT, check=True, timeout=60)
        demo = Demo(binary, directory)
        try:
            print(json.dumps(run(demo, args.workflow, args.out), ensure_ascii=False))
        finally:
            demo.stop(); demo.log.close()


if __name__ == '__main__':
    main()
