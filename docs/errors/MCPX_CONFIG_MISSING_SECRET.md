---
id: MCPX_CONFIG_MISSING_SECRET
title: MCPX_CONFIG_MISSING_SECRET
sidebar_label: MISSING_SECRET
description: An upstream config refers to a secret that does not exist in the secret store.
---

# `MCPX_CONFIG_MISSING_SECRET`

**Severity:** error
**Domain:** Config

## What happened

The upstream config references a secret (typically via `${secret:NAME}` in
`env` or `headers`), but the secret was not found in the active secret store
when mcpproxy tried to start that server.

## How to fix

### Add the missing secret

Add the secret under the name you referenced from the web UI's Secrets page.

### Check the reference syntax

```jsonc
{
  // env values support ${secret:NAME} expansion:
  "env": { "GITHUB_TOKEN": "${secret:github_personal_token}" },

  // headers do too:
  "headers": { "Authorization": "Bearer ${secret:my_api_key}" }
}
```

Plain string values are used as-is (no expansion).

## Related

- [Configuration → environment variables](../configuration/environment-variables.md)
