---
id: MCPX_STDIO_SPAWN_EACCES
title: MCPX_STDIO_SPAWN_EACCES
sidebar_label: SPAWN_EACCES
description: The OS denied permission to execute the stdio MCP server command.
---

# `MCPX_STDIO_SPAWN_EACCES`

**Severity:** error
**Domain:** STDIO

## What happened

The configured command exists on `PATH`, but the OS refused to execute it
(`EACCES`). The subprocess never started.

## Common causes

- The file is missing the executable bit (`chmod +x`).
- The file is on a noexec-mounted volume (e.g. some `/tmp` or NFS mounts).
- AppArmor / SELinux denied the exec syscall.

## How to fix

### Restore the executable bit

```bash
ls -l "$(which <command>)"        # check current mode
chmod +x "$(which <command>)"     # add execute permission
```


### Move off a noexec volume

```bash
mount | grep noexec
# Reinstall the tool to ~/.local/bin or /usr/local/bin instead of /tmp
```

### AppArmor / SELinux

Check the audit log for `DENIED exec`:

```bash
sudo dmesg | grep -i denied
sudo journalctl -t audit | grep DENIED
```

Adjust the policy to sidestep host policy.

## Related

- [`MCPX_STDIO_SPAWN_ENOENT`](MCPX_STDIO_SPAWN_ENOENT.md) — file does not exist
