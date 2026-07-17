#!/usr/bin/env python3
"""Generate docs/tethered-to-satellite-wiring.pdf from the matching markdown.

Handles: headings (H1–H3), bullet/numbered lists, tables, blockquotes,
code/mermaid blocks (code blocks are rendered verbatim; mermaid flowchart
text is replaced with a "see markdown source" note since PDF can't execute JS).

Deps: fpdf2  (installed by build.sh / venv)
"""
import os, re
from fpdf import FPDF
from fpdf.enums import XPos, YPos

TOOLS = os.path.dirname(os.path.abspath(__file__))
DOCS  = os.path.dirname(TOOLS)

# ── colours ────────────────────────────────────────────────────────────────
H1_COL   = (30,  30,  90)   # deep blue-navy
H2_COL   = (10,  80, 140)   # mid-blue
H3_COL   = (60,  60, 120)
TBL_HDR  = (220, 230, 245)  # pale blue header fill
TBL_ROW1 = (248, 250, 255)  # near-white
TBL_ROW2 = (235, 241, 252)  # light-blue alternating
RULE_COL = (190, 200, 220)
NOTE_COL = (245, 248, 225)  # pale yellow for blockquotes
CODE_COL = (245, 245, 245)  # light grey for code
MERM_COL = (235, 245, 235)  # pale green tint for mermaid placeholder

# ── column widths (mm) for the connection table ───────────────────────────
# Net | Primary GPIO | Satellite pin | Required | Direction
# Detected automatically from the header row.
AUTO_COL = True  # let the script infer widths from header names

PAGE_W  = 210
MARGIN  = 18
BODY_W  = PAGE_W - 2 * MARGIN

INLINE = re.compile(r'(\*\*.+?\*\*|`[^`]+`|\[[^\]]+\]\([^)]+\))')

# Map common Unicode characters to Latin-1 safe equivalents so we can use
# Helvetica (a core PDF font) without embedding a full TTF.
_UNICODE_MAP = str.maketrans({
    "\u2192": "->",  "\u2190": "<-",  "\u2194": "<->",
    "\u2014": "--",  "\u2013": "-",
    "\u2022": "-",   "\u2019": "'",   "\u2018": "'",
    "\u201c": '"',   "\u201d": '"',
    "\u00b7": ".",   "\u2264": "<=",  "\u2265": ">=",
    "\u00a9": "(c)", "\u00ae": "(R)",
    "\u03b1": "alpha", "\u03b2": "beta",
})

def latin1(text: str) -> str:
    """Replace non-Latin-1 chars with ASCII-safe substitutes."""
    return text.translate(_UNICODE_MAP).encode("latin-1", errors="replace").decode("latin-1")


def strip_inline(text):
    """Strip markdown inline markup, returning plain text."""
    out = []
    for part in INLINE.split(text):
        if part.startswith("**") and part.endswith("**"):
            out.append(part[2:-2])
        elif part.startswith("`") and part.endswith("`"):
            out.append(part[1:-1])
        elif part.startswith("["):
            m = re.match(r'\[([^\]]+)\]\(([^)]+)\)', part)
            out.append(m.group(1) if m else part)
        else:
            out.append(part)
    return latin1("".join(out))


class WiringPDF(FPDF):
    def __init__(self):
        super().__init__(unit="mm", format="A4")
        self.set_margins(MARGIN, MARGIN, MARGIN)
        self.set_auto_page_break(True, margin=MARGIN)
        self.add_page()

    # ── header / footer ────────────────────────────────────────────────────
    def header(self):
        if self.page_no() == 1:
            return
        self.set_font("Helvetica", "I", 8)
        self.set_text_color(160, 160, 160)
        self.cell(0, 6, latin1("Tethered C6 -> Satellite Wiring - zigbee-sniffer"),
                  new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        self.set_draw_color(*RULE_COL)
        self.line(MARGIN, self.get_y(), PAGE_W - MARGIN, self.get_y())
        self.ln(2)

    def footer(self):
        self.set_y(-12)
        self.set_font("Helvetica", "I", 8)
        self.set_text_color(160, 160, 160)
        self.cell(0, 6, f"Page {self.page_no()}", align="C",
                  new_x=XPos.LMARGIN, new_y=YPos.NEXT)

    # ── primitives ─────────────────────────────────────────────────────────
    def rule(self, col=RULE_COL):
        self.set_draw_color(*col)
        self.line(MARGIN, self.get_y(), PAGE_W - MARGIN, self.get_y())
        self.ln(2)

    def heading(self, text, level):
        sizes  = {1: 18, 2: 14, 3: 11}
        styles = {1: "B",  2: "B",  3: "B"}
        cols   = {1: H1_COL, 2: H2_COL, 3: H3_COL}
        self.ln(4 if level > 1 else 6)
        self.set_font("Helvetica", styles[level], sizes[level])
        self.set_text_color(*cols[level])
        self.multi_cell(0, sizes[level] * 0.5, latin1(text),
                        new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        if level == 1:
            self.ln(1)
            self.rule(H1_COL)
        elif level == 2:
            self.ln(1)
            self.rule(H2_COL)
        else:
            self.ln(1)
        self.set_text_color(0, 0, 0)

    def body_text(self, text):
        self.set_font("Helvetica", "", 10)
        self.set_text_color(40, 40, 40)
        self.multi_cell(0, 5.5, latin1(text), new_x=XPos.LMARGIN, new_y=YPos.NEXT)

    def bullet(self, text, indent=6, numbered=None):
        self.set_font("Helvetica", "", 10)
        self.set_text_color(40, 40, 40)
        bul = f"{numbered}." if numbered else "-"
        x0 = self.get_x()
        self.set_x(MARGIN + indent)
        self.cell(6, 5.5, latin1(bul))
        self.multi_cell(BODY_W - indent - 6, 5.5, latin1(text),
                        new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        self.set_x(x0)

    def blockquote(self, lines):
        self.ln(1)
        x = MARGIN + 4
        y0 = self.get_y()
        self.set_fill_color(*NOTE_COL)
        self.set_font("Helvetica", "I", 9.5)
        self.set_text_color(70, 70, 40)
        for ln in lines:
            text = ln.lstrip(">").strip()
            self.set_x(x + 4)
            self.multi_cell(BODY_W - 8, 5, latin1(text), fill=True,
                            new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        y1 = self.get_y()
        self.set_draw_color(190, 180, 80)
        self.line(MARGIN + 4, y0, MARGIN + 4, y1)
        self.set_text_color(0, 0, 0)
        self.ln(1)

    def code_block(self, lines):
        self.ln(1)
        self.set_fill_color(*CODE_COL)
        self.set_font("Courier", "", 8)
        self.set_text_color(50, 50, 50)
        self.set_draw_color(*RULE_COL)
        for ln in lines:
            self.set_x(MARGIN + 2)
            self.multi_cell(BODY_W - 4, 4.5, latin1(ln if ln else " "), fill=True,
                            new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        self.set_text_color(0, 0, 0)
        self.ln(1)

    def mermaid_placeholder(self, lines):
        """Render a summary box for a mermaid block."""
        self.ln(2)
        self.set_fill_color(*MERM_COL)
        self.set_draw_color(130, 180, 130)
        self.set_font("Helvetica", "B", 9)
        self.set_text_color(40, 100, 40)
        self.set_x(MARGIN)
        self.cell(BODY_W, 6, latin1("[ Mermaid Wiring Diagram ]"),
                  border=1, align="C", fill=True,
                  new_x=XPos.LMARGIN, new_y=YPos.NEXT)

        # Extract readable signal lines from the mermaid source
        signals = []
        for ln in lines:
            # arrows: A --> B  or  A --- B
            m = re.match(r'\s*(\w+)\s*(-+>?|<-+|-{3})\s*(\w+)', ln)
            if m:
                lbl = ln.strip()
                signals.append(lbl)

        self.set_font("Courier", "", 7.5)
        self.set_text_color(50, 80, 50)
        for sig in signals[:35]:    # cap to avoid overrun
            self.set_x(MARGIN + 3)
            self.cell(BODY_W - 6, 4, latin1(sig), fill=True,
                      new_x=XPos.LMARGIN, new_y=YPos.NEXT)
        self.set_text_color(0, 0, 0)
        self.set_draw_color(0, 0, 0)
        self.ln(2)

    def table(self, rows):
        """Render a markdown pipe-table."""
        if not rows:
            return
        # Filter separator row (---)
        header = [c.strip() for c in rows[0]]
        body = []
        for row in rows[1:]:
            cells = [c.strip() for c in row]
            if all(set(c) <= set("- :") for c in cells if c):
                continue
            body.append(cells)

        ncols = len(header)
        if ncols == 0:
            return

        # Column widths: equal by default, but scale the last (Direction) wider
        col_w = [BODY_W / ncols] * ncols
        # If we recognise the standard 5-column table, use fixed widths
        if ncols == 5:
            col_w = [20, 38, 38, 28, 46]   # Net, Primary GPIO, Sat pin, Req, Direction

        self.ln(2)
        row_h = 6

        def draw_row(cells, is_header=False, bg=None):
            if bg:
                self.set_fill_color(*bg)
            self.set_font("Helvetica", "B" if is_header else "", 9)
            self.set_text_color(30, 30, 30)
            x0 = MARGIN
            y0 = self.get_y()
            # Compute per-cell height
            cell_heights = []
            for i, cell in enumerate(cells[:ncols]):
                txt = strip_inline(cell)
                n_lines = max(1, int(len(txt) * 2.2 / col_w[i]) + 1)
                cell_heights.append(n_lines * row_h)
            h = max(row_h, max(cell_heights))
            for i, cell in enumerate(cells[:ncols]):
                txt = strip_inline(cell)
                self.set_xy(x0 + sum(col_w[:i]), y0)
                self.multi_cell(col_w[i], row_h, latin1(txt), border=1,
                                fill=(bg is not None),
                                align="L" if not is_header else "C",
                                new_x=XPos.RIGHT, new_y=YPos.TOP)
            self.set_xy(MARGIN, y0 + h)

        draw_row(header, is_header=True, bg=TBL_HDR)
        for idx, row in enumerate(body):
            bg = TBL_ROW1 if idx % 2 == 0 else TBL_ROW2
            draw_row(row, bg=bg)
        self.ln(3)


# ── main parser ─────────────────────────────────────────────────────────────

def convert(md_path, out_path):
    pdf = WiringPDF()
    lines = open(md_path).read().split("\n")
    n, i = len(lines), 0
    tbl: list[list[str]] = []
    quote_buf: list[str] = []

    def flush_tbl():
        nonlocal tbl
        if tbl:
            pdf.table(tbl)
            tbl = []

    def flush_quote():
        nonlocal quote_buf
        if quote_buf:
            pdf.blockquote(quote_buf)
            quote_buf = []

    while i < n:
        line = lines[i]

        # ── code / mermaid block ───────────────────────────────────────────
        if line.strip().startswith("```"):
            flush_tbl(); flush_quote()
            lang = line.strip()[3:].strip().lower()
            i += 1
            block_lines = []
            while i < n and not lines[i].strip().startswith("```"):
                block_lines.append(lines[i])
                i += 1
            i += 1  # skip closing ```
            if lang == "mermaid":
                pdf.mermaid_placeholder(block_lines)
            else:
                pdf.code_block(block_lines)
            continue

        # ── blockquote ────────────────────────────────────────────────────
        if line.startswith(">"):
            flush_tbl()
            quote_buf.append(line)
            i += 1
            continue
        else:
            flush_quote()

        # ── table row ─────────────────────────────────────────────────────
        stripped = line.strip()
        if stripped.startswith("|") and stripped.endswith("|"):
            cols = stripped.strip("|").split("|")
            tbl.append(cols)
            i += 1
            continue
        else:
            flush_tbl()

        # ── headings ──────────────────────────────────────────────────────
        m = re.match(r'^(#{1,3})\s+(.*)', line)
        if m:
            pdf.heading(m.group(2).strip(), len(m.group(1)))
            i += 1
            continue

        # ── horizontal rule ───────────────────────────────────────────────
        if stripped in ("---", "***", "___"):
            pdf.rule()
            i += 1
            continue

        # ── bullets ───────────────────────────────────────────────────────
        mb = re.match(r'^\s*[-*]\s+(.*)', line)
        if mb:
            pdf.bullet(strip_inline(mb.group(1)))
            i += 1
            continue
        mn = re.match(r'^\s*(\d+)\.\s+(.*)', line)
        if mn:
            pdf.bullet(strip_inline(mn.group(2)), numbered=int(mn.group(1)))
            i += 1
            continue

        # ── blank line ────────────────────────────────────────────────────
        if not stripped:
            i += 1
            continue

        # ── body text ─────────────────────────────────────────────────────
        pdf.body_text(strip_inline(line))
        i += 1

    flush_tbl()
    flush_quote()
    pdf.output(out_path)
    print("wrote", out_path)


if __name__ == "__main__":
    src = os.path.join(DOCS, "tethered-to-satellite-wiring.md")
    dst = os.path.join(DOCS, "tethered-to-satellite-wiring.pdf")
    convert(src, dst)
