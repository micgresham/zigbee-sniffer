#!/usr/bin/env bash
# Regenerate docs/quickstart.docx and docs/user-guide.docx from the markdown.
# Drop real screenshots into docs/images/ (e.g. overview.png) and re-run this.
set -e
cd "$(dirname "$0")"
python3 -m venv .venv 2>/dev/null || true
.venv/bin/pip install -q --upgrade pip
.venv/bin/pip install -q python-docx pillow
.venv/bin/python render_figures.py   # draw the concept diagrams → _figs/*.png
.venv/bin/python md_to_docx.py        # markdown + figures/screenshots → ../*.docx
echo "✓ docs/quickstart.docx and docs/user-guide.docx rebuilt"
