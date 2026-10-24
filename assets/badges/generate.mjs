// assets/badges/generate.mjs — one-time badge pack generator (run: node generate.mjs)
// FUTURE AI: this script is a BUILD-TIME tool, not app code. It writes the
// committed PNGs in this folder using only node builtins (zlib). Re-run it
// if you ever need to restyle the pack — the PNGs are checked in, the app
// never runs this script. All art is clean-room: simple geometric shapes +
// a hand-drawn 5x7 pixel font. No trademarked logos are reproduced.
import { writeFileSync } from 'fs';
import { deflateSync } from 'zlib';

// ---------- CRC32 ----------
const CRC_T = (() => { const t = new Uint32Array(256); for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xEDB88320 ^ (c >>> 1) : c >>> 1; t[n] = c; } return t; })();
function crc32(b) { let c = 0xFFFFFFFF; for (let i = 0; i < b.length; i++) c = CRC_T[(c ^ b[i]) & 255] ^ (c >>> 8); return (c ^ 0xFFFFFFFF) >>> 0; }
function chunk(type, data) {
  const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type), data]);
  const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(td));
  return Buffer.concat([len, td, crc]);
}
function writePNG(path, w, h, px) { // px: Buffer w*h*4 RGBA
  const raw = Buffer.alloc(h * (1 + w * 4));
  for (let y = 0; y < h; y++) { raw[y * (1 + w * 4)] = 0; px.copy(raw, y * (1 + w * 4) + 1, y * w * 4, (y + 1) * w * 4); }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(w, 0); ihdr.writeUInt32BE(h, 4);
  ihdr[8] = 8; ihdr[9] = 6; // 8-bit RGBA
  const png = Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    chunk('IHDR', ihdr), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0)),
  ]);
  writeFileSync(path, png);
  console.log('wrote', path, `${w}x${h}`, png.length + ' bytes');
}

// ---------- canvas ----------
function makeCanvas(w, h) { return { w, h, px: Buffer.alloc(w * h * 4) }; }
function setPx(c, x, y, r, g, b, a) {
  x |= 0; y |= 0; if (x < 0 || y < 0 || x >= c.w || y >= c.h) return;
  const i = (y * c.w + x) * 4;
  // source-over blend onto transparent canvas
  const sa = a / 255, da = c.px[i + 3] / 255;
  const oa = sa + da * (1 - sa);
  if (oa === 0) { c.px.fill(0, i, i + 4); return; }
  c.px[i] = Math.round((r * sa + c.px[i] * da * (1 - sa)) / oa);
  c.px[i + 1] = Math.round((g * sa + c.px[i + 1] * da * (1 - sa)) / oa);
  c.px[i + 2] = Math.round((b * sa + c.px[i + 2] * da * (1 - sa)) / oa);
  c.px[i + 3] = Math.round(oa * 255);
}
function fillRect(c, x, y, w, h, col) { for (let j = y; j < y + h; j++) for (let i = x; i < x + w; i++) setPx(c, i, j, ...col); }
function hline(c, x0, x1, y, col) { for (let x = x0; x <= x1; x++) setPx(c, x, y, ...col); }
// even-odd scanline polygon fill
function fillPoly(c, pts, col) {
  let minY = Infinity, maxY = -Infinity;
  for (const [, y] of pts) { minY = Math.min(minY, y); maxY = Math.max(maxY, y); }
  for (let y = Math.floor(minY); y <= Math.ceil(maxY); y++) {
    const xs = [];
    for (let i = 0; i < pts.length; i++) {
      const [x1, y1] = pts[i], [x2, y2] = pts[(i + 1) % pts.length];
      if ((y1 <= y && y2 > y) || (y2 <= y && y1 > y)) xs.push(x1 + (y - y1) / (y2 - y1) * (x2 - x1));
    }
    xs.sort((a, b) => a - b);
    for (let i = 0; i + 1 < xs.length; i += 2) hline(c, Math.ceil(xs[i]), Math.floor(xs[i + 1]), y, col);
  }
}
function fillCircle(c, cx, cy, r, col) {
  for (let y = Math.floor(cy - r); y <= Math.ceil(cy + r); y++)
    for (let x = Math.floor(cx - r); x <= Math.ceil(cx + r); x++)
      if ((x - cx) ** 2 + (y - cy) ** 2 <= r * r) setPx(c, x, y, ...col);
}
function roundedRect(c, x, y, w, h, r, col) {
  fillRect(c, x + r, y, w - 2 * r, h, col);
  fillRect(c, x, y + r, w, h - 2 * r, col);
  for (const [cx, cy] of [[x + r, y + r], [x + w - r, y + r], [x + r, y + h - r], [x + w - r, y + h - r]]) fillCircle(c, cx, cy, r, col);
}

// ---------- 5x7 pixel font (clean-room, hand-drawn) ----------
const FONT = {
  'A': ['01110','10001','10001','11111','10001','10001','10001'],
  'B': ['11110','10001','10001','11110','10001','10001','11110'],
  'C': ['01110','10001','10000','10000','10000','10001','01110'],
  'D': ['11110','10001','10001','10001','10001','10001','11110'],
  'E': ['11111','10000','10000','11110','10000','10000','11111'],
  'F': ['11111','10000','10000','11110','10000','10000','10000'],
  'G': ['01110','10001','10000','10111','10001','10001','01111'],
  'H': ['10001','10001','10001','11111','10001','10001','10001'],
  'I': ['01110','00100','00100','00100','00100','00100','01110'],
  'J': ['00111','00010','00010','00010','00010','10010','01100'],
  'K': ['10001','10010','10100','11000','10100','10010','10001'],
  'L': ['10000','10000','10000','10000','10000','10000','11111'],
  'M': ['10001','11011','10101','10101','10001','10001','10001'],
  'N': ['10001','11001','10101','10011','10001','10001','10001'],
  'O': ['01110','10001','10001','10001','10001','10001','01110'],
  'P': ['11110','10001','10001','11110','10000','10000','10000'],
  'Q': ['01110','10001','10001','10001','10101','10010','01101'],
  'R': ['11110','10001','10001','11110','10100','10010','10001'],
  'S': ['01111','10000','10000','01110','00001','00001','11110'],
  'T': ['11111','00100','00100','00100','00100','00100','00100'],
  'U': ['10001','10001','10001','10001','10001','10001','01110'],
  'V': ['10001','10001','10001','10001','10001','01010','00100'],
  'W': ['10001','10001','10001','10101','10101','11011','10001'],
  'X': ['10001','10001','01010','00100','01010','10001','10001'],
  'Y': ['10001','10001','01010','00100','00100','00100','00100'],
  'Z': ['11111','00001','00010','00100','01000','10000','11111'],
  '0': ['01110','10001','10011','10101','11001','10001','01110'],
  '1': ['00100','01100','00100','00100','00100','00100','01110'],
  '2': ['01110','10001','00001','00110','01000','10000','11111'],
  '3': ['11111','00010','00100','00010','00001','10001','01110'],
  '4': ['00010','00110','01010','10010','11111','00010','00010'],
  '5': ['11111','10000','11110','00001','00001','10001','01110'],
  '6': ['00110','01000','10000','11110','10001','10001','01110'],
  '7': ['11111','00001','00010','00100','01000','01000','01000'],
  '8': ['01110','10001','10001','01110','10001','10001','01110'],
  '9': ['01110','10001','10001','01111','00001','00010','01100'],
  ' ': ['00000','00000','00000','00000','00000','00000','00000'],
  "'": ['00100','00100','00100','00000','00000','00000','00000'],
  '-': ['00000','00000','00000','11111','00000','00000','00000'],
  '.': ['00000','00000','00000','00000','00000','01100','01100'],
  ':': ['00000','01100','01100','00000','01100','01100','00000'],
};
function textWidth(s, scale) { return s.length * 6 * scale - scale; }
function drawText(c, s, x, y, scale, col) {
  s = String(s).toUpperCase();
  let cx = x;
  for (const ch of s) {
    const g = FONT[ch] || FONT[' '];
    for (let r = 0; r < 7; r++) for (let colI = 0; colI < 5; colI++)
      if (g[r][colI] === '1') fillRect(c, cx + colI * scale, y + r * scale, scale, scale, col);
    cx += 6 * scale;
  }
}
function drawTextCentered(c, s, y, scale, col) {
  drawText(c, s, Math.round((c.w - textWidth(s, scale)) / 2), y, scale, col);
}

// ---------- palette (tasteful, dark-media) ----------
const WHITE = [245, 242, 255, 255];
const GOLD = [232, 182, 42, 255];
const DARK = [16, 18, 30, 228];      // translucent dark badge body
const RED = [200, 46, 46, 255];
const AMBER = [232, 182, 42, 255];   // generic amber (not anyone's trademark yellow)
const BLACK = [10, 10, 12, 255];

// ---------- badge builders ----------
function starBadge() {
  const c = makeCanvas(220, 220), cx = 110, cy = 110, R = 100, r = 42;
  const pts = [];
  for (let i = 0; i < 10; i++) {
    const a = -Math.PI / 2 + i * Math.PI / 5, rad = i % 2 === 0 ? R : r;
    pts.push([cx + rad * Math.cos(a), cy + rad * Math.sin(a)]);
  }
  fillPoly(c, pts, GOLD);
  return c;
}
function textBadge(w, h, text, { bg = DARK, fg = WHITE, scale = 9, radius = 28, padX = 56 } = {}) {
  // Auto-widen so the text never clips: the canvas grows to fit.
  w = Math.max(w, textWidth(text, scale) + padX * 2);
  const c = makeCanvas(w, h);
  roundedRect(c, 0, 0, w, h, radius, bg);
  drawTextCentered(c, text, Math.round((h - 7 * scale) / 2), scale, fg);
  return c;
}
function imdbStyle() {
  const c = makeCanvas(380, 190);
  roundedRect(c, 0, 0, 380, 190, 30, AMBER);
  drawTextCentered(c, 'IMDB', Math.round((190 - 63) / 2), 9, BLACK);
  return c;
}
function tomatoStyle() {
  const c = makeCanvas(220, 220);
  fillCircle(c, 110, 118, 96, RED);
  fillCircle(c, 82, 88, 30, [235, 120, 120, 255]); // highlight
  fillRect(c, 104, 8, 12, 34, [46, 120, 46, 255]); // stem
  return c;
}
function popcornStyle() {
  const c = makeCanvas(220, 280);
  // striped box: alternate red/white vertical stripes, rounded
  for (let i = 0; i < 7; i++) fillRect(c, 10 + i * 30, 60, 30, 210, i % 2 ? WHITE : RED);
  roundedRect(c, 10, 60, 200, 210, 18, [0, 0, 0, 0]); // (no-op keep shape simple)
  fillCircle(c, 60, 50, 26, WHITE); fillCircle(c, 110, 42, 30, WHITE); fillCircle(c, 160, 50, 26, WHITE);
  return c;
}
function pillBlank() {
  const c = makeCanvas(320, 160);
  roundedRect(c, 0, 0, 320, 160, 80, DARK);
  return c;
}
function pillWide() {
  // Facts-v2 shell: wide enough for composed text like "4K • DV • HDR",
  // which drawComposite draws at 42% of badge height, shrink-free.
  const c = makeCanvas(560, 160);
  roundedRect(c, 0, 0, 560, 160, 80, DARK);
  return c;
}
function pillShort() {
  // Short pill (400x160) for one-word family text like "NEW" — wider
  // than the 320 blank so the runtime text keeps its padding.
  const c = makeCanvas(400, 160);
  roundedRect(c, 0, 0, 400, 160, 80, DARK);
  return c;
}
// ---------- Aurora style pack (2026-10-22) ----------
// A richer variant of the family shells at the EXACT same canvas sizes
// — geometry must not shift, so the art lives inside the same rect:
// the body is inset a few px to leave room for the soft shadow, which
// keeps the shape centered. Text is still drawn at composite time;
// aurora shells are text-free like the classic ones (the IMDb glyph
// keeps its baked lettering, as its classic counterpart does).
function mixColor(a, b, t) {
  return [
    Math.round(a[0] + (b[0] - a[0]) * t),
    Math.round(a[1] + (b[1] - a[1]) * t),
    Math.round(a[2] + (b[2] - a[2]) * t),
    Math.round((a[3] ?? 255) + ((b[3] ?? 255) - (a[3] ?? 255)) * t),
  ];
}
// Horizontal inset of a rounded rect's span on `row` (0-based): the
// corner circles eat into the span near the top/bottom edges.
function roundedRowInset(h, r, row) {
  let d = null;
  if (row < r) d = r - row;
  else if (row >= h - r) d = row - (h - r) + 1;
  if (d === null || d <= 0) return 0;
  return r - Math.sqrt(Math.max(0, r * r - d * d));
}
// Vertical gradient fill of a rounded rect: per-row color lerp.
function roundedRectGradient(c, x, y, w, h, r, topCol, botCol) {
  for (let row = 0; row < h; row++) {
    const t = h <= 1 ? 0 : row / (h - 1);
    const inset = roundedRowInset(h, r, row);
    hline(c, Math.ceil(x + inset), Math.floor(x + w - 1 - inset), y + row, mixColor(topCol, botCol, t));
  }
}
// The 2px lighter line along the top inside edge of a rounded rect.
function topHighlight(c, x, y, w, r, colTop, colSecond) {
  for (let row = 0; row < 2; row++) {
    const inset = roundedRowInset(w, r, row); // h unused for top rows
    hline(c, Math.ceil(x + inset), Math.floor(x + w - 1 - inset), y + row, row === 0 ? colTop : colSecond);
  }
}
// Soft drop shadow: three expanding, fading copies of the silhouette
// painted UNDER the body (largest + faintest first).
function softShadow(c, x, y, w, h, r) {
  roundedRect(c, x - 3, y + 5, w + 6, h + 2, r + 3, [8, 8, 14, 34]);
  roundedRect(c, x - 2, y + 4, w + 4, h + 1, r + 2, [8, 8, 14, 52]);
  roundedRect(c, x - 1, y + 3, w + 2, h, r + 1, [8, 8, 14, 72]);
}
// Scanline polygon fill with a vertical gradient (the star).
function fillPolyGradient(c, pts, topCol, botCol) {
  let minY = Infinity, maxY = -Infinity;
  for (const [, y] of pts) { minY = Math.min(minY, y); maxY = Math.max(maxY, y); }
  for (let y = Math.floor(minY); y <= Math.ceil(maxY); y++) {
    const xs = [];
    for (let i = 0; i < pts.length; i++) {
      const [x1, y1] = pts[i], [x2, y2] = pts[(i + 1) % pts.length];
      if ((y1 <= y && y2 > y) || (y2 <= y && y1 > y)) xs.push(x1 + (y - y1) / (y2 - y1) * (x2 - x1));
    }
    xs.sort((a, b) => a - b);
    const t = maxY > minY ? (y - minY) / (maxY - minY) : 0;
    const col = mixColor(topCol, botCol, t);
    for (let i = 0; i + 1 < xs.length; i += 2) hline(c, Math.ceil(xs[i]), Math.floor(xs[i + 1]), y, col);
  }
}
function drawPolyEdge(c, pts, col, widthPx = 3) {
  for (let i = 0; i < pts.length; i++) {
    const [x1, y1] = pts[i], [x2, y2] = pts[(i + 1) % pts.length];
    const steps = Math.max(1, Math.ceil(Math.hypot(x2 - x1, y2 - y1)));
    for (let s = 0; s <= steps; s++) {
      fillCircle(c, x1 + (x2 - x1) * s / steps, y1 + (y2 - y1) * s / steps, widthPx / 2, col);
    }
  }
}
// Diagonal two-tone fill (the ribbon bar): color drifts with x AND y.
function fillRectDiag(c, x, y, w, h, colA, colB) {
  for (let j = 0; j < h; j++) for (let i = 0; i < w; i++) {
    const t = (i / Math.max(1, w - 1) + j / Math.max(1, h - 1)) / 2;
    setPx(c, x + i, y + j, ...mixColor(colA, colB, t));
  }
}
// Radial-ish circle fill: bright near (hx,hy), deep at the rim.
function fillCircleRadial(c, cx, cy, r, hx, hy, colHi, colLo) {
  const maxD = r * 1.6;
  for (let y = Math.floor(cy - r); y <= Math.ceil(cy + r); y++)
    for (let x = Math.floor(cx - r); x <= Math.ceil(cx + r); x++) {
      if ((x - cx) ** 2 + (y - cy) ** 2 > r * r) continue;
      const t = Math.min(1, Math.hypot(x - hx, y - hy) / maxD);
      setPx(c, x, y, ...mixColor(colHi, colLo, t));
    }
}

// Per-family accent colors for the aurora pills' left edge bar.
const ACCENT = {
  resolution: [34, 211, 238, 255],   // cyan
  audio: [167, 139, 250, 255],       // violet
  edition: [244, 114, 182, 255],     // rose
  contentRating: [251, 191, 36, 255],// amber
  languages: [45, 212, 191, 255],    // teal
  source: [245, 158, 11, 255],       // amber
  details: [226, 232, 240, 255],     // pale slate
  subtitles: [56, 189, 248, 255],    // sky
  runtime: [147, 197, 253, 255],     // blue-slate (light blue)
  watchStatus: [250, 204, 21, 255],  // yellow
  newBadge: [52, 211, 153, 255],     // emerald
  decade: [192, 132, 252, 255],      // purple
  videoCodec: [129, 140, 248, 255],  // indigo
  streaming: [251, 146, 60, 255],    // orange
  studio: [168, 162, 158, 255],      // warm gray
  genre: [248, 113, 113, 255],       // red
  hdr: [253, 224, 71, 255],          // yellow — matches the signature accent
  channels: [74, 222, 128, 255],     // green
  aspectRatio: [240, 171, 252, 255], // fuchsia
  franchise: [251, 113, 133, 255],   // rose
  origin: [253, 186, 116, 255],      // light orange
  collection: [249, 168, 212, 255],  // pink
  family: [52, 211, 153, 255],       // emerald — matches the signature accent
  sagaPosition: [253, 164, 175, 255],// light rose — kin to franchise
  quickWatch: [45, 212, 191, 255],   // teal — matches the signature accent
  slate: [148, 163, 184, 255],       // versions + generic pill
};
function auroraPill(w, h, accent) {
  const c = makeCanvas(w, h);
  const bx = 10, by = 6, bw = w - 20, bh = h - 16, r = Math.floor(bh / 2);
  softShadow(c, bx, by, bw, bh, r);
  roundedRectGradient(c, bx, by, bw, bh, r, [41, 45, 71, 244], [15, 17, 30, 244]);
  topHighlight(c, bx, by, bw, r, [178, 188, 224, 170], [150, 160, 205, 90]);
  // 10px accent bar down the left inside edge (clear of the corners).
  roundedRect(c, bx + 16, by + 22, 10, bh - 44, 5, accent);
  return c;
}
function auroraRibbonShell() {
  // Same silhouette as ribbon('', …): bar + fold triangles, on a
  // 680x170 canvas, inset slightly so the shadow has somewhere to fall.
  const c = makeCanvas(680, 170);
  const barX = 46, barY = 6, barW = 588, barH = 156, cy = barY + barH / 2;
  const silhouette = (ox, oy, col) => {
    fillRect(c, barX + ox, barY + oy, barW, barH, col);
    fillPoly(c, [[barX + ox, barY + oy], [barX - 40 + ox, cy + oy], [barX + ox, barY + barH + oy]], col);
    fillPoly(c, [[barX + barW + ox, barY + oy], [barX + barW + 40 + ox, cy + oy], [barX + barW + ox, barY + barH + oy]], col);
  };
  silhouette(4, 7, [8, 8, 14, 70]);
  silhouette(2, 4, [8, 8, 14, 60]);
  const fold = [12, 10, 24, 255];
  fillPoly(c, [[barX, barY], [barX - 40, cy], [barX, barY + barH]], fold);
  fillPoly(c, [[barX + barW, barY], [barX + barW + 40, cy], [barX + barW, barY + barH]], fold);
  fillRectDiag(c, barX, barY, barW, barH, [56, 47, 98, 255], [23, 20, 42, 255]);
  hline(c, barX, barX + barW - 1, barY, [186, 176, 226, 180]);
  hline(c, barX, barX + barW - 1, barY + 1, [160, 150, 205, 90]);
  return c;
}
function starAurora() {
  const c = makeCanvas(220, 220), cx = 108, cy = 105, R = 95, r = 40;
  const pts = [];
  for (let i = 0; i < 10; i++) {
    const a = -Math.PI / 2 + i * Math.PI / 5, rad = i % 2 === 0 ? R : r;
    pts.push([cx + rad * Math.cos(a), cy + rad * Math.sin(a)]);
  }
  fillPoly(c, pts.map(([x, y]) => [x + 5, y + 7]), [8, 8, 14, 110]); // shadow
  fillPolyGradient(c, pts, [255, 216, 96, 255], [196, 138, 18, 255]);
  drawPolyEdge(c, pts, [146, 98, 8, 255], 3);
  return c;
}
function imdbAurora() {
  const c = makeCanvas(380, 190);
  const bx = 8, by = 5, bw = 364, bh = 176, r = 30;
  softShadow(c, bx, by, bw, bh, r);
  roundedRectGradient(c, bx, by, bw, bh, r, [255, 206, 66, 255], [206, 144, 12, 255]);
  topHighlight(c, bx, by, bw, r, [255, 240, 190, 210], [255, 228, 160, 110]);
  drawTextCentered(c, 'IMDB', Math.round((190 - 63) / 2), 9, BLACK);
  return c;
}
function tomatoAurora() {
  const c = makeCanvas(220, 220);
  fillCircle(c, 113, 121, 94, [8, 8, 14, 100]); // shadow
  fillCircleRadial(c, 108, 115, 94, 76, 82, [255, 122, 108, 255], [172, 26, 26, 255]);
  fillCircle(c, 78, 84, 26, [255, 178, 168, 150]); // gloss
  fillRect(c, 102, 6, 12, 34, [38, 110, 42, 255]); // stem
  return c;
}
function popcornAurora() {
  const c = makeCanvas(220, 280);
  roundedRect(c, 15, 67, 200, 210, 18, [8, 8, 14, 90]); // box shadow
  // Classic stripes, then shading: a bottom shade that deepens and a
  // top gloss that fades — the box gains depth without new shapes.
  for (let i = 0; i < 7; i++) fillRect(c, 10 + i * 30, 60, 30, 210, i % 2 ? WHITE : RED);
  for (let j = 0; j < 210; j++) {
    const t = j / 209;
    if (t > 0.55) hline(c, 10, 209, 60 + j, [20, 8, 8, Math.round(52 * (t - 0.55) / 0.45)]);
    if (j < 46) hline(c, 10, 209, 60 + j, [255, 255, 255, Math.round(52 * (1 - j / 46))]);
  }
  // Puffs get a cream under-shadow so they sit ON the box, not in it.
  for (const [x, y, r] of [[60, 50, 26], [110, 42, 30], [160, 50, 26]]) {
    fillCircle(c, x + 2, y + 4, r, [226, 208, 168, 255]);
    fillCircle(c, x, y, r, WHITE);
  }
  return c;
}

function ribbon(text, bg, scale = 9) {
  // Ribbon auto-widens to fit its text (folded ends drawn at the edges).
  const tw = text ? textWidth(text, scale) : 0;
  const barW = Math.max(600, tw + 120);
  const c = makeCanvas(barW + 80, 170);
  const barX = 40;
  fillRect(c, barX, 0, barW, 170, bg);
  const dk = [Math.round(bg[0] * 0.7), Math.round(bg[1] * 0.7), Math.round(bg[2] * 0.7), 255];
  fillPoly(c, [[barX, 0], [0, 85], [barX, 170]], dk);
  fillPoly(c, [[barX + barW, 0], [barX + barW + 40, 85], [barX + barW, 170]], dk);
  if (text) drawText(c, text, Math.round(barX + (barW - tw) / 2), Math.round((170 - 7 * scale) / 2), scale, WHITE);
  return c;
}

const BADGES = [
  ['star.png', starBadge(), 'rating', 'Star (rating shell — value drawn at runtime)'],
  ['imdb.png', imdbStyle(), 'rating', 'IMDb-style badge (clean-room)'],
  ['tomato.png', tomatoStyle(), 'rating', 'Tomato-style badge (clean-room)'],
  ['popcorn.png', popcornStyle(), 'rating', 'Popcorn-style badge (clean-room)'],
  ['badge-4k.png', textBadge(300, 150, '4K'), 'tech', '4K text badge'],
  ['badge-hdr.png', textBadge(300, 150, 'HDR'), 'tech', 'HDR text badge'],
  ['badge-dolby-vision.png', textBadge(560, 150, 'DOLBY VISION', { scale: 8 }), 'tech', 'Dolby Vision text badge'],
  ['badge-atmos.png', textBadge(360, 150, 'ATMOS'), 'tech', 'Atmos text badge'],
  ['badge-dtsx.png', textBadge(360, 150, 'DTS:X'), 'tech', 'DTS:X text badge'],
  ['pill.png', pillBlank(), 'status', 'Unwatched-count pill (count drawn at runtime)'],
  ['ribbon-new.png', ribbon('NEW', RED), 'status', 'NEW ribbon'],
  ['ribbon-blank.png', ribbon('', DARK), 'ribbon', 'Blank ribbon (text drawn at runtime)'],
  ['ribbon-directors-cut.png', ribbon("DIRECTOR'S CUT", DARK), 'ribbon', "Director's Cut ribbon"],
  ['ribbon-4k-remux.png', ribbon('4K REMUX', DARK), 'ribbon', '4K Remux ribbon'],
  ['ribbon-extended.png', ribbon('EXTENDED', DARK), 'ribbon', 'Extended ribbon'],
  // Facts-v2 value shells (2026-10): empty art — the TEXT is the item's
  // own data, drawn at composite time by the matching rule's label.
  ['pill-rescombo.png', pillWide(), 'tech', 'Resolution combo (text drawn at runtime)'],
  ['pill-content-rating.png', pillBlank(), 'status', 'Content rating pill (text drawn at runtime)'],
  ['ribbon-status.png', ribbon('', DARK), 'ribbon', 'Show status ribbon (text drawn at runtime)'],
  ['pill-language.png', pillBlank(), 'status', 'Language pill — SUB/DUB/DUAL (text drawn at runtime)'],
  // Kometa-style FAMILY shells (2026-10-22): empty art like the facts-v2
  // shells — each family's evaluator composes the text per item.
  ['pill-audio.png', pillWide(), 'tech', 'Audio pill — best-format text drawn at runtime (family)'],
  ['pill-edition.png', pillWide(), 'tech', 'Edition pill — edition title drawn at runtime (family)'],
  ['pill-versions.png', pillBlank(), 'tech', 'Versions pill — "N Versions" drawn at runtime (family)'],
  ['pill-source.png', pillWide(), 'tech', 'Source pill — release source drawn at runtime (family)'],
  ['pill-details.png', pillWide(), 'status', 'Details pill — year/runtime or seasons/episodes drawn at runtime (family)'],
  ['pill-runtime.png', pillWide(), 'status', 'Runtime pill — runtime drawn at runtime (family)'],
  ['pill-subtitles.png', pillBlank(), 'status', 'Subtitles pill — subtitle languages/count drawn at runtime (family)'],
  // Badge-pack families (2026-10): watch status, new, decade, codec,
  // streaming, studio, genre — empty shells, text drawn at runtime.
  ['pill-watch-status.png', pillWide(), 'status', 'Watch status pill — UNWATCHED / N UNWATCHED drawn at runtime (family)'],
  ['pill-new.png', pillShort(), 'status', 'New pill — NEW drawn at runtime (family)'],
  ['pill-decade.png', pillBlank(), 'status', 'Decade pill — “1980s” drawn at runtime (family)'],
  ['pill-video-codec.png', pillWide(), 'tech', 'Video codec pill — HEVC / H.264 / AV1 drawn at runtime (family)'],
  ['pill-streaming.png', pillWide(), 'status', 'Streaming pill — provider names drawn at runtime (family)'],
  ['pill-studio.png', pillWide(), 'status', 'Studio pill — studio/network name drawn at runtime (family)'],
  ['pill-genre.png', pillWide(), 'status', 'Genre pill — genre tags drawn at runtime (family)'],
  // Badge-library families (v1.11): HDR, channels, aspect ratio,
  // franchise, origin — empty shells, text drawn at runtime — plus
  // the Collection family's ribbon shell (banner in every style).
  ['pill-hdr.png', pillWide(), 'tech', 'HDR pill — DV / HDR10 / HLG drawn at runtime (family)'],
  ['pill-channels.png', pillBlank(), 'tech', 'Channels pill — 5.1 / 7.1 / 2.0 drawn at runtime (family)'],
  ['pill-aspect-ratio.png', pillBlank(), 'tech', 'Aspect ratio pill — 2.39:1 / 16:9 drawn at runtime (family)'],
  ['pill-franchise.png', pillWide(), 'status', 'Franchise pill — TMDb collection name drawn at runtime (family)'],
  ['pill-origin.png', pillBlank(), 'status', 'Origin pill — language · country drawn at runtime (family)'],
  ['ribbon-collection.png', ribbon('', DARK), 'ribbon', 'Collection ribbon — Plex collection name drawn at runtime (family)'],
  // Layman-pack families (v1.11.1): family ribbon, saga position,
  // quick watch — plus the genre family's banner shell (its
  // presentation choice alongside pill-genre). Empty art, text
  // drawn at runtime, same as every family shell.
  ['ribbon-family.png', ribbon('', DARK), 'ribbon', 'Family ribbon — FAMILY drawn at runtime (family)'],
  ['ribbon-leaving.png', ribbon('', DARK), 'ribbon', 'Leaving ribbon — LEAVING SOON / LEAVING ON date drawn at runtime (family)'],
  ['pill-saga-position.png', pillWide(), 'status', 'Saga position pill — “MOVIE 2 OF 4” drawn at runtime (family)'],
  ['pill-quick-watch.png', pillWide(), 'status', 'Quick watch pill — “UNDER 90 MIN” drawn at runtime (family)'],
  ['ribbon-genre.png', ribbon('', DARK), 'ribbon', 'Genre ribbon — genre tags drawn at runtime (family banner presentation)'],
  // Aurora style pack (2026-10-22): richer variants of the family
  // shells + rating glyphs at the SAME canvas sizes as classic.
  ['pill-rescombo-aurora.png', auroraPill(560, 160, ACCENT.resolution), 'tech', 'Resolution combo — Aurora (text drawn at runtime)'],
  ['pill-audio-aurora.png', auroraPill(560, 160, ACCENT.audio), 'tech', 'Audio pill — Aurora (text drawn at runtime)'],
  ['pill-edition-aurora.png', auroraPill(560, 160, ACCENT.edition), 'tech', 'Edition pill — Aurora (text drawn at runtime)'],
  ['pill-content-rating-aurora.png', auroraPill(320, 160, ACCENT.contentRating), 'status', 'Content rating pill — Aurora (text drawn at runtime)'],
  ['pill-language-aurora.png', auroraPill(320, 160, ACCENT.languages), 'status', 'Language pill — Aurora (text drawn at runtime)'],
  ['pill-versions-aurora.png', auroraPill(320, 160, ACCENT.slate), 'tech', 'Versions pill — Aurora (text drawn at runtime)'],
  ['pill-source-aurora.png', auroraPill(560, 160, ACCENT.source), 'tech', 'Source pill — Aurora (text drawn at runtime)'],
  ['pill-details-aurora.png', auroraPill(560, 160, ACCENT.details), 'status', 'Details pill — Aurora (text drawn at runtime)'],
  ['pill-runtime-aurora.png', auroraPill(560, 160, ACCENT.runtime), 'status', 'Runtime pill — Aurora (text drawn at runtime)'],
  ['pill-subtitles-aurora.png', auroraPill(320, 160, ACCENT.subtitles), 'status', 'Subtitles pill — Aurora (text drawn at runtime)'],
  ['pill-watch-status-aurora.png', auroraPill(560, 160, ACCENT.watchStatus), 'status', 'Watch status pill — Aurora (text drawn at runtime)'],
  ['pill-new-aurora.png', auroraPill(400, 160, ACCENT.newBadge), 'status', 'New pill — Aurora (text drawn at runtime)'],
  ['pill-decade-aurora.png', auroraPill(320, 160, ACCENT.decade), 'status', 'Decade pill — Aurora (text drawn at runtime)'],
  ['pill-video-codec-aurora.png', auroraPill(560, 160, ACCENT.videoCodec), 'tech', 'Video codec pill — Aurora (text drawn at runtime)'],
  ['pill-streaming-aurora.png', auroraPill(560, 160, ACCENT.streaming), 'status', 'Streaming pill — Aurora (text drawn at runtime)'],
  ['pill-studio-aurora.png', auroraPill(560, 160, ACCENT.studio), 'status', 'Studio pill — Aurora (text drawn at runtime)'],
  ['pill-genre-aurora.png', auroraPill(560, 160, ACCENT.genre), 'status', 'Genre pill — Aurora (text drawn at runtime)'],
  ['pill-aurora.png', auroraPill(320, 160, ACCENT.slate), 'status', 'Pill — Aurora (value drawn at runtime)'],
  ['ribbon-status-aurora.png', auroraRibbonShell(), 'ribbon', 'Show status ribbon — Aurora (text drawn at runtime)'],
  ['ribbon-blank-aurora.png', auroraRibbonShell(), 'ribbon', 'Blank ribbon — Aurora (text drawn at runtime)'],
  // Badge-library families (v1.11) — Aurora twins at the SAME
  // canvas sizes as their classic shells.
  ['pill-hdr-aurora.png', auroraPill(560, 160, ACCENT.hdr), 'tech', 'HDR pill — Aurora (text drawn at runtime)'],
  ['pill-channels-aurora.png', auroraPill(320, 160, ACCENT.channels), 'tech', 'Channels pill — Aurora (text drawn at runtime)'],
  ['pill-aspect-ratio-aurora.png', auroraPill(320, 160, ACCENT.aspectRatio), 'tech', 'Aspect ratio pill — Aurora (text drawn at runtime)'],
  ['pill-franchise-aurora.png', auroraPill(560, 160, ACCENT.franchise), 'status', 'Franchise pill — Aurora (text drawn at runtime)'],
  ['pill-origin-aurora.png', auroraPill(320, 160, ACCENT.origin), 'status', 'Origin pill — Aurora (text drawn at runtime)'],
  ['ribbon-collection-aurora.png', auroraRibbonShell(), 'ribbon', 'Collection ribbon — Aurora (text drawn at runtime)'],
  // Layman-pack families (v1.11.1) — Aurora twins at the SAME
  // canvas sizes as their classic shells.
  ['ribbon-family-aurora.png', auroraRibbonShell(), 'ribbon', 'Family ribbon — Aurora (text drawn at runtime)'],
  ['ribbon-leaving-aurora.png', auroraRibbonShell(), 'ribbon', 'Leaving ribbon — Aurora (text drawn at runtime)'],
  ['pill-saga-position-aurora.png', auroraPill(560, 160, ACCENT.sagaPosition), 'status', 'Saga position pill — Aurora (text drawn at runtime)'],
  ['pill-quick-watch-aurora.png', auroraPill(560, 160, ACCENT.quickWatch), 'status', 'Quick watch pill — Aurora (text drawn at runtime)'],
  ['ribbon-genre-aurora.png', auroraRibbonShell(), 'ribbon', 'Genre ribbon — Aurora (text drawn at runtime)'],
  ['star-aurora.png', starAurora(), 'rating', 'Star — Aurora (rating shell — value drawn at runtime)'],
  ['imdb-aurora.png', imdbAurora(), 'rating', 'IMDb-style badge — Aurora (clean-room)'],
  ['tomato-aurora.png', tomatoAurora(), 'rating', 'Tomato-style badge — Aurora (clean-room)'],
  ['popcorn-aurora.png', popcornAurora(), 'rating', 'Popcorn-style badge — Aurora (clean-room)'],
];

import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
const dir = dirname(fileURLToPath(import.meta.url));
const manifest = [];
for (const [file, canvas, category, label] of BADGES) {
  writePNG(join(dir, file), canvas.w, canvas.h, canvas.px);
  manifest.push({ id: file.replace('.png', ''), file, label, category, w: canvas.w, h: canvas.h });
}
writeFileSync(join(dir, 'badges.json'), JSON.stringify({ name: 'Default pack (clean-room)', badges: manifest }, null, 2) + '\n');
console.log('wrote badges.json with', manifest.length, 'badges');
