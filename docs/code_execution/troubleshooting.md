---
title: "Code Execution Troubleshooting"
sidebar_label: "Troubleshooting"
description: "Diagnose sandbox errors, timeouts, and unexpected results in code execution."
---

# JavaScript Code Execution - Troubleshooting

Common issues, error messages, and solutions for the `code_execution` tool.

## Table of Contents

1. [Configuration Issues](#configuration-issues)
2. [Syntax Errors](#syntax-errors)
3. [Runtime Errors](#runtime-errors)
4. [Timeout Issues](#timeout-issues)
5. [Tool Call Errors](#tool-call-errors)
6. [Stored Script Errors](#stored-script-errors)
7. [Serialization Errors](#serialization-errors)
8. [Performance Issues](#performance-issues)
9. [Debugging Tips](#debugging-tips)

---

## Configuration Issues

### Code execution permission

Enable **Allow code execution** for the client on **Clients**. There is no
profile-level or global enable switch. Deployment limits come from
`MCPPROXY_CODE_EXECUTION_*` environment settings; restart after changing them.

### Error: "code_execution tool not found in tools list"

**Symptom**: LLM agent lists available tools but `code_execution` is missing.

**Cause**: Feature is not enabled — a disabled `code_execution` is deliberately absent from `tools/list` — or the client is holding a tool list from before the flag was enabled.

**Solution**:
Code execution requires **Allow code execution** on the client in **Clients**. There is no global or profile-level enable switch.
2. The change is hot-reloaded and announced with `notifications/tools/list_changed`; if the client does not honor that notification, reconnect it
3. List tools again

**Verification**:

---

### Error: "timeout must be between 1 and 600000 milliseconds"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "message": "timeout_ms must be between 1 and 600000 milliseconds"
  }
}
```

**Cause**: Invalid `timeout_ms` value in request options.

**Solution**: Use a timeout between 1ms and 600000ms (10 minutes):
```json
{
  "code": "...",
  "options": {
    "timeout_ms": 120000  // 2 minutes (valid)
  }
}
```

---

### Error: "code_execution_max_parallel: must be between 1 and 32"

**Symptom**: mcpproxy refuses to load the configuration file:
```
config validation failed: code_execution_max_parallel: must be between 1 and 32 (or 0 for default)
```

**Cause**: `code_execution_max_parallel` is outside the supported range.

**Solution**: Use a value between 1 and 32 (or omit the key / set `0` for the
default of 8):
```json
{
  "code_execution_max_parallel": 8
}
```

The same range applies to the per-batch override —
`call_tools(requests, {max_parallel: 40})` returns a single `INVALID_ARGS`
envelope instead of dispatching.

---

## Syntax Errors

### Error: "SyntaxError: Unexpected token"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "code": "SYNTAX_ERROR",
    "message": "SyntaxError: Unexpected token '{'",
    "stack": ""
  }
}
```

**Common Causes**:
1. Missing closing brackets/braces
2. Invalid JavaScript syntax

**Solutions**:

**Problem**: Missing brackets
```javascript
// Bad
const x = { value: 1, missing: 2

// Good
const x = { value: 1, missing: 2 };
```

**Problem**: Incomplete expressions
```javascript
// Bad
const msg = `Hello, ${

// Good
const msg = `Hello, ${name}!`;
```

---

### Error: "SyntaxError: Unexpected end of input"

**Symptom**: Code execution fails with "end of input" error.

**Cause**: Incomplete JavaScript expression or statement.

**Solution**: Ensure all blocks are properly closed:
```javascript
// Bad
var x = function() {
  return 42;
// Missing closing brace

// Good
var x = function() {
  return 42;
};
```

---

## Runtime Errors

### Error: "TypeError: Cannot read property 'X' of undefined/null"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "code": "RUNTIME_ERROR",
    "message": "TypeError: Cannot read property 'name' of undefined",
    "stack": "..."
  }
}
```

**Cause**: Accessing properties on `undefined` or `null` values.

**Solution**: Always check values before accessing properties:

```javascript
// Bad
var name = input.user.name;  // Crashes if input.user is undefined

// Good
var name = input.user ? input.user.name : 'Unknown';

// Better
var name = 'Unknown';
if (input && input.user && input.user.name) {
  name = input.user.name;
}
```

**For Tool Calls**:
```javascript
// Bad
var user = call_tool('github', 'get_user', {username: input.username});
var name = user.result.name;  // Crashes if user.ok is false

// Good
var user = call_tool('github', 'get_user', {username: input.username});
if (!user.ok) {
  return {error: user.error.message};
}
var name = user.result.name;
```

---

### Error: "ReferenceError: X is not defined"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "code": "RUNTIME_ERROR",
    "message": "ReferenceError: myVariable is not defined",
    "stack": "..."
  }
}
```

**Cause**: Using variables or functions that don't exist.

**Common Mistakes**:

**Problem**: Undefined variables
```javascript
// Bad
return result;  // 'result' was never declared

// Good
var result = 42;
return result;
```

**Problem**: Using unavailable functions
```javascript
// Bad
var data = require('fs').readFileSync('file.txt');  // require() not available

// Bad
setTimeout(function() { doWork(); }, 1000);  // setTimeout() not available

// Good
var data = call_tool('filesystem', 'read_file', {path: 'file.txt'});
```

---

### Error: "RangeError: Maximum call stack size exceeded"

**Symptom**: Execution fails with stack overflow.

**Cause**: Infinite recursion or very deep recursion.

**Solution**: Check for infinite loops or recursive calls:

```javascript
// Bad
function factorial(n) {
  return n * factorial(n - 1);  // No base case!
}

// Good
function factorial(n) {
  if (n <= 1) return 1;  // Base case
  return n * factorial(n - 1);
}

// Better (iterative)
function factorial(n) {
  var result = 1;
  for (var i = 2; i <= n; i++) {
    result *= i;
  }
  return result;
}
```

---

## Timeout Issues

### Error: "JavaScript execution timed out"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "code": "TIMEOUT",
    "message": "JavaScript execution timed out",
    "stack": ""
  }
}
```

**Common Causes**:
1. Infinite loops
2. Too many tool calls
3. Slow upstream servers
4. Heavy computation

**Solutions**:

**Problem**: Infinite loop
```javascript
// Bad
while (true) {
  // This will timeout
}

// Bad
for (var i = 0; i < 1000000000; i++) {
  // This takes too long
}

// Good
for (var i = 0; i < input.items.length; i++) {
  // Bounded loop
}
```

**Problem**: Insufficient timeout for workload
```javascript
// Bad: Default 2-minute timeout for 100 tool calls
{
  "code": "for(var i=0;i<100;i++){call_tool(...)}",
  "options": {"timeout_ms": 120000}  // Might not be enough
}

// Good: Increase timeout for heavy workloads
{
  "code": "for(var i=0;i<100;i++){call_tool(...)}",
  "options": {"timeout_ms": 300000}  // 5 minutes
}
```

**Problem**: Slow upstream tools
```javascript
// Solution: Reduce number of calls or increase timeout
{
  "code": "...",
  "options": {
    "timeout_ms": 300000,      // Increase timeout
    "max_tool_calls": 50       // Limit calls to prevent runaway
  }
}
```

---

## Tool Call Errors

### Error: "Exceeded maximum tool calls limit"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "code": "MAX_TOOL_CALLS_EXCEEDED",
    "message": "Exceeded maximum tool calls limit (10)",
    "stack": ""
  }
}
```

**Cause**: Code called `call_tool()` more times than `max_tool_calls` allows.
Every element of a `call_tools()` batch counts as one call too, checked in input
order — tail elements over the limit come back as per-slot
`MAX_TOOL_CALLS_EXCEEDED` errors and are never dispatched.

**Solution**:

**Option 1**: Increase limit
```json
{
  "code": "for(var i=0;i<20;i++){call_tool(...)}",
  "options": {"max_tool_calls": 25}  // Set higher than needed
}
```

**Option 2**: Reduce tool calls
```javascript
// Bad: Call tool for every item
for (var i = 0; i < 1000; i++) {
  call_tool('api', 'process', {id: items[i]});
}

// Good: Batch items if tool supports it
var batchSize = 100;
for (var i = 0; i < items.length; i += batchSize) {
  var batch = items.slice(i, i + batchSize);
  call_tool('api', 'process_batch', {items: batch});
}
```

---

### Error: "Server 'X' is not in the allowed servers list"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "code": "SERVER_NOT_ALLOWED",
    "message": "Server 'gitlab' is not in the allowed servers list",
    "stack": ""
  }
}
```

**Cause**: Attempted to call a server not in `allowed_servers` option.

**Solution**:

**Option 1**: Add server to allowed list
```json
{
  "code": "call_tool('gitlab', 'get_user', {username: 'test'})",
  "options": {
    "allowed_servers": ["github", "gitlab"]  // Add gitlab
  }
}
```

**Option 2**: Remove restriction (allow all servers)
```json
{
  "code": "call_tool('gitlab', 'get_user', {username: 'test'})",
  "options": {
    "allowed_servers": []  // Empty array = all servers allowed
  }
}
```

---

### Error: "server not found: X"

**Symptom**: `call_tool()` returns error saying server doesn't exist.

**Cause**: Server name is incorrect or server is not configured.

**Solution**:

1. **Check server name**: Verify spelling and case
```javascript
// Bad
call_tool('GitHub', 'get_user', {});  // Wrong case

// Good
call_tool('github', 'get_user', {});  // Correct name
```

2. **List available servers**:

3. **Add server if missing**:

---

### Error: "call_tools: element N: ..." (INVALID_ARGS)

**Symptom**: `call_tools()` returns a single envelope instead of an array of slots:
```json
{
  "ok": false,
  "error": {
    "code": "INVALID_ARGS",
    "message": "call_tools: element 3: must be an object with server and tool"
  }
}
```

**Cause**: The batch itself is malformed, so nothing was dispatched and no budget
was consumed. Triggers: `requests` is not an array, an element is not an object
with non-empty `server`/`tool` strings, a supplied `args` is not an object, the
array has a sparse hole, `options` is not an object, `max_parallel` is not an
integer in 1-32, or the batch exceeds 100 elements.

**Solution**: Fix the element the message names, then check the result shape
before mapping over it:
```javascript
var slots = call_tools(requests, {max_parallel: 8});
if (!Array.isArray(slots)) {
  // whole batch rejected — slots is {ok:false, error:{...}}
  ({error: slots.error.message});
} else {
  ({results: slots});
}
```

For more than 100 items, chunk the array and issue one `call_tools()` per chunk.

---


## Stored Script Errors

These apply to invocations that name a [stored script](overview.md#stored-scripts)
(`script: "<name>"`) instead of sending `code` inline. Every refusal names only
your own script and the reason — never a host path, a raw OS error, or what else
is stored.

### Error: "Provide exactly one of 'code' or 'script'"

**Symptom**:
```
Provide exactly one of 'code' (inline source) or 'script' (the name of a script stored in the 'scripts' directory inside mcpproxy's data directory) — not both, not neither.
```

**Cause**: The request carried both `code` and `script`, or neither. JSON Schema
cannot express "exactly one of", so neither field is schema-required and the
rule is enforced by the tool.

**Solution**: Send exactly one source.

---

### Error: "stored script X not found"

**Symptom**:
```
Cannot execute stored script: stored script "fetch-pr" not found (an MCP client must already know the script name; there is no listing)
```

The message is the same whether the scripts directory is empty or full.

**Cause**: No `<name>.js` / `<name>.ts` in the scripts directory. Usually a typo
(names are **case-sensitive**), a file that is not a script (uppercase or other
extension: `.JS`, `.mjs`, `.jsx` are ignored), the wrong directory — the scripts
directory is `<MCPPROXY_DATA_DIR>/scripts` — or a script added or renamed in the
last couple of seconds.

**Solution**: There is no listing, so check the name against the files in
`<MCPPROXY_DATA_DIR>/scripts` and give the agent the script names in its custom
instructions. If you just added the file, retry after a moment. mcpproxy never
creates the directory itself, so `mkdir -p` it. If you changed
`MCPPROXY_DATA_DIR`, restart mcpproxy with the intended environment.

**Case-insensitive filesystems** (the default macOS volume): the on-disk
spelling still decides. `FETCH-PR.JS` or `Fetch-pr.js` is not the script
`fetch-pr` even where the filesystem would open it under that name. A
differently-cased name and one that is not stored at all give the same refusal.

---

### Error: "invalid script name"

**Symptom**:
```
Cannot execute stored script: invalid script name "../../etc/passwd": character "." is not allowed (names are 1-64 characters of A-Z, a-z, 0-9, '-' or '_' — a name, never a path)
```

**Cause**: `script` is a **name**, never a path. Separators, `..`, dots,
extensions, non-ASCII characters and names longer than 64 characters are
rejected before the filesystem is touched at all.

**Solution**: Pass the base name only — `fetch-prs`, not `fetch-prs.js`,
`./fetch-prs.js`, or `/abs/path/fetch-prs.js`.

---

### Error: "stored script X is ambiguous"

**Symptom**:
```
Cannot execute stored script: stored script "triage" is ambiguous: both a .js and a .ts file exist — ask an administrator to remove one
```

**Cause**: Both extensions exist for one name — often a leftover after
converting a script from JavaScript to TypeScript. Ambiguity is never resolved
silently.

**Solution**: Delete (or rename) one of the two files.

---

### Error: "stored script X is oversized / empty / unreadable / non-regular"

**Symptom**:
```
Cannot execute stored script: stored script "big-report" is oversized: scripts are limited to 262144 bytes
```

**Cause**:

| Reason | Meaning |
|--------|---------|
| `oversized` | The file exceeds the 256 KB stored-script bound (inline `code` has no such bound; this one exists purely to bound the daemon-side read) |
| `empty` | Zero bytes — commonly a half-finished redirect (`> script.js`) |
| `unreadable` | Permissions or an I/O error |
| `non-regular` | The path is a symlink, directory, or device — scripts must be regular files |

**Solution**: Split an oversized workflow into several scripts (or move bulk
data into `input`), finish the write, fix permissions, or replace the symlink
with the real file. Copy, do not link:
```bash
cp /shared/workflows/report.js "$MCPPROXY_DATA_DIR/scripts/report.js"
```
The scripts *directory* itself may be a symlink — it is operator-controlled;
only the script file may not be.

---

### Error: "stored script X does not accept requested language Y"

**Symptom**:
```
Cannot execute stored script: stored script "daily-report" does not accept requested language "javascript" — omit 'language' and let it be derived automatically
```

**Cause**: The extension is authoritative for a stored script, and the explicit
`language` contradicted it.

**Solution**: Omit `language` entirely — the extension decides.

---

### Issue: An edited script still runs the old content

**Cause**: Almost always the file was not replaced where the daemon looks, or
the edit went to a different scripts directory. There is no cache and no
watcher: every invocation opens and reads the file once, so a completed
replacement is visible to the very next call — no restart, nothing to flush.

**Solution**: Edit by **atomic replace** so no invocation
can observe a half-written file:
```bash
tmp=$(mktemp "$MCPPROXY_DATA_DIR/scripts/.report.XXXXXX")
cp new-report.js "$tmp" && mv "$tmp" "$MCPPROXY_DATA_DIR/scripts/report.js"
```
Editing **in place** while an invocation is reading is the one unsupported case:
that run gets whatever the read returned (validated, but unspecified).

---

### Issue: No way to upload or edit a script through the API

**Cause**: Working as designed. There is **no write path** for stored scripts.
Code running in the sandbox has no filesystem access either.

**Solution**: Author scripts with your normal filesystem tooling (editor, `scp`,
configuration management).

---

## Serialization Errors

### Error: "Result contains non-JSON-serializable values"

**Symptom**:
```json
{
  "ok": false,
  "error": {
    "code": "SERIALIZATION_ERROR",
    "message": "Result contains non-JSON-serializable values (functions, circular references, etc.)",
    "stack": ""
  }
}
```

**Cause**: JavaScript return value contains functions, circular references, or other non-JSON types.

**Solutions**:

**Problem**: Returning functions
```javascript
// Bad
return {
  calculate: function() { return 42; }
};

// Good
return {
  result: 42
};
```

**Problem**: Returning undefined
```javascript
// Bad
return undefined;  // Not JSON-serializable

// Good
return null;  // JSON-serializable
```

**Problem**: Circular references
```javascript
// Bad
var obj = {value: 42};
obj.self = obj;  // Circular reference
return obj;

// Good
return {value: 42};
```

**Problem**: Special objects (Date, RegExp)
```javascript
// Bad
return {
  created: new Date()  // Date object not JSON-serializable
};

// Good
return {
  created: new Date().toISOString()  // String is JSON-serializable
};
```

---

## Performance Issues

### Issue: Slow execution times

**Symptom**: Code takes longer than expected to execute.

**Diagnosis**:
1. Check number of tool calls
2. Measure upstream tool latency
3. Look for inefficient loops
4. Check for excessive data processing

**Solutions**:

**Reduce tool calls**:
```javascript
// Bad: N tool calls
for (var i = 0; i < items.length; i++) {
  call_tool('api', 'get', {id: items[i]});
}

// Good: 1 batch tool call
call_tool('api', 'get_batch', {ids: items});
```

**Run independent calls in parallel** (when the tool has no bulk variant):
```javascript
// Bad: N sequential calls — latency is the sum
for (var i = 0; i < items.length; i++) {
  call_tool('api', 'get', {id: items[i]});
}

// Good: one batch — latency is about the slowest call
var slots = call_tools(items.map(function (id) {
  return {server: 'api', tool: 'get', args: {id: id}};
}), {max_parallel: 8});
```

**Cache repeated calls**:
```javascript
// Bad: Call same tool multiple times
for (var i = 0; i < items.length; i++) {
  var config = call_tool('api', 'get_config', {});  // Repeated call
  process(items[i], config);
}

// Good: Call once, reuse result
var config = call_tool('api', 'get_config', {});
if (!config.ok) return config;

for (var i = 0; i < items.length; i++) {
  process(items[i], config.result);
}
```

**Optimize loops**:
```javascript
// Bad: Inefficient nested loops
for (var i = 0; i < arr1.length; i++) {
  for (var j = 0; j < arr2.length; j++) {
    if (arr1[i] === arr2[j]) {
      results.push(arr1[i]);
    }
  }
}

// Good: Use object for O(1) lookup
var set = {};
for (var i = 0; i < arr2.length; i++) {
  set[arr2[i]] = true;
}
for (var i = 0; i < arr1.length; i++) {
  if (set[arr1[i]]) {
    results.push(arr1[i]);
  }
}
```

---

### Issue: Pool exhaustion (all VMs busy)

**Symptom**: Requests take longer to start, or you see "waiting for VM" logs.

**Cause**: More concurrent executions than pool size allows.

**Solution**: Increase pool size in configuration:
```json
{
  "code_execution_pool_size": 20  // Increase from default 10
}
```

**Trade-offs**:
- Larger pool = more concurrency, more memory usage
- Smaller pool = less concurrency, less memory usage

**Recommendation**: Monitor pool usage and adjust based on load.

---

## Debugging Tips

### Enable Debug Logging


**Server**: Edit config to set log level:
```json
{
  "log_level": "debug"
}
```

**What you'll see**:
- Tool call details (server, tool name, arguments)
- Execution timing
- Pool acquisition/release
- Detailed error messages

---

### Use console.log() for Debugging

```javascript
var user = call_tool('github', 'get_user', {username: input.username});
console.log('User response:', JSON.stringify(user));  // Logs to server logs

if (!user.ok) {
  console.log('Error occurred:', user.error);
  return {error: user.error.message};
}

console.log('User name:', user.result.name);
return {name: user.result.name};
```

**Where to find logs**: `~/.mcpproxy/logs/main.log` (or platform-specific log directory)

---

### Test Code Incrementally

Start simple and build up:

```javascript
// Step 1: Verify input access
return input;

// Step 2: Test tool call
var res = call_tool('github', 'get_user', {username: 'octocat'});
return res;

// Step 3: Add error handling
var res = call_tool('github', 'get_user', {username: 'octocat'});
if (!res.ok) return {error: res.error.message};
return res.result;

// Step 4: Add data transformation
var res = call_tool('github', 'get_user', {username: 'octocat'});
if (!res.ok) return {error: res.error.message};
return {
  name: res.result.name,
  repos: res.result.public_repos
};
```

---

### Validate JSON Serialization

Test that your return value is JSON-serializable:

```javascript
var result = {/* your data */};

// This will throw if result is not serializable
var json = JSON.stringify(result);

// If it succeeds, return it
return result;
```

---

### Use Error Boundaries

Wrap risky operations in try-catch or use checks:

```javascript
// Check before accessing
if (res && res.ok && res.result && res.result.name) {
  return {name: res.result.name};
} else {
  return {error: 'Invalid response structure'};
}

// For loops, check each iteration
for (var i = 0; i < items.length; i++) {
  if (!items[i]) {
    console.log('Skipping null item at index', i);
    continue;
  }
  // Process items[i]
}
```

---


---

## Getting Help

If you're still stuck after trying these solutions:

1. **Check the examples**: [examples.md](examples.md) has 10+ working patterns
2. **Review API reference**: [api-reference.md](api-reference.md) has complete schema
3. **Read the overview**: [overview.md](overview.md) explains architecture
4. **Check server logs**: `~/.mcpproxy/logs/main.log` for detailed error messages
5. **File an issue**: [GitHub Issues](https://github.com/smart-mcp-proxy/mcpproxy-go/issues)

### What to Include in Bug Reports

```markdown
**Environment**:
- MCPProxy version: (run `mcpproxy --version`)
- OS: (macOS/Linux/Windows)
- Configuration: (relevant config fields)

**Code**:
```javascript
// Your JavaScript code here
```

**Input**:
```json
{
  "input": {...}
}
```

**Expected**: What you expected to happen

**Actual**: What actually happened (include full error message)

**Logs**: Relevant lines from `~/.mcpproxy/logs/main.log`
```

---

## Next Steps

- **Examples**: See [examples.md](examples.md) for working code samples
- **API Reference**: See [api-reference.md](api-reference.md) for complete schema
- **Overview**: See [overview.md](overview.md) for architecture and best practices