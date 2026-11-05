#!/usr/bin/python3 -I
"""Release adapter for the live GNAS discovery service on loopback 7703.

This module deliberately keeps the historical sharedzoop/7702 controller
unchanged. It loads the same safety implementation with a separate, fixed
production profile for ``wecom-mcp-discovery-21462d2.service``. The profile
is immutable: it binds the current unit, release topology, public resource,
and readiness contract before any future receipt can be consumed.
"""
import importlib.util
from pathlib import Path

HERE = Path(__file__).resolve().parent
BASE_CONTROLLER = HERE / 'mcp_release_controller.py'
spec = importlib.util.spec_from_file_location('_mcp_release_controller', BASE_CONTROLLER)
if spec is None or spec.loader is None:
    raise RuntimeError('release controller is unavailable')
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)

# Fixed live topology. These values are not derived from a candidate or a
# request, and changing them requires a new reviewed controller/profile.
PROFILE = 'gnas-discovery-21462d2-v1'
ENVIRONMENT = 'zhycit-prod-01/wecom-mcp-discovery-21462d2'
UNIT = 'wecom-mcp-discovery-21462d2.service'
BASE = c.BASE
RELEASES = BASE / 'releases'
INCOMING = c.INCOMING
CONTROL = c.CONTROL
APPROVALS = c.APPROVALS
DROPIN = Path('/etc/systemd/system/wecom-mcp-discovery-21462d2.service.d/zz-managed-release.conf')
STATE = BASE / 'state/discovery'
LOOPBACK = '127.0.0.1:7703'
READINESS_FILE = Path('/etc/wecom-mcp/gnas-discovery-readiness.json')
CURRENT_RELEASE_ID = '20261105T001200-21462d2dfb22'
CURRENT_BINARY_SHA256 = '988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22'
PREVIOUS_RELEASE_ID = '20261105T000000-396f2500540e'
PREVIOUS_BINARY_SHA256 = 'e37bd4ec2103dd33bd2d923c2d827748c8f6b95a63db26c4a1835f4a4bac7d69'
TARGET_HOST = 'zhycit.com'
HOSTS = (TARGET_HOST,)
RESOURCES = {TARGET_HOST: 'https://zhycit.com/mcp'}
# Read-only observation of the surrounding discovery fleet. These hosts are
# evidence only; this profile never manages, restarts, or promotes them.
OBSERVED_FLEET_SNAPSHOT = (
    {'host': 'zhycit.com', 'state': 'ready', 'resource': 'https://zhycit.com/mcp', 'public_route': 'mcp'},
    {'host': 'mcp.wesiyu.com', 'state': 'ready', 'resource': 'https://mcp.wesiyu.com/mcp', 'public_route': 'mcp'},
    {'host': 'mcp.rtyouth.com', 'state': 'ready', 'resource': 'https://mcp.rtyouth.com/mcp', 'public_route': 'mcp'},
    {'host': 'mcp.jianpinke.com', 'state': 'tenant_unavailable', 'resource': 'https://mcp.jianpinke.com/mcp', 'public_route': 'tls_reset'},
    {'host': 'mcp.jyiai.com', 'state': 'resource_unmapped', 'resource': 'https://mcp.jyiai.com/gmzoop/mcp', 'public_route': '404', 'local_status': 421},
)
FLEET_STATE_VECTOR = ({'host': TARGET_HOST, 'state': 'ready'},)
FLEET_STATE_VECTOR_SHA256 = c.fleet_state_vector_sha256(list(FLEET_STATE_VECTOR))
FILES = c.FILES
RID = c.RID
SHA = c.SHA
APR = c.APR


def require(ok, message):
    return c.require(ok, message)


def _dropin(release_id):
    require(RID.fullmatch(release_id), 'invalid release ID')
    base = RELEASES / release_id
    args = (
        '%s --gnas-discovery-policy %s --gnas-state-root %s --listen %s '
        '--gnas-fleet-refresh 30s --gnas-ready-worker-isolation '
        '--gnas-ready-worker-timeout 5s'
    ) % (base / 'wecom-mcp-team', base / 'discovery-policy.json', STATE, LOOPBACK)
    return (
        '[Service]\nExecStartPre=\nExecStart=\n'
        'ExecStartPre=' + args + ' --check-config\n'
        'ExecStart=' + args + '\n'
        'ReadWritePaths=' + str(STATE) + '\n'
    ).encode()


def _ready(host, legacy=False, loopback=LOOPBACK, public_resource_metadata=False):
    # The service remains root-routed locally. Public routes are per-host;
    # the resource-specific RFC 9728 path is fixed to /mcp for this fleet.
    for endpoint, code in (
        ('/healthz', 200),
        ('/readyz', 200),
        ('/mcp', 401),
        ('/.well-known/oauth-protected-resource', 200),
    ):
        c.probe(host, endpoint, code, loopback=loopback)
    if public_resource_metadata:
        public_endpoints = (
            ('/healthz', 200), ('/readyz', 200), ('/mcp', 401),
            ('/.well-known/oauth-protected-resource', 200),
            ('/.well-known/oauth-protected-resource/mcp', 200),
        )
    else:
        public_endpoints = (('/healthz', 200), ('/readyz', 200), ('/mcp', 401))
    for endpoint, code in public_endpoints:
        c.probe(host, endpoint, code, True)


def _unready(host, loopback=LOOPBACK):
    # Keep the tenant-local state check on the local service. The current
    # public route for this unavailable tenant is intentionally unverified;
    # no deploy may turn that absence into a success claim.
    for public in (False,):
        endpoints = (
            '/healthz', '/readyz', '/mcp',
            '/.well-known/oauth-protected-resource',
        )
        for endpoint in endpoints:
            c.probe(host, endpoint, 503, public=public, loopback=loopback)


def _unmapped(host, loopback=LOOPBACK):
    # The desired gmzoop hostname is not registered in the current routing
    # snapshot: 7703 returns 421 and its public vhost returns 404.
    for endpoint in ('/healthz', '/readyz', '/mcp', '/.well-known/oauth-protected-resource'):
        c.probe(host, endpoint, 421, loopback=loopback)
    for endpoint in ('/healthz', '/readyz', '/mcp', '/.well-known/oauth-protected-resource'):
        c.probe(host, endpoint, 404, public=True, loopback=loopback)


def _healthy(path, digest, hosts, legacy=False):
    c.require_runtime(path, digest)
    for host in hosts:
        if host in UNREADY_HOSTS:
            _unready(host, loopback=LOOPBACK)
            continue
        if host in UNMAPPED_HOSTS:
            _unmapped(host, loopback=LOOPBACK)
            continue
        _ready(host, legacy=legacy, loopback=LOOPBACK, public_resource_metadata=True)


def _preflight_recovery(release_id):
    m = verify(RELEASES / release_id, release_id)
    if m.get('_ready_worker_isolation'):
        raise ValueError('blue-green isolation executor is not installed; discovery recovery prohibited')
    c.recovery_matches(m)
    c.run(
        'systemd-run', '--quiet', '--wait', '--collect',
        '--unit=wecom-mcp-discovery-recovery-check-' + release_id,
        '--property=Type=oneshot', '--property=RuntimeMaxSec=35',
        '--property=TimeoutStartSec=35', '--property=Restart=no',
        '--property=User=wecom-mcp-gmzoop', '--property=Group=wecom-mcp-gmzoop',
        '--property=EnvironmentFile=/etc/wecom-mcp/gmzoop.env',
        '--property=ProtectSystem=strict', '--property=ProtectHome=read-only',
        '--property=NoNewPrivileges=yes', '--property=PrivateTmp=yes',
        '--property=ReadWritePaths=' + str(BASE / 'instances/gmzoop/data'),
        '--property=StandardOutput=null', '--property=StandardError=null',
        str(RELEASES / release_id / 'wecom-mcp-team'),
        '--gnas-discovery-policy', str(RELEASES / release_id / 'discovery-policy.json'),
        '--gnas-state-root', str(STATE), '--listen', LOOPBACK, '--check-config',
    )
    c.recovery_matches(m)
    return {'state': 'recovery_preflight_passed', 'release_id': release_id, 'service_restarted': False}


def _deploy_fields(manifest, directory):
    expected = _base_deploy_fields(manifest, directory)
    expected.update({
        'profile': PROFILE,
        'service_unit': UNIT,
        'loopback': LOOPBACK,
        'resources': dict(RESOURCES),
        'public_hosts': list(HOSTS),
        'observed_fleet_snapshot': [dict(row) for row in OBSERVED_FLEET_SNAPSHOT],
        'readiness_file': str(READINESS_FILE),
        'expected_current_release_id': manifest['expected_current_release_id'],
        'expected_current_binary_sha256': manifest['expected_current_binary_sha256'],
        'expected_previous_release_id': manifest['expected_previous_release_id'],
        'expected_previous_binary_sha256': manifest['expected_previous_binary_sha256'],
        'expected_current_unit_fragment_sha256': manifest['expected_current_unit_fragment_sha256'],
    })
    return expected


def _verify(directory, release_id):
    manifest = _base_verify(directory, release_id)
    require(manifest.get('profile') == PROFILE, 'discovery profile mismatch')
    require(manifest.get('service_unit') == UNIT and manifest.get('loopback') == LOOPBACK, 'discovery service binding mismatch')
    require(manifest.get('resources') == RESOURCES and manifest.get('public_hosts') == list(HOSTS), 'public resource binding mismatch')
    require(manifest.get('observed_fleet_snapshot') == [dict(row) for row in OBSERVED_FLEET_SNAPSHOT], 'observed fleet snapshot mismatch')
    require(manifest.get('readiness_file') == str(READINESS_FILE), 'readiness file binding mismatch')
    for key in ('expected_current_release_id', 'expected_previous_release_id'):
        require(isinstance(manifest.get(key), str) and manifest[key], 'release lineage missing')
    for key in ('expected_current_binary_sha256', 'expected_previous_binary_sha256', 'expected_current_unit_fragment_sha256'):
        require(SHA.fullmatch(manifest.get(key, '')), key + ' missing')
    require(manifest.get('expected_current_release_id') == CURRENT_RELEASE_ID, 'current release baseline mismatch')
    require(manifest.get('expected_current_binary_sha256') == CURRENT_BINARY_SHA256, 'current binary baseline mismatch')
    require(manifest.get('expected_previous_release_id') == PREVIOUS_RELEASE_ID, 'previous release baseline mismatch')
    require(manifest.get('expected_previous_binary_sha256') == PREVIOUS_BINARY_SHA256, 'previous binary baseline mismatch')
    require(manifest.get('fleet_state_vector') == [dict(row) for row in FLEET_STATE_VECTOR], 'discovery fleet vector mismatch')
    require(manifest.get('fleet_state_vector_sha256') == FLEET_STATE_VECTOR_SHA256, 'discovery fleet vector digest mismatch')
    return manifest


# Keep the unpatched implementations used by the profile wrappers below.
_base_verify = c.verify
_base_deploy_fields = c.deploy_fields

# Patch only the imported implementation's profile globals. The historical
# controller file and its default 7702 behavior are not modified by this
# adapter. Every aliased function below resolves globals from ``c``.
c.ENVIRONMENT = ENVIRONMENT
c.UNIT = UNIT
c.RELEASES = RELEASES
c.INCOMING = INCOMING
c.CONTROL = CONTROL
c.APPROVALS = APPROVALS
c.DROPIN = DROPIN
c.STATE = STATE
c.HOSTS = HOSTS
c.FLEET_STATE_VECTOR = FLEET_STATE_VECTOR
c.BLUE_GREEN_PLAN = dict(c.BLUE_GREEN_PLAN, green=dict(c.BLUE_GREEN_PLAN['green'], unit=UNIT, loopback=LOOPBACK, state_root=str(STATE)))
c.dropin = _dropin
c.recovery_dropin = _dropin
c.ready = _ready
c.unready = _unready
c.healthy = _healthy
c.preflight_recovery = _preflight_recovery
c.deploy_fields = _deploy_fields
c.verify = _verify

verify = _verify
sha = c.sha
read_json = c.read_json
regular = c.regular
dropin = _dropin
recovery_dropin = _dropin
deploy_fields = _deploy_fields
fleet_state_vector = lambda: [dict(row) for row in FLEET_STATE_VECTOR]
fleet_state_vector_sha256 = lambda vector=None: c.fleet_state_vector_sha256(fleet_state_vector() if vector is None else vector)
observed_fleet_snapshot = lambda: [dict(row) for row in OBSERVED_FLEET_SNAPSHOT]


if __name__ == '__main__':
    c.main()
