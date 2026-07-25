# Doc build tools

Regenerates the Word versions of the user docs
([`../quickstart.docx`](../quickstart.docx), [`../user-guide.docx`](../user-guide.docx)) from the
markdown, embedding both the concept diagrams and any real screenshots.

## Rebuild
```bash
./build.sh          # sets up a venv, renders figures, writes the .docx files
```

## Screenshots — automatic
With the host running, capture every tab straight from the live UI:
```bash
# requires: pip install playwright   (uses your system Google Chrome)
python screenshots.py http://localhost:8081 day          # theme: day | night | auto
python screenshots.py http://localhost:8081 night -night # a second, -night set
```
This writes `overview.png … about.png` (and `*-night.png`) into `../images/`, auto-trimmed to
content. Then `./build.sh` embeds them. The guide expects: `overview`, `live-frames`, `rf-spectrum`,
`active-testing`, `diagnostics`, `config`, `about` (+ `theme-comparison`).

## …or add them by hand
Save a PNG in `../images/` with the matching name above and run `./build.sh`. Missing files show a
"figure not found" note until added.

- `render_figures.py` draws the concept diagrams (architecture, signal-sources, ui-tabs,
  routing-tree) as PNGs with Pillow — no cairo needed.
- `md_to_docx.py` converts the markdown to `.docx` (headings, tables, code, lists, images).
