---
title: Tool-call intent
sidebar_label: Tool-call intent
---

# Tool-call intent

In Retrieve mode, clients call discovered tools through `call_tool_read`,
`call_tool_write` or `call_tool_destructive`, using the exact server/tool name and
JSON arguments from discovery. Choose the variant matching the operation.
Calls can include a reason and data-sensitivity information for Activity records.

A tool explicitly annotated destructive cannot be called through the read or
write variant. That validation is always enforced; use the destructive variant
for a destructive tool. Calling a read-only tool through a more permissive
variant is allowed. These checks do not replace profile access, client
permissions, tool approval or tool enable/disable decisions.
