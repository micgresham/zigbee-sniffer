#!/usr/bin/env python3
"""Capture a screenshot of each dashboard tab into docs/images/, using the
system Google Chrome via Playwright. Point it at a running host:

    python screenshots.py http://localhost:8080

Then run build.sh to embed them in the .docx. Requires: playwright (pip).
"""
import os, sys, time
from playwright.sync_api import sync_playwright
from PIL import Image, ImageChops

def trim_bottom(path, margin=40):
    """Crop empty background below the content by scanning rows up from the
    bottom (the bottom-centre pixel is the reference 'blank' colour)."""
    im = Image.open(path).convert("RGB")
    w, h = im.size
    px = im.load()
    ref = px[w // 2, h - 3]
    step = max(1, w // 60)
    def blank(y):
        return all(sum(abs(a - b) for a, b in zip(px[x, y], ref)) < 20
                   for x in range(0, w, step))
    y = h - 1
    while y > 10 and blank(y):
        y -= 1
    if y < h - margin:
        im.crop((0, 0, w, min(h, y + margin))).save(path)

URL = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080"
THEME = sys.argv[2] if len(sys.argv) > 2 else None   # "day" | "night" | "auto"
SUFFIX = sys.argv[3] if len(sys.argv) > 3 else ""    # e.g. "-night" for a second set
IMG = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "images")
os.makedirs(IMG, exist_ok=True)

# (tab data-t, output filename, extra action before shot)
TABS = [
    ("overview",   "overview.png",      None),
    ("frames",     "live-frames.png",   None),
    ("spectrum",   "rf-spectrum.png",   "scan"),
    ("active",     "active-testing.png", None),
    ("diagnostics","diagnostics.png",   None),
    ("config",     "config.png",        None),
    ("about",      "about.png",         None),
]

def main():
    with sync_playwright() as p:
        browser = p.chromium.launch(channel="chrome", headless=True)
        page = browser.new_context(viewport={"width": 1500, "height": 1000},
                                   device_scale_factor=2).new_page()
        page.on("dialog", lambda d: d.accept())  # accept the single-radio confirm
        page.goto(URL, wait_until="networkidle")
        if THEME:
            page.evaluate(f"localStorage.setItem('zbtheme','{THEME}')")
            page.reload(wait_until="networkidle")
        time.sleep(5)  # let live data (devices/frames/routing) fill in
        for key, fname, action in TABS:
            page.click(f'button.tabbtn[data-t="{key}"]')
            time.sleep(1.5)
            if action == "scan":
                try:
                    page.click("#sp-scan"); time.sleep(3)
                except Exception:
                    pass
            fn = fname.replace(".png", SUFFIX + ".png")
            out = os.path.abspath(os.path.join(IMG, fn))
            page.screenshot(path=out)  # viewport-height; trim any blank tail
            trim_bottom(out)
            print("shot", fn)
        browser.close()

if __name__ == "__main__":
    main()
