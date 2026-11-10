---
id: code-execution
title: Code Execution
sidebar_label: Code Execution
sidebar_position: 3
description: Execute JavaScript or TypeScript to orchestrate multiple MCP tools
keywords: [code, execution, javascript, typescript, orchestration]
---

# Code Execution

The `code_execution` tool enables orchestrating multiple upstream MCP tools in a single request using sandboxed JavaScript (ES2020+) or TypeScript.

## Overview

Code execution allows AI agents to:

- Chain multiple tool calls together
- Process and transform tool outputs
- Implement complex logic and conditionals
- Reduce round-trip latency
- Run [stored scripts](#stored-scripts) by name instead of re-sending the source every call

## Configuration

Enable code execution in your config:

```json
{
  "code_execution_timeout_ms": 120000,
  "code_execution_max_tool_calls": 0,
  "code_execution_pool_size": 10,
  "code_execution_max_parallel": 8
}
```

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `code_execution_timeout_ms` | integer | `120000` | Execution timeout (2 minutes) |
| `code_execution_max_tool_calls` | integer | `0` | Max tool calls (0 = unlimited) |
| `code_execution_pool_size` | integer | `10` | Number of VM instances to pool |
| `code_execution_max_parallel` | integer | `8` | Default concurrency for `call_tools()` batches (1-32) |

## API

### Input Schema

```json
{
  "code": "string - inline JavaScript or TypeScript code to execute",
  "script": "string - name of a stored script to execute instead of 'code'",
  "language": "string (optional) - 'javascript' (default) or 'typescript'",
  "input": "object (optional) - Input data available as 'input' variable"
}
```

Provide **exactly one** of `code` or `script` — both or neither is an error.
Neither is schema-required, because JSON Schema cannot express the rule; the
tool enforces it.

### Built-in Functions

#### call_tool(server, tool, args)

Execute a tool on an upstream server:

```javascript
var result = call_tool('github', 'create_issue', {
  repo: 'owner/repo',
  title: 'Bug report',
  body: 'Description'
});
```

#### call_tools(requests, options)

Execute **independent** tools in parallel and get one result slot per request, in
input order:

```javascript
var slots = call_tools([
  {server: 'github', tool: 'get_pull_request', args: {owner: 'acme', repo: 'api', pullNumber: 1}},
  {server: 'github', tool: 'get_pull_request', args: {owner: 'acme', repo: 'api', pullNumber: 2}},
  {server: 'ci', tool: 'latest_pipeline', args: {project: 'acme/api'}}
], {max_parallel: 5});

slots.map(function (s) { return s.ok ? s.result : 'ERR: ' + s.error.code; });
```

#### log(message)

Log a message (visible in tool response):

```javascript
log('Processing started');
```

### Return Value

The last expression in the code is returned as the tool result:

```javascript
// Return an object
({
  success: true,
  data: result
})
```

## Stored Scripts

A stored script is a `<name>.js` / `<name>.ts` file the operator drops into the
`scripts/` directory inside the data directory (`MCPPROXY_DATA_DIR`). Agents then
run it by name with `script: "<name>"` instead of re-sending the whole source on
every call — a 19 KB workflow costs a name plus its `input` per run.

```json
{"script": "fetch-prs", "input": {"owner": "acme", "repo": "api"}}
```

### Authoring rules

| Rule | Value |
|------|-------|
| Location | `scripts/` inside the data directory |
| Name | 1–64 characters of `A-Za-z0-9_-`, case-sensitive — a bare name, never a path |
| Extension | lowercase `.js` or `.ts` only; the extension decides the language |
| Size | 1 byte – 256 KB (empty and oversized files are rejected) |
| File type | regular files only — a symlink at the script path is rejected |

`.ts` scripts take exactly the same transpilation path as inline
`language: "typescript"`. Passing a `language` that contradicts the extension is
an error; omitting it is the normal case.

Both `name.js` and `name.ts` present is **ambiguous** — the invocation fails
rather than picking one.

### Freshness

Every invocation opens and reads the file once, with no cache and no watcher.
Edit a script by **atomic replace** (write a temp file, then rename over it) and
the next invocation runs the new content — no daemon restart. Added and removed
files are likewise picked up on next use. An in-place write racing an invocation
yields unspecified (but validated) content, which is why atomic replace is the
supported edit.

### Discovery and errors

There is no listing: an MCP client must already know the script name, so put the
names in its custom instructions. Every refusal names only the caller's own
script and the reason, never a host path or what else is stored:

| Situation | Error |
|-----------|-------|
| Unknown name | `stored script "X" not found (…)` |
| Both extensions present | `stored script "X" is ambiguous: both a .js and a .ts file exist — …` |
| Empty / oversized / unreadable / non-regular | `stored script "X" is oversized: scripts are limited to 262144 bytes` |
| `language` contradicts the extension | `stored script "X" does not accept requested language "…"` |

### No write path

Nothing creates, edits, or deletes scripts through any API. The filesystem is
the only authoring interface, so an agent can run stored workflows but never
author them.

## Examples

### Simple Calculation

```javascript
// input: { "a": 5, "b": 3 }
({ sum: input.a + input.b })
```

### Tool Chaining

```javascript
// Get user info then create an issue
var user = call_tool('github', 'get_user', { username: input.username });
var issue = call_tool('github', 'create_issue', {
  repo: input.repo,
  title: 'Issue from ' + user.name,
  body: 'Created by code execution'
});
({ user: user, issue: issue })
```

### Conditional Logic

```javascript
var files = call_tool('filesystem', 'list_directory', { path: input.path });

if (files.length > 100) {
  ({ status: 'too_many_files', count: files.length });
} else {
  var results = [];
  for (var i = 0; i < files.length; i++) {
    if (files[i].endsWith('.md')) {
      results.push(files[i]);
    }
  }
  ({ markdown_files: results });
}
```

### Error Handling

```javascript
try {
  var result = call_tool('api', 'fetch_data', { url: input.url });
  ({ success: true, data: result });
} catch (e) {
  ({ success: false, error: e.message });
}
```

## Security

### Sandbox Environment

- Code runs in isolated JavaScript VM (goja)
- No access to file system
- No access to network (except via call_tool)
- No access to process environment
- Memory limits enforced

### Tool Call Security

- Tools inherit quarantine status
- Rate limiting applied
- Response size limits enforced

## Troubleshooting

### Timeout Errors

Increase the timeout or optimize your code:

```json
{
  "code_execution_timeout_ms": 300000
}
```

### Syntax Errors

Modern JavaScript syntax (ES2020+) is fully supported, including arrow functions, const/let, template literals, destructuring, optional chaining, and nullish coalescing:

```javascript
// All of these work
const result = () => call_tool('server', 'tool');
const name = user?.profile?.name ?? 'unknown';
const msg = `Hello, ${name}!`;
const { data, error } = response;
```

### Tool Not Found

Verify the server and tool names:


### Stored Script Not Found

Check that `<MCPPROXY_DATA_DIR>/scripts/<name>.js` (or `.ts`) exists, that the name matches exactly (case-sensitive), and that the file is a regular file.

## TypeScript Support

Set `language: "typescript"` to write code with type annotations. TypeScript types are automatically stripped before execution using esbuild, with near-zero transpilation overhead (<5ms).

### Supported Features

- Type annotations: `const x: number = 42`
- Interfaces: `interface User { name: string; age: number; }`
- Type aliases: `type StringOrNumber = string | number`
- Generics: `function identity<T>(arg: T): T { return arg; }`
- Enums: `enum Direction { Up = "UP", Down = "DOWN" }`
- Namespaces and type assertions

### Example


### Important Notes

- TypeScript support uses type-stripping only (no type checking or semantic validation)
- Valid JavaScript is also valid TypeScript
- Transpilation errors return the `TRANSPILE_ERROR` error code with line/column information
- See `docs/code_execution/overview.md` in the repository for comprehensive TypeScript documentation

## Best Practices

1. **Keep code simple**: Complex logic is harder to debug
2. **Handle errors**: Use try/catch for tool calls
3. **Minimize tool calls**: Batch operations when possible; run independent calls with `call_tools()` instead of a sequential loop
4. **Use logging**: Add log() calls for debugging
