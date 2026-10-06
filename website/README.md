# cracklet website

Static landing page for cracklet, no build step.

- `index.html` – landing page
- `impressum.html` – legal notice (IT-Labs GmbH)
- `style.css` – design tokens and layout, light/dark via `prefers-color-scheme`
- `copy.js` – copy-to-clipboard for the command boxes
- `favicon.svg`

Preview locally:

```sh
cd website && python3 -m http.server 8080
```

Deploy by uploading the directory to any static host (GitHub Pages, Netlify, Cloudflare Pages, nginx).
