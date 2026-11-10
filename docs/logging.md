---
title: "Logging"
sidebar_label: "Logging"
description: "JSON logs, rotation, and live per-server logs."
---

# Logging

MCPProxy writes newline-delimited JSON, with one record per line, to both file
and console outputs. There is no format switch. Each record has `ts` (the time
it was written, including timezone and fractional seconds), `level`, and `msg`,
plus structured fields such as `server`, `caller`, and `error`. Upstream process
output is captured inside these records; it does not select the file format.

```json
{"ts":"2026-11-08T20:46:48.116637+01:00","level":"error","msg":"Connection failed","server":"example","error":"Connection refused"}
```

## View server logs

Open a server's **Logs** tab. Entries are newest first, with recorded time,
colored level, message, and error text shown directly. Expand a row for remaining
structured fields by clicking the row. Selecting or copying text does not
toggle it. Search operates locally. The green Live
indicator accompanies automatic updates every 1.5 seconds; there is no pause
switch or refresh button. Updates stop while the browser tab is hidden or after
leaving Logs. At the top, new entries appear immediately. When you scroll down,
the visible rows stay steady while newer entries are buffered. **Jump to latest**
returns to the top and applies the newest tail. Disabled servers retain logs.

`GET /api/v1/servers/{name}/logs?tail=200` returns
`{server_name, logs: [{timestamp, level, message, server, fields?}], count}` inside
the standard API envelope. Tail is capped at 500 entries. Primary and peer hosts
use the same tail reader. Missing files return an empty list; malformed records,
partial writes, and old non-JSON formats are skipped. No timestamps are invented,
and no existing log files are converted. Credential values are scrubbed before
records are returned.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `MCPPROXY_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn` or `error`; trace uses debug-level logging |
| `MCPPROXY_LOG_TO_CONSOLE` | `true` | Write records to stderr |
| `MCPPROXY_LOG_TO_FILE` | `false` | Write the main log file |
| `MCPPROXY_LOG_DIR` | `<data-dir>/logs` | Log directory |
| `MCPPROXY_LOG_FILENAME` | `main.log` | Main log file name |
| `MCPPROXY_LOG_MAX_SIZE` | `10` | Rotation size in MB |
| `MCPPROXY_LOG_MAX_BACKUPS` | `5` | Rotated files kept |
| `MCPPROXY_LOG_MAX_AGE` | `30` | Days rotated files are kept |
| `MCPPROXY_LOG_COMPRESS` | `true` | Compress rotated files |

Per-server files are named `server-<name>.log`; unsafe filename characters are
replaced with underscores. Primary and peer hosts both write them under their
log directory.

```sh
MCPPROXY_LOG_LEVEL=debug MCPPROXY_LOG_TO_FILE=true mcpproxy serve
```
