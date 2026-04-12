import { marked } from 'marked'
import DOMPurify from 'dompurify'

// Configure marked for safe defaults
marked.use({
  breaks: true, // GFM line breaks
  gfm: true,    // GitHub Flavored Markdown
})

export function renderMarkdown(text: string): string {
  if (!text) return ''
  const html = marked.parseSync(text)
  return DOMPurify.sanitize(html)
}
