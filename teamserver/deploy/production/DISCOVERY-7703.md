# 7703 discovery release profile

This profile is separate from the historical `wecom-mcp@sharedzoop.service`
controller. It binds the live `wecom-mcp-discovery-21462d2.service`, the
loopback listener `127.0.0.1:7703`, the fixed `zhycit_zoop` public binding
(`https://zhycit.com/mcp`), the discovery state root, and the database-only
recovery contract. The sharedzoop/7702 controller and its receipt are not
interchangeable with this profile.

## Read-only live baseline

The profile was built from the exact teamserver candidate `72601b3` and a
read-only runtime check:

- current process release: `20261105T001200-21462d2dfb22`
- current process SHA-256: `988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22`
- previous release reference: `20261105T000000-396f2500540e`
- loopback: `127.0.0.1:7703`
- unit fragment SHA-256: `4caa2abe6bed4650faee60084b1657d4e350c3724ead3d4815c7b17a6870d098`
- effective unit fingerprint: `c2c21274a1c4d1f53d08a3cc5a0b321b63c94b5521bf4f982361198972a8b79f`

The candidate manages only the fixed `zhycit_zoop` host. It also records a
read-only surrounding-fleet snapshot: `mcp.wesiyu.com` and `mcp.rtyouth.com`
are ready, `mcp.jianpinke.com` is tenant-local unavailable (local 503; the
public endpoint currently resets TLS), and `mcp.jyiai.com`/gmzoop is unmapped
(local 421, public 404). Those hosts are evidence only and are not restarted,
switched, or included in the promotion target.

## Candidate and rollback contracts

`build-discovery-candidate.py` creates a schema-v2 immutable candidate with
the binary, discovery policy, service/recovery drop-ins, manifest, and
`SHA256SUMS`. The manifest binds current/previous release IDs and binary
digests, GNAS pair, resource map, complete fleet snapshot, unit fragment and
runtime fingerprints, readiness file, and the separately supplied archive,
manifest, catalogue, and legacy-smoke evidence digests.

The fallback artifact is the read-only copy of the actual current release
binary and policy. It is an evidence package only; its service is never
started by this worktree. A deploy receipt must bind both the immutable
candidate and this fallback evidence, then re-read the live unit, process,
fleet, and public route state before consuming a one-time receipt (maximum
four hours). No receipt is created by this repository change.

## Check-only / future controlled actions

```sh
python3 -B teamserver/deploy/production/build-discovery-candidate.py \
  --output /absolute/new/candidate \
  --expected-runtime-path /home/product/services/mcp/wecom/releases/<current>/wecom-mcp-team \
  --expected-binary-sha256 <current-process-sha256> \
  --expected-unit-fingerprint <effective-unit-fingerprint> \
  --expected-runtime-config-fingerprint <database-discovery-fingerprint> \
  --expected-current-unit-fragment-sha256 <unit-file-sha256> \
  --expected-gnas-release-id <paired-gnas-release> \
  --expected-gnas-binary-sha256 <paired-gnas-sha256> \
  --expected-current-archive-sha256 <fallback-archive-sha256> \
  --expected-current-manifest-sha256 <fallback-manifest-sha256> \
  --expected-current-catalogue-sha256 <fallback-catalogue-sha256> \
  --legacy-smoke-sha256 <independent-legacy-smoke-evidence-sha256> \
  --ci-url <same-commit-hosted-ci-url>
```

Omitting `--ci-url` keeps the artifact local-only. Installation of this
adapter, staging, service restart, Nginx changes, GNAS changes, and receipt
creation remain outside this change and are not performed here.
