import { marked, type Tokens } from 'marked'
import DOMPurify from 'dompurify'
import { codeToHtml } from 'shiki'
import { transformerNotationDiff } from '@shikijs/transformers'

// Configure marked
marked.use({
  breaks: true,
  gfm: true,
  async: true,
})

// Language aliases for common shorthand
const langAliases: Record<string, string> = {
  sh: 'bash', shell: 'bash', zsh: 'bash',
  js: 'javascript', ts: 'typescript',
  py: 'python', rb: 'ruby',
  yml: 'yaml', dockerfile: 'docker',
  console: 'bash', terminal: 'bash',
}

// Languages shiki supports (subset we care about)
const supportedLangs = new Set([
  'javascript', 'typescript', 'python', 'go', 'rust', 'bash', 'shell',
  'json', 'yaml', 'toml', 'html', 'css', 'vue', 'sql', 'docker',
  'markdown', 'diff', 'ini', 'xml', 'c', 'cpp', 'java', 'ruby',
  'php', 'swift', 'kotlin', 'lua', 'powershell', 'graphql', 'nginx',
])

function resolveLanguage(lang: string | undefined): string | null {
  if (!lang) return null
  const lower = lang.toLowerCase().trim()
  const resolved = langAliases[lower] || lower
  return supportedLangs.has(resolved) ? resolved : null
}

// Custom renderer for code blocks with shiki
const renderer = new marked.Renderer()

// We'll collect code blocks and highlight them async after marked sync pass
interface PendingHighlight {
  id: string
  code: string
  lang: string | null
}

let pendingHighlights: PendingHighlight[] = []
let highlightCounter = 0

renderer.code = function (this: unknown, token: Tokens.Code): string {
  const { text, lang } = token
  const resolvedLang = resolveLanguage(lang)

  // Diff detection: if lang is 'diff' or content has +/- line prefixes
  const isDiff = lang === 'diff' || (!lang && looksLikeDiff(text))

  if (resolvedLang || isDiff) {
    const id = `shiki-${++highlightCounter}`
    pendingHighlights.push({
      id,
      code: text,
      lang: isDiff ? 'diff' : resolvedLang,
    })
    // Placeholder replaced after async shiki pass
    return `<div id="${id}" class="shiki-placeholder"><pre><code>${escapeHtml(text)}</code></pre></div>`
  }

  // Fallback: plain code block
  return `<pre class="code-block"><code>${escapeHtml(text)}</code></pre>`
}

marked.use({ renderer })

function looksLikeDiff(text: string): boolean {
  const lines = text.split('\n')
  let diffLines = 0
  for (const line of lines) {
    if (/^[+-](?![-+]{2})/.test(line) || /^@@/.test(line)) diffLines++
  }
  return diffLines >= 2 && diffLines / lines.length > 0.3
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}

/**
 * Auto-detect terminal output and wrap in fenced code blocks.
 * Only for content that doesn't already have fencing.
 */
function preProcessCodeBlocks(text: string): string {
  // If text already has fenced code blocks, only process unfenced regions
  if (text.includes('```')) return processUnfencedRegions(text)

  return processLines(text.split('\n'))
}

/**
 * Split text into fenced (pass-through) and unfenced (auto-detect) regions.
 */
function processUnfencedRegions(text: string): string {
  const lines = text.split('\n')
  const result: string[] = []
  let unfencedBuffer: string[] = []
  let inFence = false

  for (const line of lines) {
    if (/^```/.test(line.trim())) {
      if (!inFence) {
        // Flush unfenced buffer before entering fence
        if (unfencedBuffer.length > 0) {
          result.push(processLines(unfencedBuffer))
          unfencedBuffer = []
        }
        inFence = true
        result.push(line)
      } else {
        inFence = false
        result.push(line)
      }
    } else if (inFence) {
      result.push(line)
    } else {
      unfencedBuffer.push(line)
    }
  }

  // Flush remaining unfenced
  if (unfencedBuffer.length > 0) {
    result.push(processLines(unfencedBuffer))
  }

  return result.join('\n')
}

function processLines(lines: string[]): string {
  const result: string[] = []
  let codeBuffer: string[] = []

  const isCodeLine = (line: string, bufferLen: number): boolean => {
    const trimmed = line.trimStart()
    if (trimmed === '') return bufferLen > 0
    if (/^[/$#>]/.test(trimmed)) return true
    if (/^\(.*\)\s*(PS\s+)?[A-Z]:\\.*>/i.test(trimmed)) return true
    if (/^PS\s+[A-Z]:\\.*>/i.test(trimmed)) return true
    if (/^(warning|error|Error|mesh-ctl|fatal|WARN|ERR):/i.test(trimmed)) return true
    if (/^(inet6?|link\/|valid_lft|mtu |scope |brd )/.test(trimmed)) return true
    if (/^\d+:\s+\S+@?\S*:?\s/.test(trimmed)) return true
    if (/^[a-z][\w-]+\s+(master|endpoint|client|server)\s+\d/i.test(trimmed)) return true
    if (/^[A-Z_]{3,}(\s+[A-Z_]{2,}){2,}/.test(trimmed)) return true
    if (/^(\t|    )/.test(line) && !/^(\t|    )[-*+\d]/.test(line)) return true
    if (/^\w[\w_-]*:\s*".+"$/.test(trimmed)) return true
    if (bufferLen > 0 && /^\w[\w_-]*:\s*\S/.test(trimmed)) return true
    if (/^\d+\.\d+\.\d+\.\d+\/\d+\s+dev\s+/.test(trimmed)) return true
    return false
  }

  function flushCode() {
    if (codeBuffer.length >= 2) {
      result.push('```')
      result.push(...codeBuffer)
      result.push('```')
    } else {
      result.push(...codeBuffer)
    }
    codeBuffer = []
  }

  for (const line of lines) {
    if (isCodeLine(line, codeBuffer.length)) {
      codeBuffer.push(line)
    } else {
      if (codeBuffer.length > 0) flushCode()
      result.push(line)
    }
  }
  if (codeBuffer.length > 0) flushCode()

  return result.join('\n')
}

/**
 * Render markdown with syntax highlighting.
 * Returns HTML string with shiki-highlighted code blocks.
 */
export async function renderMarkdownAsync(text: string): Promise<string> {
  if (!text) return ''

  pendingHighlights = []
  highlightCounter = 0

  const processed = preProcessCodeBlocks(text)
  let html = await marked.parse(processed)

  // Replace placeholders with shiki-highlighted code
  for (const { id, code, lang } of pendingHighlights) {
    try {
      const highlighted = await codeToHtml(code, {
        lang: lang || 'text',
        themes: {
          light: 'github-light',
          dark: 'github-dark',
        },
        defaultColor: false,
        transformers: [
          transformerNotationDiff(),
        ],
      })
      html = html.replace(
        `<div id="${id}" class="shiki-placeholder"><pre><code>${escapeHtml(code)}</code></pre></div>`,
        `<div class="shiki-block">${highlighted}</div>`
      )
    } catch {
      // Shiki failed for this block — keep fallback
    }
  }

  return DOMPurify.sanitize(html, {
    ADD_TAGS: ['span'],
    ADD_ATTR: ['style', 'class'],
  })
}

/**
 * Sync fallback (no highlighting) — for places that can't await.
 */
export function renderMarkdown(text: string): string {
  if (!text) return ''
  const processed = preProcessCodeBlocks(text)
  const html = marked.parse(processed) as string
  return DOMPurify.sanitize(html)
}
