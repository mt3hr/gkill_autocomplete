#!/usr/bin/env node
// クロスコンパイル用の薄い包み。
//
// GOOS / GOARCH は呼び出し側(package.json の cross-env)が渡す。
// ここは出力先を作ってビルドするだけ。

import fs from 'node:fs'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '..', '..')

const [, , platformName, binaryName] = process.argv
if (!platformName || !binaryName) {
    console.error('usage: build_go.mjs <platform-name> <binary-name>')
    process.exit(1)
}

const outputDir = path.join(ROOT, 'release', platformName)
fs.mkdirSync(outputDir, { recursive: true })

const outputPath = path.join(outputDir, binaryName)

// shell は使わない。引数のエスケープを介さないため。
// Windows では拡張子まで指定しないと実行ファイルを解決できない。
const goBin = process.platform === 'win32' ? 'go.exe' : 'go'

const result = spawnSync(
    goBin,
    ['build', '-trimpath', '-ldflags', '-s -w', '-o', outputPath, './cmd/gkill_autocomplete'],
    {
        cwd: path.join(ROOT, 'src', 'autocomplete'),
        stdio: 'inherit',
        env: process.env,
    },
)

if (result.status !== 0) {
    console.error(`${platformName} のビルドに失敗しました`)
    process.exit(result.status ?? 1)
}

console.log(`${platformName}: ${path.relative(ROOT, outputPath)}`)
