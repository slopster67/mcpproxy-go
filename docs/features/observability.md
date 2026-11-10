---
title: "Usage storage"
sidebar_label: "Usage storage"
description: "Control the local Usage response cache and summary persistence."
---

# Usage storage

Activity and Usage use local records in the encrypted primary database.
The `observability` settings control only the cache and persistence cadence:

| Setting | Default | Purpose |
| --- | --- | --- |
| `observability.usage_cache_ttl` | `5s` | Cache Usage API responses to reduce repeated calculations. |
| `observability.usage_persist_interval` | `30s` | Periodically persist accumulated usage summaries. Summaries also flush on orderly shutdown. |

Both settings apply live. No external collector is needed.
