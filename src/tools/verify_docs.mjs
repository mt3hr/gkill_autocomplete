#!/usr/bin/env node
// 資料と実装のずれを検査する。
//
// 考え方は2つ。
//
//   1. 資料から数値を読み取るのではなく、コードから実測した数値で
//      「この語句が入っているはず」を組み立て、含まれているかを見る。
//      書式を自由にできて、正規表現の取りこぼしも起きない。
//
//   2. 実測と assertion の登録を分ける。数値を書いた資料を増やしたら
//      buildCountAssertions に1行足すだけで済む。
//
// 依存は入れない。Node の標準だけで動かす。
// 実行場所に依存しないよう、ルートはこのファイルからの相対で決める。

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const ROOT = path.resolve(HERE, '..', '..')

const errors = []
const warnings = []

const err = (message) => errors.push(message)
const warn = (message) => warnings.push(message)

const exists = (relativePath) => fs.existsSync(path.join(ROOT, relativePath))
const readText = (relativePath) => fs.readFileSync(path.join(ROOT, relativePath), 'utf8')

/** 指定した拡張子のファイルを再帰的に集める。 */
function collectFiles(relativeDir, filter) {
    const absoluteDir = path.join(ROOT, relativeDir)
    if (!fs.existsSync(absoluteDir)) {
        return []
    }

    const collected = []
    const walk = (current) => {
        for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
            const child = path.join(current, entry.name)
            if (entry.isDirectory()) {
                if (entry.name === 'node_modules' || entry.name === '.git') {
                    continue
                }
                walk(child)
                continue
            }
            if (filter(entry.name)) {
                collected.push(child)
            }
        }
    }
    walk(absoluteDir)
    return collected
}

/** コードから実測した数値。資料に書いてよいのはここに出るものだけ。 */
function computeMetrics() {
    const goFiles = collectFiles('src/autocomplete', (name) => name.endsWith('.go'))
    const goTestFiles = goFiles.filter((file) => file.endsWith('_test.go'))

    let goTests = 0
    for (const file of goTestFiles) {
        goTests += (fs.readFileSync(file, 'utf8').match(/^func Test/gm) ?? []).length
    }

    const internalDir = path.join(ROOT, 'src/autocomplete/internal')
    const internalPackages = fs.existsSync(internalDir)
        ? fs.readdirSync(internalDir, { withFileTypes: true }).filter((entry) => entry.isDirectory()).length
        : 0

    const clientFiles = collectFiles('src/client', (name) => name.endsWith('.ts') || name.endsWith('.vue'))
    const docFiles = collectFiles('documents/reverse', (name) => name.endsWith('.md'))

    return {
        goFiles: goFiles.length,
        goTestFiles: goTestFiles.length,
        goTests,
        internalPackages,
        clientFiles: clientFiles.length,
        docFiles: docFiles.length,
    }
}

/**
 * 「どの資料にどの語句があるべきか」の一覧。
 *
 * 数値を書いた資料を増やしたら、ここに1行足すこと。
 * 足し忘れると、その資料にだけ古い数値が残り続ける。
 */
function buildCountAssertions(metrics) {
    const assertions = []
    const add = (file, phrase) => assertions.push({ file, phrase })

    add('documents/reverse/folder-structure.md', `設計資料(${metrics.docFiles}ファイル)`)
    add(
        'documents/reverse/folder-structure.md',
        `Go ファイルは **${metrics.goFiles}ファイル**（うちテスト **${metrics.goTestFiles}ファイル**）`,
    )
    add('documents/reverse/folder-structure.md', `${metrics.internalPackages}パッケージ`)
    add('documents/reverse/folder-structure.md', `**${metrics.clientFiles}ファイル**`)

    add(
        'documents/reverse/testing-guide.md',
        `**Go のテスト宣言 ${metrics.goTests}件（${metrics.goTestFiles}ファイル）**`,
    )
    add('documents/reverse/testing-guide.md', `Go のソースは ${metrics.goFiles}ファイル`)

    return assertions
}

/** 検査対象の markdown。 */
function docMarkdownFiles() {
    const collected = collectFiles('documents', (name) => name.endsWith('.md'))
    for (const name of ['README.md', 'CLAUDE.md']) {
        if (exists(name)) {
            collected.push(path.join(ROOT, name))
        }
    }
    return collected
}

function checkCounts(metrics) {
    for (const { file, phrase } of buildCountAssertions(metrics)) {
        if (!exists(file)) {
            err(`件数検査: ファイルが存在しない: ${file}`)
            continue
        }
        if (!readText(file).includes(phrase)) {
            err(`件数ドリフト: ${file} に期待語句が見つからない → 「${phrase}」（実測に合わせて更新が必要）`)
        }
    }
}

function checkLinks() {
    const linkRe = /\]\(([^)]+)\)/g

    for (const absolute of docMarkdownFiles()) {
        const relative = path.relative(ROOT, absolute)
        const text = fs.readFileSync(absolute, 'utf8')
        const dir = path.dirname(absolute)

        for (const match of text.matchAll(linkRe)) {
            const target = match[1]
            if (/^(https?:)?\/\//.test(target) || target.startsWith('#') || target.startsWith('mailto:')) {
                continue
            }
            const hashIndex = target.indexOf('#')
            const filePart = hashIndex >= 0 ? target.slice(0, hashIndex) : target
            if (!filePart) {
                continue
            }
            if (!fs.existsSync(path.join(dir, filePart))) {
                err(`リンク切れ: ${relative} → ${target}`)
            }
        }
    }
}

function checkPaths() {
    // バッククォートで囲まれたトークンのうち、実在確認する価値があるものだけを見る。
    // 説明のために書いた一般的なパスを誤検出しないよう、条件を絞っている。
    const codeRe = /`([^`\n]+)`/g

    for (const absolute of docMarkdownFiles()) {
        const relative = path.relative(ROOT, absolute)
        const text = fs.readFileSync(absolute, 'utf8')

        for (const match of text.matchAll(codeRe)) {
            const token = match[1].trim()
            if (!/^src\/[\w./-]+$/.test(token)) {
                continue
            }
            if (token.includes('*')) {
                continue
            }
            const hasExtension = /\.[a-z]+$/.test(token)
            if (!hasExtension && !token.endsWith('/')) {
                continue
            }
            if (!exists(token)) {
                warn(`参照パス未検出: ${relative} → ${token}`)
            }
        }
    }
}

function checkMermaid() {
    const known = [
        'graph', 'flowchart', 'sequenceDiagram', 'classDiagram',
        'stateDiagram', 'stateDiagram-v2', 'erDiagram', 'journey',
        'gantt', 'pie', 'gitGraph', 'mindmap', 'timeline', 'quadrantChart',
    ]

    for (const absolute of docMarkdownFiles()) {
        const relative = path.relative(ROOT, absolute)
        const text = fs.readFileSync(absolute, 'utf8')

        for (const match of text.matchAll(/```mermaid\n([\s\S]*?)```/g)) {
            const body = match[1].trim()
            if (body === '') {
                err(`Mermaid: 空のブロック: ${relative}`)
                continue
            }
            const firstLine = body.split('\n')[0].trim()
            if (!known.some((kind) => firstLine.startsWith(kind))) {
                warn(`Mermaid: 図種別が不明: ${relative} → 「${firstLine}」`)
            }
        }
    }
}

/**
 * 資料に実在のタグ名やリポジトリ名が混ざっていないかの目安。
 *
 * 完全な検出はできないので、決めた架空の名前以外の「それらしい語」を
 * 見つけたら知らせるだけにとどめる。
 */
function checkFictionalExamples() {
    const allowed = new Set(['SampleRep', 'SampleRep_DeviceA_20200101', 'DeviceA', 'OtherRep'])

    for (const absolute of docMarkdownFiles()) {
        const relative = path.relative(ROOT, absolute)
        const text = fs.readFileSync(absolute, 'utf8')

        // 「種別_端末_日付」の形をしたリポジトリ名らしき語を拾う。
        for (const match of text.matchAll(/\b([A-Za-z][A-Za-z0-9]*_[A-Za-z0-9]+_\d{8})\b/g)) {
            if (!allowed.has(match[1])) {
                warn(`資料に実在のリポジトリ名が混ざっている可能性: ${relative} → ${match[1]}`)
            }
        }
    }
}

function main() {
    const metrics = computeMetrics()

    if (process.argv.includes('--list')) {
        console.log(JSON.stringify(metrics, null, 2))
        return
    }

    checkCounts(metrics)
    checkLinks()
    checkPaths()
    checkMermaid()
    checkFictionalExamples()

    for (const message of warnings) {
        console.warn(`警告: ${message}`)
    }
    for (const message of errors) {
        console.error(`エラー: ${message}`)
    }

    if (errors.length > 0) {
        console.error(`\n${errors.length}件の不整合があります。`)
        process.exit(1)
    }
    console.log(`資料の検査を通過しました（警告 ${warnings.length}件）。`)
}

main()
