# Encrypted storage

The primary stores canonical servers, profiles, clients, peers, named secrets,
OAuth access/refresh tokens, OAuth client registrations and other database records in `<data-dir>/primary.db/`.
This is a transactional Badger database with encryption enabled for keys and
values in its tables, write-ahead logs and value logs. It uses a deployment-supplied
32-byte AES master key to encrypt its internal data keys. Client-token HMAC keys
are derived from that master key with a separate HKDF context; no `.token_key`
file is created.

There are no macOS Keychain, Linux Secret Service, desktop-session, D-Bus or other
OS credential-store integrations. Linux and macOS use the same storage path.

## Key configuration

Set exactly one:

| Variable | Value |
| --- | --- |
| `MCPPROXY_ENCRYPTION_KEY` | A base64-encoded random 32-byte key |
| `MCPPROXY_ENCRYPTION_KEY_FILE` | Path to a file containing that base64-encoded key, optionally followed by a newline |

Generate a key outside the application using `openssl rand -base64 32` and keep
it in your deployment's secret manager. Key files must be regular files without
world-read permission; `0400`, `0600` and restricted group-read modes such as
`0440` are supported. File paths may be symlinks, as with systemd credentials.
The key is read once on startup. Missing, ambiguous, malformed or incorrect keys
prevent the primary from starting. No key is generated or saved automatically.

Back up the key separately from the database. Stop the daemon before copying the
whole `primary.db` directory. Restoring requires the same key. Changing the value
of the environment variable is not key rotation: an existing database will reject
it. MCPProxy has no automatic storage migration or key-conversion path.

## NixOS with agenix

The agenix secret contains the base64 text described above. The service reads the
decrypted runtime file; no key material belongs in the Nix store:

```nix
age.secrets.mcpproxy-db-key = {
  file = ./secrets/mcpproxy-db-key.age;
  owner = "mcpproxy";
  mode = "0400";
};

systemd.services.mcpproxy.serviceConfig = {
  User = "mcpproxy";
  StateDirectory = "mcpproxy";
  Environment = [
    "MCPPROXY_DATA_DIR=/var/lib/mcpproxy"
    "MCPPROXY_ENCRYPTION_KEY_FILE=${config.age.secrets.mcpproxy-db-key.path}"
  ];
  ExecStart = "${pkgs.mcpproxy}/bin/mcpproxy serve";
};
```

This assumes your deployment supplies the `mcpproxy` package, system user and
environment settings. No desktop environment, login session or keyring daemon is
needed.

## Secret references

Create a secret on **Secrets**, then use `${secret:name}` in server settings:

```json
{
  "headers": { "Authorization": "Bearer ${secret:api-token}" },
  "env": { "HOME": "${env:HOME}" }
}
```

Compound values and multiple references are supported. `${env:NAME}` reads the
environment of the host executing that server. `${secret:name}` always reads a
named secret from the primary. Unsupported reference types are rejected; there
are no old-reference aliases.

Server environment/header values are part of the encrypted canonical server
record. Use secret references when a value should be shared or managed separately.
The global JSON settings file,
search index and operational logs are separate from the encrypted database.
A literal value entered in configuration remains literal there; database
encryption does not encrypt arbitrary files or protect secrets from a running
process that has already unlocked the store.

## Peers

Peers do not open any credential database or load the primary's encryption key.
Supply their bootstrap credential through `MCPPROXY_PEER_TOKEN` or `MCPPROXY_PEER_TOKEN_FILE`,
or leave both unset and sign the peer in through the browser. A browser sign-in
stores only a refresh token in `<data-dir>/peer-credential.json` with owner-only
permissions. For agenix, point the file option at the decrypted runtime secret. Set only one
token source. File permissions follow the same no-world-read rule as key files.

The primary sends the secret values and OAuth state needed by each peer's
assigned servers over the authenticated control connection. Use HTTPS between
hosts. Peers resolve environment references locally, perform their servers'
OAuth exchanges and refreshes, and publish updated grants back to the encrypted
primary store. Those values, tokens, and unacknowledged updates exist only in
peer process memory. Peers may write operational logs, but do not persist
credential snapshots or their supplied bootstrap token. On restart they need the
primary to supply their state again.
