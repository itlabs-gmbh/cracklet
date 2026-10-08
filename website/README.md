# cracklet website

Static website for cracklet (landing page and docs), no build step.

- `index.html` – landing page
- `docs.html` – quickstart and command reference
- `agents.html` – agent guides (Claude Code, GitHub, MCP, pools)
- `capabilities.html` – broker, grants, scopes, secrets and capability files in detail
- `impressum.html` – legal notice (IT-Labs GmbH)
- `style.css` – design tokens and layout, light/dark via `prefers-color-scheme`;
  the ember `--accent` is the one brand colour
- `docs.css` – documentation layout (tables, code listings, callouts), loaded after `style.css`
- `copy.js` – copy-to-clipboard for the command boxes
- `favicon.svg`

Preview locally:

```sh
cd website && python3 -m http.server 8080
```

Keep the docs in sync with the CLI: they quote flags, paths and limits from
`internal/` (broker, cap, grant, secret). Change both in the same PR.

Deploy by uploading the directory to any static host (GitHub Pages, Netlify, Cloudflare Pages, nginx).
