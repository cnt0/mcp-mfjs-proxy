# Agent Guide

## Project overview

`mcp-mfjs-proxy` is a transparent proxy for stdio Model Context Protocol (MCP) servers. It rewrites tool schemas that valid MCP servers emit but Moonshot's stricter "moonshot flavored JSON schema" (MFJS) validation rejects. It is agent-independent: an MCP client starts the proxy, and the proxy starts the real server as a child process.

The proxy handles newline-delimited JSON-RPC over stdio only. It does not provide an HTTP transport, open network listeners, persist state, or implement a general MCP client/server. The repository is licensed under GNU GPL v3; see `LICENSE`.

## Repository layout and configuration

Document only files tracked in the root repository. Untracked local helpers, checkouts, and build artifacts are temporary and should not be included in this guide.

- `go.mod`: the only root build manifest. It declares module `mcp-mfjs-proxy` and Go `1.27.3`. The proxy uses only the Go standard library, so there is no `go.sum` and no dependency-install step.
- `main.go`: CLI parsing, child-process lifecycle, signal forwarding, exit-code propagation, and the two stdio relay loops.
- `rewrite.go`: JSON-RPC message classification, request/response correlation, tool-name rewriting and restoration, and dispatch to schema sanitization.
- `sanitize.go`: the schema compatibility transformations, including local reference resolution and circular-reference handling.
- `validate-mcp.py`: an executable, standard-library-only Python 3 diagnostic utility. It starts an MCP server, performs `initialize` and `tools/list`, and validates every advertised `inputSchema` and `outputSchema` with `walle -level strict`.
- `.gitmodules`: registers `walle` at `walle/`, pinned as a Git submodule. `walle` is needed by the validator, not by the proxy build.
- `README.md`: user-facing rationale and basic usage.

There are no root `package.json`, `pyproject.toml`, `Cargo.toml`, Makefile, container manifest, linter configuration, or GitHub Actions workflows.

## Build and run

Use Go 1.27.3 or a compatible newer toolchain:

```bash
go build -o mcp-mfjs-proxy .
```

For a compile-only check without replacing a local binary:

```bash
go build ./...
```

Wrap the original stdio MCP command after `--`:

```bash
./mcp-mfjs-proxy -- agent-lsp
./mcp-mfjs-proxy --report -- agent-lsp
./mcp-mfjs-proxy --no-rename -- agent-lsp
```

Flags:

- `--report` logs tool renames, schema rewrite counts, and tool-map invalidation to stderr.
- `--no-rename` leaves tool names unchanged; schema sanitization still runs.
- A missing upstream command or a flag-parse error produces usage on stderr and exit status 2.

The proxy has no configuration file. It passes its environment to the upstream process. Configure an MCP client by prefixing the original server command with the proxy command.

### Schema validator

If using the submodule-backed validator, initialize `walle` first. Its configured URL uses GitHub SSH:

```bash
git submodule update --init --recursive
./validate-mcp.py -- agent-lsp
```

Validate the sanitized schemas by wrapping the same upstream server with the proxy:

```bash
./validate-mcp.py -- ./mcp-mfjs-proxy --report -- agent-lsp
```

By default, the script builds `walle` from `walle/cmd/walle` into a temporary directory. To skip the submodule and that build, point `WALLE_BIN` at an existing executable:

```bash
WALLE_BIN=/path/to/walle ./validate-mcp.py -- agent-lsp
```

`MCP_TIMEOUT` sets the response timeout in seconds for MCP requests and defaults to `15`:

```bash
MCP_TIMEOUT=30 ./validate-mcp.py -- agent-lsp
```

The validator exits 0 when all checked schemas pass, 1 on validation or runtime failures, and 2 for missing command arguments. It validates schemas only; it does not exercise `tools/call` or `notifications/tools/list_changed`.

## Runtime architecture

`main.go` starts the upstream command directly with `os/exec`, without a shell. The child inherits the environment, its stderr is connected to the proxy's stderr, and stdin/stdout are piped through the proxy. One goroutine relays client stdin to upstream stdin; the main flow relays upstream stdout to client stdout. Both use line-oriented reads, including a final unterminated line.

The proxy forwards interrupt, HUP, TERM, and QUIT signals to the child. It returns the child's exit code, or `128 + signal` when the child dies from a signal. An upstream-output relay failure kills the child and returns 1.

`rewrite.go` keeps two mutex-protected maps:

- `pending` maps string or numeric JSON-RPC request IDs to their methods, allowing a response to be identified as a `tools/list` response.
- `toolNames` maps rewritten tool names back to their upstream names.

Only these messages are candidates for semantic rewriting:

1. A matched `tools/list` response: sanitize every tool's `inputSchema` and `outputSchema`; unless `--no-rename` is set, also sanitize each tool name and record its reverse mapping.
2. A `tools/call` request: replace a rewritten `params.name` with the original upstream name.
3. `notifications/tools/list_changed`: clear the tool-name mapping.

Other messages pass through byte-for-byte. Malformed JSON and unrecognized shapes also pass through unchanged. Rewritten messages are re-marshaled as compact JSON while preserving their LF or CRLF line ending. A message-level rewrite failure retains the original response. An individual schema sanitization error is logged and leaves that schema unchanged, while other schemas in the response may still be rewritten.

Tool-name sanitization replaces characters outside `[a-zA-Z0-9_-]` with `_` and collapses repeated underscores. Schema sanitization includes:

- Resolving local JSON Pointer `$ref` values, merging reference siblings, handling circular references, and removing top-level `$defs`/`definitions` buckets after dereferencing.
- Inferring missing types from values or schema structure, reconciling `type` with `enum`, `const`, `anyOf`, and `oneOf`, and converting type arrays or mixed-type enums to compatible branches.
- Converting tuple-style `prefixItems` and array-valued `items` into supported `items` forms, deriving `maxItems` where required, and handling `items: false`.
- Removing names from `required` when they do not exist in `properties`.
- Recursing through supported property, item, combinator, and dependent-schema positions.

## Development conventions

- Keep the root Go files in `package main`; there is currently no internal package hierarchy.
- Use `gofmt` and the existing standard-library idioms. Avoid adding a framework or external dependency for functionality already implemented locally.
- Preserve transparent passthrough. The design deliberately avoids a typed MCP SDK so new MCP methods, notifications, capabilities, and protocol versions do not require proxy support. Add rewrites only for compatibility cases that need them.
- Never write diagnostics to stdout: stdout is the MCP protocol stream. Use stderr for logs and upstream diagnostics.
- Keep accesses to `pending` and `toolNames` synchronized. Preserve string and numeric request-ID handling and LF/CRLF behavior when changing message parsing.
- Preserve the original message when parsing or sanitization cannot safely rewrite it.
- Treat `sanitize.go` as a port of the reference behavior, not as a general-purpose JSON Schema implementation. Validate semantic changes with `walle`.
- Keep `validate-mcp.py` Python-stdlib-only and retain its executable command-line interface.

## Testing and verification

Run these checks after changing root code:

```bash
gofmt -d main.go rewrite.go sanitize.go
go vet ./...
go test ./...
go build ./...
```

There are currently no root Go or Python test files and no CI configuration. Consequently, `go test ./...` is a compile check, not a behavioral test suite.

For end-to-end verification, run `validate-mcp.py` against a real stdio MCP server both directly and through the proxy. Use `--report` to inspect rewrites on stderr. When changing name rewriting, also perform a real `tools/call` to confirm the upstream server receives its original tool name; when changing cache invalidation, exercise a subsequent `notifications/tools/list_changed` and `tools/list` cycle. Neither behavior is covered by the validator.

## Security and operational considerations

- The proxy and validator intentionally execute the supplied upstream command and inherit the current environment. Run only trusted commands, and assume they can read the same environment variables and filesystem permissions as the caller.
- `WALLE_BIN` also selects an executable to run; do not point it at an untrusted binary.
- Treat upstream JSON and schemas as untrusted input. Preserve graceful handling of malformed messages and avoid protocol output on stderr/stdout other than the intended channels.
- The proxy provides process and protocol plumbing, not a security sandbox. It adds no authentication, authorization, encryption, or process isolation.

## Deployment and release process

There is no automated CI/CD, release, packaging, container, or service-deployment process in this repository. Deployment currently consists of building the binary, placing it on the MCP client's machine, and changing the client's server command to invoke the proxy. The upstream MCP server and its own runtime prerequisites must also be installed. Building or running the proxy does not require `walle`; only the optional validation workflow does.
