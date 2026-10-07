# Example capabilities

Copy a file into `~/.cracklet/caps/`, run `cracklet cap lint <name>`, then
`cracklet grant <vm> <name>`. Each file is a complete capability; the comments
at the top explain what it hands into the guest and how.

| File                   | Primitive | Shows                                              |
|------------------------|-----------|----------------------------------------------------|
| `chrome-devtools.toml` | `mcp`     | a stdio MCP server on the Mac bridged into the guest |
| `gh-api.toml`          | `exec`    | an external program answering requests (no secret leaves the Mac) |

The embedded `claude` and `github` capabilities are the `proxy` examples; print
them with `cracklet cap show claude` and `cracklet cap show github`.
