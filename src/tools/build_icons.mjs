// PWA のアイコンを作る。
//
// 依存を増やさずに済ませるため、RGBA のビットマップを組み立てて
// PNG に符号化する(zlib は Node に入っている)。
//
// 生成物は public/ に置いて追跡する。毎回のビルドで作り直さないのは、
// 同じ入力から同じ絵しか出ないうえ、ビルドの手順を増やしたくないため。
//
//   node src/tools/build_icons.mjs
//
// 絵柄は gkill 本体の配色に合わせた角丸の四角にタグの形。
// 記録の中身は一切含まない。

import { deflateSync } from 'node:zlib'
import { mkdirSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const repositoryRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const outputDir = join(repositoryRoot, 'public')

// gkill 本体の primary。
const BACKGROUND = [0x26, 0x72, 0xed, 0xff]
const FOREGROUND = [0xff, 0xff, 0xff, 0xff]

/** 32bit CRC。PNG のチャンクに要る。 */
function crc32(bytes) {
    let crc = 0xffffffff
    for (const byte of bytes) {
        crc ^= byte
        for (let bit = 0; bit < 8; bit++) {
            crc = crc & 1 ? (crc >>> 1) ^ 0xedb88320 : crc >>> 1
        }
    }
    return (crc ^ 0xffffffff) >>> 0
}

function chunk(type, data) {
    const typeAndData = Buffer.concat([Buffer.from(type, 'ascii'), data])
    const length = Buffer.alloc(4)
    length.writeUInt32BE(data.length)
    const crc = Buffer.alloc(4)
    crc.writeUInt32BE(crc32(typeAndData))
    return Buffer.concat([length, typeAndData, crc])
}

/** RGBA のビットマップを PNG にする。 */
function encodePng(width, height, pixels) {
    const header = Buffer.alloc(13)
    header.writeUInt32BE(width, 0)
    header.writeUInt32BE(height, 4)
    header[8] = 8 // ビット深度
    header[9] = 6 // カラータイプ: RGBA
    header[10] = 0 // 圧縮方式
    header[11] = 0 // フィルタ方式
    header[12] = 0 // インタレースなし

    // 各行の先頭にフィルタ種別(0 = なし)を置く。
    const raw = Buffer.alloc((width * 4 + 1) * height)
    for (let y = 0; y < height; y++) {
        const rowStart = y * (width * 4 + 1)
        raw[rowStart] = 0
        pixels.copy(raw, rowStart + 1, y * width * 4, (y + 1) * width * 4)
    }

    return Buffer.concat([
        Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
        chunk('IHDR', header),
        chunk('IDAT', deflateSync(raw, { level: 9 })),
        chunk('IEND', Buffer.alloc(0)),
    ])
}

/**
 * アイコンを1枚描く。
 *
 * maskable が真のときは、絵を内側 60% に収める。Android は
 * アイコンを円などに切り抜くので、端まで描くと欠ける。
 */
function drawIcon(size, maskable) {
    const pixels = Buffer.alloc(size * size * 4)
    const put = (x, y, color) => {
        const offset = (y * size + x) * 4
        pixels[offset] = color[0]
        pixels[offset + 1] = color[1]
        pixels[offset + 2] = color[2]
        pixels[offset + 3] = color[3]
    }

    const radius = maskable ? 0 : size * 0.22
    for (let y = 0; y < size; y++) {
        for (let x = 0; x < size; x++) {
            // 角丸の四角。maskable は全面を塗る(切り抜きは端末がやる)。
            const inCornerX = Math.min(x, size - 1 - x)
            const inCornerY = Math.min(y, size - 1 - y)
            let inside = true
            if (inCornerX < radius && inCornerY < radius) {
                const dx = radius - inCornerX
                const dy = radius - inCornerY
                inside = dx * dx + dy * dy <= radius * radius
            }
            put(x, y, inside ? BACKGROUND : [0, 0, 0, 0])
        }
    }

    // タグの形。中央に置いた菱形と、その中の穴。
    const scale = maskable ? 0.6 : 0.78
    const center = size / 2
    const half = (size * scale) / 2

    for (let y = 0; y < size; y++) {
        for (let x = 0; x < size; x++) {
            // 45度回した正方形 = 菱形。
            const dx = Math.abs(x - center)
            const dy = Math.abs(y - center)
            if (dx + dy > half) {
                continue
            }
            // 縁だけを描く(塗りつぶさず輪郭にする)。
            const thickness = size * 0.075
            if (dx + dy < half - thickness) {
                continue
            }
            put(x, y, FOREGROUND)
        }
    }

    // タグの穴。菱形の左上寄りに小さな丸。
    const holeRadius = size * 0.07
    const holeX = center - half * 0.42
    const holeY = center - half * 0.42
    for (let y = 0; y < size; y++) {
        for (let x = 0; x < size; x++) {
            const dx = x - holeX
            const dy = y - holeY
            if (dx * dx + dy * dy <= holeRadius * holeRadius) {
                put(x, y, FOREGROUND)
            }
        }
    }

    return encodePng(size, size, pixels)
}

mkdirSync(outputDir, { recursive: true })

const icons = [
    { name: 'pwa-192x192.png', size: 192, maskable: false },
    { name: 'pwa-512x512.png', size: 512, maskable: false },
    { name: 'pwa-maskable-512x512.png', size: 512, maskable: true },
    { name: 'apple-touch-icon.png', size: 180, maskable: false },
    { name: 'favicon.png', size: 64, maskable: false },
]

for (const icon of icons) {
    const png = drawIcon(icon.size, icon.maskable)
    writeFileSync(join(outputDir, icon.name), png)
    console.log(`${icon.name} (${png.length} bytes)`)
}
