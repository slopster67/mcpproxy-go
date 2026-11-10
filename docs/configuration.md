# Configuration

Deployment settings use environment variables only, with built-in defaults.
Secret fields have `_FILE` inputs for runtime secret files such as agenix.
There are no deployment flags or JSON configuration files. Settings are read
once at startup; restart to apply changes.

Servers, profiles, clients, peers, secrets, OAuth credentials, approvals,
activity and usage live in encrypted `<data-dir>/primary.db/`. The runtime loads
an immutable entity snapshot for fast requests. Entity changes commit to the
database before the new snapshot is published.

See [Environment configuration](configuration/environment-variables.md),
[Settings inventory](../SETTINGS.md), [Upstream servers](configuration/upstream-servers.md),
[Profiles](features/profiles.md), [Encrypted storage](features/encrypted-storage.md)
and [Dashboard OIDC](features/dashboard-oidc.md).
