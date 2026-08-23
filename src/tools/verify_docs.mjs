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
        skills: skillNames().length,
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

    // AI 向け資料層。規約スキルの本数は CLAUDE.md が書いている。
    add('CLAUDE.md', `（${metrics.skills}スキル）`)

    return assertions
}

/** 検査対象の markdown。 */
function docMarkdownFiles() {
    const collected = collectFiles('documents', (name) => name.endsWith('.md'))
    for (const name of ['README.md', 'AGENTS.md', 'CLAUDE.md']) {
        if (exists(name)) {
            collected.push(path.join(ROOT, name))
        }
    }
    // 規約スキル（領域別の不変条件の正本）。リンク・ファイル名・個人情報の検査対象に載せる。
    collected.push(...collectFiles('.claude/skills', (name) => name.endsWith('.md')))
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

// ─────────────────────────────────────────────────────────────
// AI 向け資料層（AGENTS.md / 規約スキル / 他AI入口）
//
// gkill 本体の src/tools/verify_docs.mjs から移植した。
// ADR とマニュアルの検査は移植していない（どちらもこのリポジトリに無く、
// 実体の無い検査は空回りするため）。
// ─────────────────────────────────────────────────────────────
const SKILLS_DIR = '.claude/skills'
// LF 正規化後の実測 + 余裕。上限は「気付かせる装置」であって「許容量」ではない。
// **当たったら上限を上げるのではなく、中身をスキルへ落とすこと。**
const AGENTS_MD_MAX_BYTES = 22000
const CLAUDE_MD_MAX_LINES = 40
const ENTRYPOINTS = ['.github/copilot-instructions.md', '.cursor/rules/autocomplete.mdc']

const normalizeLF = (text) => text.replace(/\r\n/g, '\n')

function skillNames() {
    if (!exists(SKILLS_DIR)) {
        return []
    }
    return fs.readdirSync(path.join(ROOT, SKILLS_DIR))
        .filter((name) => exists(`${SKILLS_DIR}/${name}/SKILL.md`))
        .sort()
}

function checkAgentEntrypoints() {
    if (!exists('AGENTS.md')) {
        err('AGENTS.md が無い（AI エージェント共通の入口）')
        return
    }
    const agents = normalizeLF(readText('AGENTS.md'))
    const bytes = Buffer.byteLength(agents, 'utf8')
    if (bytes > AGENTS_MD_MAX_BYTES) {
        err(`AGENTS.md が ${bytes} バイト（上限 ${AGENTS_MD_MAX_BYTES}）。`
            + '上限を上げるのではなく、領域別の内容を .claude/skills/ へ移してルーティング表に載せること')
    }
    if (!agents.includes('<!-- ROUTING-TABLE:BEGIN')) {
        err('AGENTS.md にルーティング表のマーカーが無い')
    }

    if (!exists('CLAUDE.md')) {
        err('CLAUDE.md が無い（Claude Code の入口）')
        return
    }
    const claude = normalizeLF(readText('CLAUDE.md'))
    if (!/^@AGENTS\.md\s*$/m.test(claude)) {
        err('CLAUDE.md に `@AGENTS.md` の行が無い（Claude Code に AGENTS.md が読み込まれない）')
    }
    const claudeLines = claude.split('\n').length
    if (claudeLines > CLAUDE_MD_MAX_LINES) {
        err(`CLAUDE.md が ${claudeLines} 行（上限 ${CLAUDE_MD_MAX_LINES}）。`
            + '規約の正本は AGENTS.md と .claude/skills/。CLAUDE.md は入口だけを持つこと')
    }

    // 他AIツールの入口は導線だけを持つ。規約本文の複製は必ずドリフトする
    for (const relative of ENTRYPOINTS) {
        if (!exists(relative)) {
            err(`AI 入口ファイルが無い: ${relative}`)
            continue
        }
        const pointer = normalizeLF(readText(relative))
        if (!pointer.includes('AGENTS.md')) {
            err(`AI 入口が AGENTS.md を指していない: ${relative}`)
        }
        if (Buffer.byteLength(pointer, 'utf8') > 4096) {
            err(`AI 入口が大きすぎる: ${relative}（導線だけにする）`)
        }
        if (/してはいけない|してはならない/.test(pointer)) {
            err(`AI 入口に規約本文が書かれている: ${relative}（正本は AGENTS.md と .claude/skills/）`)
        }
    }
    const geminiPath = '.gemini/settings.json'
    if (!exists(geminiPath)) {
        err(`AI 入口ファイルが無い: ${geminiPath}`)
        return
    }
    let gemini = null
    try {
        gemini = JSON.parse(readText(geminiPath))
    } catch {
        err(`${geminiPath} が JSON として読めない`)
    }
    if (gemini && !(gemini.contextFileName ?? []).includes('AGENTS.md')) {
        err(`${geminiPath} の contextFileName に AGENTS.md が無い（Gemini CLI が規約を読まない）`)
    }
}

function checkSkills() {
    const names = skillNames()
    if (names.length === 0) {
        err(`規約スキルが1つも見つからない: ${SKILLS_DIR}/*/SKILL.md`
            + '（.gitignore が /.claude/* + !/.claude/skills/ になっているか確認。'
            + 'ディレクトリごと無視すると新しいクローンに存在せず、検査が静かにゼロ件になる）')
        return
    }
    const agents = exists('AGENTS.md') ? normalizeLF(readText('AGENTS.md')) : ''
    const tableMatch = agents.match(/<!-- ROUTING-TABLE:BEGIN[\s\S]*?<!-- ROUTING-TABLE:END -->/)
    const table = tableMatch ? tableMatch[0] : ''
    const linked = new Set()
    for (const match of table.matchAll(/\]\((\.claude\/skills\/[\w-]+\/SKILL\.md)\)/g)) {
        linked.add(match[1])
    }

    for (const name of names) {
        const relative = `${SKILLS_DIR}/${name}/SKILL.md`
        const text = normalizeLF(readText(relative))
        const frontMatter = text.match(/^---\n([\s\S]*?)\n---\n/)
        if (!frontMatter) {
            err(`SKILL.md に frontmatter が無い: ${relative}`)
            continue
        }
        const nameLine = frontMatter[1].match(/^name:\s*(.+?)\s*$/m)
        if (!nameLine || nameLine[1] !== name) {
            err(`SKILL.md の name がディレクトリ名と違う: ${relative} → `
                + `「${nameLine ? nameLine[1] : '(無し)'}」`)
        }
        // description は二重引用符でくくった1物理行（オンデマンド発動の唯一の手がかり）
        const descriptionLine = frontMatter[1].match(/^description:\s*"(.+)"\s*$/m)
        if (!descriptionLine) {
            err(`SKILL.md の description が無いか、二重引用符1行の形式でない: ${relative}`)
        } else {
            const description = descriptionLine[1]
            if (description.length < 80) {
                err(`description が短すぎて発動精度が出ない: ${relative}（${description.length}字）`)
            }
            if (description.length > 1024) {
                err(`description が長すぎる（常時コンテキストを食う）: ${relative}（${description.length}字）`)
            }
            if (!/(src\/|\.claude\/|documents\/|package\.json|AGENTS\.md|CLAUDE\.md|\.(go|ts|vue|mjs)\b)/.test(description)) {
                err(`description に発動の手がかり（パスやファイル名）が無い: ${relative}`)
            }
        }
        if (!linked.has(relative)) {
            err(`スキルが AGENTS.md のルーティング表に無い: ${relative}`
                + '（表に行が無いスキルは、パス連動のスキル機構を持たないエージェントから永遠に読まれない）')
        }
        // 1スキル = SKILL.md 1ファイル。補助 .md の散在は「索引に載らず読まれない資料」になる
        for (const file of fs.readdirSync(path.join(ROOT, SKILLS_DIR, name))) {
            if (file.endsWith('.md') && file !== 'SKILL.md') {
                err(`スキルに SKILL.md 以外の .md がある: ${SKILLS_DIR}/${name}/${file}`
                    + '（1スキル=1ファイル。内容は SKILL.md へ）')
            }
        }
    }
    for (const linkedPath of linked) {
        const dirName = linkedPath.split('/')[2]
        if (!names.includes(dirName)) {
            err(`ルーティング表にあるスキルが実在しない: ${linkedPath}`)
        }
    }
}

// ソース内アンカーコメント（規約スキルへの参照）の実在検査。
// コメント内の参照は checkLinks に載らないので、スキルの改名・削除で静かに古びる。
function checkSkillAnchors() {
    const pattern = /\.claude\/skills\/[\w-]+\/SKILL\.md/g
    for (const absolute of collectFiles('src', (name) => /\.(go|ts|vue|mjs)$/.test(name))) {
        const relative = path.relative(ROOT, absolute).split(path.sep).join('/')
        for (const match of fs.readFileSync(absolute, 'utf8').matchAll(pattern)) {
            if (!exists(match[0])) {
                err(`ソースのアンカーコメントが指すスキルが実在しない: ${relative} → ${match[0]}`)
            }
        }
    }
}

// .gitignore がスキルを追跡できる形になっているか。
// 裸の `.claude/` へ戻してもローカルではファイルが残るので気づけず、
// 新しいクローンでだけ「スキルが1本も無い」状態になる。
function checkGitignoreSkills() {
    if (!exists('.gitignore')) {
        err('.gitignore が無い')
        return
    }
    const lines = normalizeLF(readText('.gitignore')).split('\n').map((line) => line.trim())
    if (lines.includes('.claude/') || lines.includes('/.claude') || lines.includes('.claude')) {
        err('.gitignore に裸の `.claude/` 行がある。'
            + 'git はネガティブパターンで親ディレクトリの除外を打ち消せないので、'
            + '`/.claude/*` と `!/.claude/skills/` の2行組にすること')
    }
    for (const needed of ['/.claude/*', '!/.claude/skills/']) {
        if (!lines.includes(needed)) {
            err(`.gitignore に \`${needed}\` の行が無い（規約スキルが追跡されない）`)
        }
    }
}

// 資料に載っているファイル名の実在。
// 件数だけを検査していると「数は合っているのに一覧は古い」が通り抜ける。
const DOC_FILENAME_EXTENSIONS = ['go', 'ts', 'vue', 'mjs']
const DOC_FILENAME_PLACEHOLDER = /^_|xxx|yyy|zzz/
// このリポジトリに実在しないが、名指しする必要がある外部のファイル名。
// gkill 本体のもの。増やすときは必ず理由を書くこと。
const EXTERNAL_FILENAMES = new Set([
    'handle_login.go',                        // gkill 本体。弾く順序を合わせる相手
    'mi_repository_sqlite3_impl.go',          // gkill 本体。5射影 UNION の出どころ
    'plugin_protocol.go',                     // gkill 本体
    'handle_get_kyous_mcp.go',                // gkill 本体。get_kyous_mcp のハンドラ
])

function collectRepositoryBasenames() {
    const names = new Set()
    const skip = new Set(['node_modules', '.git', 'dist', 'coverage', 'html', 'release'])
    const walk = (dir) => {
        for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
            if (entry.isDirectory()) {
                if (skip.has(entry.name)) {
                    continue
                }
                walk(path.join(dir, entry.name))
                continue
            }
            names.add(entry.name)
        }
    }
    walk(ROOT)
    return names
}

function checkDocFilenames() {
    const basenames = collectRepositoryBasenames()
    const pattern = new RegExp(
        `(?<![\\w./-])([A-Za-z0-9_][\\w.-]*\\.(?:${DOC_FILENAME_EXTENSIONS.join('|')}))(?![\\w-])`, 'g')
    const missing = new Map()
    for (const absolute of docMarkdownFiles()) {
        const relative = path.relative(ROOT, absolute).split(path.sep).join('/')
        for (const match of fs.readFileSync(absolute, 'utf8').matchAll(pattern)) {
            const name = match[1]
            if (DOC_FILENAME_PLACEHOLDER.test(name)) {
                continue
            }
            if (EXTERNAL_FILENAMES.has(name) || basenames.has(name)) {
                continue
            }
            if (!missing.has(name)) {
                missing.set(name, new Set())
            }
            missing.get(name).add(relative)
        }
    }
    for (const name of [...missing.keys()].sort()) {
        err(`資料に載っているファイルが実在しない: ${name}`
            + `（${[...missing.get(name)].sort().join(', ')}）`)
    }
}

// 資料に出てくる `npm run <name>` が package.json に実在するか。
function checkNpmScripts() {
    const scripts = new Set(Object.keys(JSON.parse(readText('package.json')).scripts ?? {}))
    const pattern = /npm run ([a-z][\w:-]*)/g
    const missing = new Map()
    for (const absolute of docMarkdownFiles()) {
        const relative = path.relative(ROOT, absolute).split(path.sep).join('/')
        for (const match of fs.readFileSync(absolute, 'utf8').matchAll(pattern)) {
            if (scripts.has(match[1])) {
                continue
            }
            if (!missing.has(match[1])) {
                missing.set(match[1], new Set())
            }
            missing.get(match[1]).add(relative)
        }
    }
    for (const name of [...missing.keys()].sort()) {
        err(`資料が指す npm スクリプトが無い: npm run ${name}`
            + `（${[...missing.get(name)].sort().join(', ')}）`)
    }
}

// 個人情報・実環境情報の混入検査（AGENTS.md「絶対に守ること（個人情報）」の機械化）。
// checkFictionalExamples が実在リポジトリ名を見るのに対し、こちらはパスとメールを見る。
function checkPersonalInfo() {
    const patterns = [
        [/[A-Za-z]:\\+Users\\+(?![〈<]|user(?:name)?\b)[A-Za-z0-9]/, 'Windows のユーザープロファイル実パス'],
        [/\/(?:home|Users)\/(?!user\/|〈|<)[a-z0-9_-]{3,}\//, 'ホームディレクトリの実パス'],
        [/[A-Za-z0-9._%+-]+@(?!example\.)[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}/, 'メールアドレス'],
    ]
    // メールアドレスの形をしているが個人のものではない書き方。
    // git@github.com は SSH の clone URL（git clone git@github.com:...）。
    const emailAllowlist = new Set(['git@github.com'])
    const ngFile = 'verify_docs_personal_ngwords.local.txt'
    const ngWords = exists(ngFile)
        ? normalizeLF(readText(ngFile)).split('\n').map((word) => word.trim()).filter(Boolean)
        : []
    for (const absolute of docMarkdownFiles()) {
        const relative = path.relative(ROOT, absolute).split(path.sep).join('/')
        const text = normalizeLF(fs.readFileSync(absolute, 'utf8'))
        for (const [pattern, label] of patterns) {
            for (const match of text.matchAll(new RegExp(pattern, 'g'))) {
                if (emailAllowlist.has(match[0])) {
                    continue
                }
                err(`個人情報の疑い（${label}）: ${relative} → 「${match[0].slice(0, 40)}」`
                    + '（$HOME や 〈ユーザー名〉 のプレースホルダに置き換えること）')
                break
            }
        }
        for (const word of ngWords) {
            if (text.includes(word)) {
                err(`個人情報の疑い（ローカル NG 語）: ${relative} に「${word}」`)
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
    checkDocFilenames()
    checkAgentEntrypoints()
    checkSkills()
    checkGitignoreSkills()
    checkNpmScripts()
    checkSkillAnchors()
    checkPersonalInfo()

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
