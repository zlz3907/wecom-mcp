# MCP policy rollback floor: unfinished prototype

This is not a production installation or authorization to enable policy.

The prototype checks a root-owned fail-closed floor, exact reviewed route and
nginx topology hashes, and the target unit, process executable, execution
configuration, network namespace and listening socket. It has not been wired
into the existing release, manual rollback, automatic rollback, crash recovery
or ingress controllers. The current production rollback can still route to an
old MCP that ignores managed policy.

Before policy activation is allowed, finish and independently test all of:

1. Bind approved floor creation and future changes to the actual installed
   controller files and a verified compatible rollback target; install/upgrade
   or safely retire every existing route controller, including recovery paths.
2. Bind nginx's effective full configuration and its reload lifecycle to the
   same cross-component lock as staging, checking both before and after reload;
   a concurrent unrelated nginx reload must not expose an unverified route.
3. Bind the trusted live OAuth server, tenant/instance/authorization resource
   and current dynamic discovery generation, not just process arguments and
   environment. A binary/configuration digest alone is not a policy contract.
4. Serialize activation and route/floor changes using one OS-visible lock or
   an equivalent transactional protocol. Missing or invalid floor in a managed
   deployment must never permit activation, deployment, recovery or rollback.
5. Cover shared enterprise discovery as well as the isolated Zhonghe route;
   prove compatibility for each tenant and keep a verified compatible fallback.
6. Test actual Linux processes and sockets, including restart, re-exec, fd
   sharing, network namespaces, floor loss and concurrent policy activation.

Deploying or configuring these controls requires the separately recorded
production infrastructure process and fresh exact approval receipts.
