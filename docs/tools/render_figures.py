#!/usr/bin/env python3
"""Render the concept figures as PNGs with Pillow (no cairo needed).
Output: docs/tools/_figs/*.png — embedded into the .docx by md_to_docx.py."""
import os
from PIL import Image, ImageDraw, ImageFont

OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "_figs")
os.makedirs(OUT, exist_ok=True)
S = 2  # supersample for crisp text

BG="#0d1117"; PANEL="#161b22"; PANEL2="#0f141b"; LINE="#2a3340"; FG="#e6edf3"
DIM="#8b97a3"; DIM2="#5b6672"; ACC="#58a6ff"; GOOD="#3fb950"; FAIR="#d29922"
WEAK="#f85149"; PUR="#7c5cff"; NODE="#c9d4e0"; LEAF="#8b97a3"

def font(sz, bold=False):
    cands = (["/System/Library/Fonts/Supplemental/Arial Bold.ttf",
              "/System/Library/Fonts/Helvetica.ttc"] if bold else
             ["/System/Library/Fonts/Supplemental/Arial.ttf",
              "/System/Library/Fonts/Helvetica.ttc"])
    for c in cands:
        try: return ImageFont.truetype(c, sz*S)
        except Exception: pass
    return ImageFont.load_default()

def new(w, h):
    img = Image.new("RGB", (w*S, h*S), BG)
    return img, ImageDraw.Draw(img)

def rr(d, x, y, w, h, r=10, fill=None, outline=None, ow=1):
    d.rounded_rectangle([x*S, y*S, (x+w)*S, (y+h)*S], radius=r*S,
                        fill=fill, outline=outline, width=ow*S)

def txt(d, x, y, s, sz=11, col=FG, bold=False, anchor="la"):
    d.text((x*S, y*S), s, font=font(sz, bold), fill=col, anchor=anchor)

def line(d, x1, y1, x2, y2, col=LINE, w=1, dash=False):
    if dash:
        import math
        dx, dy = x2-x1, y2-y1; L = math.hypot(dx, dy); n = max(1, int(L/8))
        for i in range(n):
            if i % 2: continue
            a=i/n; b=min(1,(i+1)/n)
            d.line([(x1+dx*a)*S,(y1+dy*a)*S,(x1+dx*b)*S,(y1+dy*b)*S], fill=col, width=int(w*S))
    else:
        d.line([x1*S,y1*S,x2*S,y2*S], fill=col, width=int(w*S))

def circle(d, cx, cy, r, fill):
    d.ellipse([(cx-r)*S,(cy-r)*S,(cx+r)*S,(cy+r)*S], fill=fill)

def arrow(d, x1, y, x2):
    line(d, x1, y, x2-6, y, ACC, 2)
    d.polygon([((x2-6)*S,(y-4)*S),((x2)*S,y*S),((x2-6)*S,(y+4)*S)], fill=ACC)

def save(img, name):
    img.resize((img.width//S, img.height//S), Image.LANCZOS).save(f"{OUT}/{name}.png")
    print("wrote", name)

# 1) ARCHITECTURE ---------------------------------------------------------------
img, d = new(860, 340)
txt(d, 24, 20, "Zigbee Sniffer · Tethered — how it fits together", 17, FG, True)
rr(d, 24, 70, 180, 230, 10, PANEL, LINE)
txt(d, 114, 82, "YOUR ZIGBEE NETWORK", 10, DIM, True, "ma")
circle(d, 70, 140, 15, ACC); txt(d, 70, 140, "C", 9, BG, True, "mm")
txt(d, 70, 165, "coordinator", 9, DIM, anchor="ma")
circle(d, 150, 150, 10, NODE); circle(d, 120, 210, 8, LEAF)
circle(d, 70, 230, 10, NODE); circle(d, 160, 235, 8, LEAF)
line(d, 70,150,150,150,GOOD,1); line(d,70,150,70,220,FAIR,1)
line(d,70,230,120,210,GOOD,1); line(d,150,150,160,227,WEAK,1)
txt(d, 114, 282, "lights · sensors · routers", 9, DIM2, anchor="ma")
txt(d, 243, 176, "802.15.4", 10, DIM, anchor="ma"); txt(d, 243, 192, "RF 2.4GHz", 10, DIM, anchor="ma")
arrow(d, 214, 210, 272)
rr(d, 284, 120, 150, 120, 10, PANEL, LINE)
txt(d, 359, 138, "ESP32-C6", 11, DIM, True, "ma"); txt(d, 359, 158, "tethered firmware", 10, FG, anchor="ma")
txt(d, 359, 182, "capture · ED scan", 9, DIM2, anchor="ma"); txt(d, 359, 200, "active probe (TX)", 9, DIM2, anchor="ma")
txt(d, 470, 192, "USB", 10, DIM, anchor="ma"); arrow(d, 434, 210, 490)
rr(d, 502, 90, 200, 180, 10, PANEL, LINE)
txt(d, 602, 108, "zbsniff — Go host", 11, DIM, True, "ma")
for i,t in enumerate(["decode · decrypt (AES)","SQLite store","diagnostics · routing","REST + WebSocket"]):
    txt(d, 602, 130+i*17, t, 10, FG, anchor="ma")
txt(d, 602, 214, "runs on a PC, a Pi, or as a", 9, DIM2, anchor="ma")
txt(d, 602, 230, "Home Assistant add-on", 9, PUR, True, "ma")
txt(d, 738, 192, "http", 10, DIM, anchor="ma"); arrow(d, 702, 210, 732)
rr(d, 744, 150, 96, 120, 10, PANEL, LINE)
txt(d, 792, 170, "BROWSER", 11, DIM, True, "ma"); txt(d, 792, 194, "web UI", 10, FG, anchor="ma")
txt(d, 792, 218, "devices, routing,", 9, DIM2, anchor="ma"); txt(d, 792, 232, "spectrum…", 9, DIM2, anchor="ma")
txt(d, 24, 322, "Optional: 1–4 C6 dongles (roles) · Home Assistant for names + network LQI", 10, DIM2)
save(img, "architecture")

# 2) SIGNAL SOURCES -------------------------------------------------------------
img, d = new(860, 360)
txt(d, 24, 20, "Two very different signal readings — don't confuse them", 17, FG, True)
rr(d, 24, 60, 500, 280, 12, PANEL2, LINE)
rr(d, 60, 250, 70, 44, 8, PANEL, ACC, 1); txt(d, 95, 272, "sniffer", 11, ACC, True, "mm")
circle(d, 300, 120, 17, NODE); txt(d, 300, 120, "R", 9, BG, True, "mm"); txt(d, 300, 94, "router", 10, DIM, anchor="ma")
circle(d, 410, 150, 13, LEAF); txt(d, 410, 178, "device", 10, DIM, anchor="ma")
line(d, 396, 146, 316, 126, GOOD, 3); txt(d, 356, 122, "strong", 10, GOOD, True, "ma")
txt(d, 356, 180, "device↔router: close & strong", 9, GOOD, anchor="ma")
line(d, 120, 258, 398, 158, WEAK, 1.5, dash=True)
txt(d, 250, 230, "weak (far from sniffer)", 10, WEAK, True, "ma")
txt(d, 274, 320, "Same device — weak to the sniffer, yet healthy on the mesh.", 11, DIM, anchor="ma")
rr(d, 548, 60, 288, 280, 12, PANEL, LINE)
txt(d, 568, 78, "WHICH READING IS WHICH?", 11, DIM, True)
rows=[(112,ACC,"RSSI / Qual (sniffer)",["how loud the sniffer hears it —","depends where the sniffer sits."]),
      (176,GOOD,"Net LQI (Home Assistant)",["the coordinator's view of the","device's real link. The truth."]),
      (240,FAIR,"Active probe RSSI",["measured from a chosen radio's","location — move it near a device."])]
for cy,c,h,sub in rows:
    circle(d, 576, cy, 5, c); txt(d, 592, cy-8, h, 12, FG, True)
    txt(d, 592, cy+8, sub[0], 10, DIM); txt(d, 592, cy+24, sub[1], 10, DIM)
txt(d, 568, 318, "'Weak link' in Diagnostics is INFO — sniffer vantage,", 9, DIM2)
txt(d, 568, 332, "not a mesh problem.", 9, DIM2)
save(img, "signal-sources")

# 3) UI TABS --------------------------------------------------------------------
img, d = new(860, 430)
txt(d, 24, 18, "Zigbee Sniffer · Tethered", 15, FG, True)
rr(d, 230, 16, 150, 22, 11, "#0d2a16"); txt(d, 305, 27, "● receiving from device", 10, GOOD, anchor="ma")
rr(d, 392, 16, 120, 22, 11, "#0d2a16"); txt(d, 452, 27, "● capturing · ch 20", 10, GOOD, anchor="ma")
txt(d, 836, 27, "ch 20 · captured 4213 · devices 16", 10, DIM, anchor="ra")
line(d, 0, 52, 860, 52, LINE, 1)
tabs=[("Overview",20,86,True),("Live frames",112,94,False),("RF spectrum",212,96,False),
      ("Active testing",314,104,False),("Diagnostics",424,98,False),("Config",528,62,False),("About",596,60,False)]
for name,x,w,active in tabs:
    rr(d, x, 64, w, 30, 7, PANEL if active else "#0f141a", LINE, 1)
    txt(d, x+w/2, 79, name, 12, FG if active else DIM, True, "ma")
cards=[(20,112,"Overview",["Devices, the live routing tree,","and logged incidents."]),
       (300,112,"Live frames",["Capture controls + decoded","frames as they arrive."]),
       (580,112,"RF spectrum",["Energy per channel + waterfall,","Wi-Fi overlap, peak-hold."]),
       (20,212,"Active testing",["Probe a device (TX) to confirm","it's alive; schedule probes."]),
       (300,212,"Diagnostics",["Auto findings (syslog, filterable)","+ connection & logs."]),
       (580,212,"Config",["Theme, key, Home Assistant,","export, roles, firmware (OTA)."])]
for x,y,h,sub in cards:
    w = 270 if x < 580 else 260
    rr(d, x, y, w, 88, 8, PANEL, LINE)
    txt(d, x+14, y+16, h, 12, ACC, True); txt(d, x+14, y+40, sub[0], 11, DIM); txt(d, x+14, y+56, sub[1], 11, DIM)
rr(d, 20, 320, 820, 90, 8, PANEL2, LINE)
txt(d, 34, 336, "ALWAYS VISIBLE (top bar)", 12, DIM, True)
txt(d, 34, 358, "● receiving — host is getting data.   ● capturing · ch N — radio is actively sniffing.", 11, DIM)
txt(d, 34, 378, "Capture keeps running when you switch tabs — only Stop ends it.", 11, DIM)
save(img, "ui-tabs")

# 4) ROUTING TREE ---------------------------------------------------------------
img, d = new(860, 360)
txt(d, 24, 20, "Reading the routing tree", 17, FG, True)
rr(d, 24, 56, 540, 284, 12, PANEL2, LINE)
E=[(290,200,180,120,GOOD),(290,200,410,120,FAIR),(290,200,240,290,GOOD),
   (180,120,110,200,GOOD),(410,120,470,210,WEAK)]
for x1,y1,x2,y2,c in E: line(d,x1,y1,x2,y2,c,2)
line(d,410,120,470,70,"#3a4452",1.6,dash=True)
txt(d,230,150,"210",11,GOOD,True); txt(d,356,150,"120",11,FAIR,True); txt(d,452,168,"60",11,WEAK,True)
circle(d,290,200,12,ACC); txt(d,290,230,"coordinator",10,DIM,anchor="ma")
for cx,cy,r,c,lbl,ly in [(180,120,8,NODE,"Hallway",100),(410,120,8,NODE,"Family rm",100),
    (110,200,5,LEAF,"Motion",220),(240,290,5,LEAF,"Door",310),(470,210,5,LEAF,"Bulb",230),(470,70,5,WEAK,"Sun rm",54)]:
    circle(d,cx,cy,r,c); txt(d,cx,ly,lbl,10,"#aeb9c4",anchor="ma")
rr(d,584,56,252,284,12,PANEL,LINE)
txt(d,602,74,"LEGEND",12,DIM,True)
leg=[(112,ACC,"coordinator (center)",True),(140,NODE,"router (bigger dot)",True),
     (168,LEAF,"end device (small)",True),(196,WEAK,"isolated / weak",True)]
for cy,c,t,_ in leg:
    circle(d,612,cy,6,c); txt(d,630,cy-7,t,12,FG)
for cy,c,t in [(224,GOOD,"strong link"),(248,WEAK,"weak link")]:
    line(d,602,cy,624,cy,c,3); txt(d,632,cy-7,t,12,FG)
line(d,602,272,624,272,"#3a4452",2,dash=True); txt(d,632,265,"parent not yet seen",12,FG)
txt(d,602,300,"Numbers on links = Net LQI (toggle).",11,DIM2)
txt(d,602,318,"Scroll=zoom · drag=pan · isolate a device.",11,DIM2)
save(img, "routing-tree")
print("done")
