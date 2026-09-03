// One-off: generate 16x16 colored dot PNG icons (green/yellow/red) for tray status.
// Pure Node (zlib + raw PNG encoding), no external deps. Run once: node gen-dot-icons.cjs
const zlib = require('zlib');
const fs = require('fs');
const path = require('path');

function crc32(buf) {
  let c = ~0;
  for (let i = 0; i < buf.length; i++) {
    c ^= buf[i];
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xEDB88320 & -(c & 1));
  }
  return (~c) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const typeBuf = Buffer.from(type, 'ascii');
  const crcBuf = Buffer.alloc(4);
  crcBuf.writeUInt32BE(crc32(Buffer.concat([typeBuf, data])), 0);
  return Buffer.concat([len, typeBuf, data, crcBuf]);
}

function makePng(size, drawFn) {
  const rowLen = size * 4 + 1;
  const raw = Buffer.alloc(rowLen * size);
  for (let y = 0; y < size; y++) {
    raw[y * rowLen] = 0; // filter: none
    for (let x = 0; x < size; x++) {
      const [r, g, b, a] = drawFn(x, y);
      const off = y * rowLen + 1 + x * 4;
      raw[off] = r; raw[off + 1] = g; raw[off + 2] = b; raw[off + 3] = a;
    }
  }
  const sig = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8;  // bit depth
  ihdr[9] = 6;  // color type: RGBA
  ihdr[10] = 0; ihdr[11] = 0; ihdr[12] = 0;
  const idat = zlib.deflateSync(raw, { level: 9 });
  return Buffer.concat([sig, chunk('IHDR', ihdr), chunk('IDAT', idat), chunk('IEND', Buffer.alloc(0))]);
}

function dotPng(color) {
  const [r, g, b] = color;
  const size = 16;
  const cx = (size - 1) / 2;
  const cy = (size - 1) / 2;
  const radius = 6.2;
  const ringRadius = 7.6;
  return makePng(size, (x, y) => {
    const dx = x - cx;
    const dy = y - cy;
    const dist = Math.sqrt(dx * dx + dy * dy);
    if (dist <= radius) return [r, g, b, 255];
    // subtle anti-alias ring
    if (dist <= ringRadius) {
      const t = (ringRadius - dist) / (ringRadius - radius);
      const a = Math.round(255 * t);
      return [r, g, b, a];
    }
    return [0, 0, 0, 0];
  });
}

const dir = __dirname;
const colors = {
  'dot-green.png': [34, 197, 94],
  'dot-yellow.png': [234, 179, 8],
  'dot-red.png': [239, 68, 68]
};

Object.keys(colors).forEach((name) => {
  const out = dotPng(colors[name]);
  fs.writeFileSync(path.join(dir, name), out);
  console.log('wrote', name, out.length, 'bytes');
});
