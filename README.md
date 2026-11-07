# mcp-mfjs-proxy

This project is a workaround for cases where you want connect some MCP to Kimi LLM and get errors like
```
[provider.api_error] tools.function.parameters is not a valid moonshot flavored json schema, details: <At path 'properties.arguments.items': items must be an object>
```

## Prior art

- [PR #830](https://github.com/MoonshotAI/kimi-code/pull/830)
- [PR #1720](https://github.com/MoonshotAI/kimi-code/pull/1720)

These PRs attempt to fix this problem just for `kimi-code` agent. However, the issue is *not* in `kimi-code`, but rather, it's on Moonshot's backend. So,
- these PRs are unlikely to get merged, which means that authors will need to maintain them in the quickly evolving codebase
- it affects all agents, not just `kimi-code`

## The workaround

This project introduces a proxy for stdio MCP servers. Essentially, you just wrap the affected server with this proxy
```bash
mcp-mfjs-proxy -- agent-lsp
```
and it should start working.
(BTW [agent-lsp](https://github.com/blackwell-systems/agent-lsp) in question)

This project also includes the `validate-mcp.py` script which checks whether your MCP conforms to the specification Kimi wants or not.
```bash
$ git submodule update --init --recursive # check out walle
$ ./validate-mcp.py -- agent-lsp
...
execute_command.inputSchema: Validation failed: At path 'properties.arguments.items': items must be an object
Checked 66 schemas: 1 invalid
```
It checks that by using [walle](https://github.com/MoonshotAI/walle), which is a Moonshot's schema validator.

## Pros and cons

- this project depends on standartized MCP specification, rather than agent's internal codebase
- this project (in theory, at least) is supposed to work with any agent, not just `kimi-code`

*however*,

- this project applies only fixes from `PR #830` and `PR #1720` (see above), which seems to be enough for me
- right now, this project supports stdio MCPs only

if you want to support http MCPs and implement other fixes, then

## Contributions

Contributions welcome.
