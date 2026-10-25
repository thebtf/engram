import { conversationText } from '../hooks/content.js';

export type ContextTier = 'FULL' | 'TARGETED' | 'MINIMAL' | 'NONE';

export interface TierConfig {
  /** How often to do a FULL search (every N turns). Default: 8. */
  fullSearchInterval: number;
  /** How often to do a MINIMAL search if no other trigger. Default: 4. */
  minimalInterval: number;
  /** Maximum prompt length (chars) to be classified as NONE. Default: 30. */
  shortPromptMaxChars: number;
}

export const DEFAULT_TIER_CONFIG: TierConfig = {
  fullSearchInterval: 8,
  minimalInterval: 4,
  shortPromptMaxChars: 30,
};

export interface TierResult {
  tier: ContextTier;
  tokenBudget: number;
  reason: string;
}

const TOKEN_BUDGETS: Record<ContextTier, number> = {
  FULL: 2000,
  TARGETED: 1000,
  MINIMAL: 500,
  NONE: 0,
};

const RECALL_PHRASES = [
  'remember',
  'recall',
  'what do you know about',
  'prior decision',
  'previous session',
  'what was decided',
  'engram',
  'memory',
];

const STOP_WORDS = new Set([
  'the', 'this', 'that', 'with', 'from', 'have', 'been',
  'will', 'would', 'could', 'should', 'about', 'into',
  'what', 'when', 'where', 'which', 'there', 'their',
  'also', 'just', 'more', 'some', 'than', 'them', 'then',
  'very', 'your', 'make', 'like', 'does', 'each', 'only',
  'need', 'want', 'please', 'can', 'are', 'for', 'and',
  'but', 'not', 'you', 'all', 'any', 'her', 'was', 'one',
  'our', 'out', 'has', 'had', 'how', 'its', 'may', 'new',
  'now', 'old', 'see', 'way', 'who', 'did', 'get', 'let',
  'say', 'she', 'too', 'use',
]);

function extractSignificantWords(text: string): Set<string> {
  return new Set(
    text
      .toLowerCase()
      .split(/\W+/)
      .filter((w) => w.length > 3 && !STOP_WORDS.has(w)),
  );
}

function jaccardSimilarity(a: Set<string>, b: Set<string>): number {
  if (a.size === 0 && b.size === 0) return 1;
  const intersection = new Set([...a].filter((w) => b.has(w)));
  const union = new Set([...a, ...b]);
  return union.size === 0 ? 1 : intersection.size / union.size;
}

function isTopicShift(current: string, previous: string): boolean {
  if (!previous) return false;
  const currentWords = extractSignificantWords(current);
  const previousWords = extractSignificantWords(previous);
  return jaccardSimilarity(currentWords, previousWords) < 0.3;
}

// Context-fill brackets: scale injection budget based on how full the context window is.
// Inspired by chuck's FRESH/MODERATE/DEPLETED/CRITICAL model.
export type ContextFill = 'FRESH' | 'MODERATE' | 'DEPLETED' | 'CRITICAL';

const FILL_MULTIPLIERS: Record<ContextFill, number> = {
  FRESH: 1.0,
  MODERATE: 0.7,
  DEPLETED: 0.4,
  CRITICAL: 0.1,
};

/** Estimate context fill level from message count and total character volume. */
export function estimateContextFill(messages?: unknown[]): ContextFill {
  if (!messages || messages.length === 0) return 'FRESH';

  let totalChars = 0;
  for (const m of messages) {
    if (typeof m === 'string') {
      totalChars += m.length;
    } else if (m && typeof m === 'object') {
      const obj = m as Record<string, unknown>;
      if (typeof obj.content === 'string') {
        totalChars += obj.content.length;
      } else {
        try {
          totalChars += JSON.stringify(m).length;
        } catch {
          // Cyclic structures or BigInt values throw — skip silently rather than crashing.
          totalChars += 0;
        }
      }
    }
  }

  // ~4 chars per token; typical context windows: 128K-200K tokens
  const estimatedTokens = totalChars / 4;
  if (estimatedTokens < 50_000) return 'FRESH';
  if (estimatedTokens < 100_000) return 'MODERATE';
  if (estimatedTokens < 150_000) return 'DEPLETED';
  return 'CRITICAL';
}

function makeTier(tier: ContextTier, reason: string, fillMultiplier = 1.0): TierResult {
  if (tier === 'NONE') {
    return { tier, tokenBudget: 0, reason };
  }
  return {
    tier,
    tokenBudget: Math.round(TOKEN_BUDGETS[tier] * fillMultiplier),
    reason,
  };
}

/** Host history is run-local; no plugin-global counter may couple different conversations. */
export function classifyTurn(prompt: string, messages: unknown[] = []): TierResult {
  const users = messages.filter(message => message && typeof message === 'object' && 'role' in message && message.role === 'user');
  const last = conversationText(users[users.length - 1]);
  const includesCurrent = last === prompt;
  const turnCount = users.length + (includesCurrent ? 0 : 1);
  const previous = conversationText(users[users.length - (includesCurrent ? 2 : 1)]);
  const fill = estimateContextFill(messages);
  const mult = FILL_MULTIPLIERS[fill];
  const suffix = mult < 1 ? ` [fill:${fill}]` : '';
  const tier = (value: ContextTier, reason: string) => makeTier(value, reason + suffix, mult);
  if (turnCount <= 1) return tier('FULL', 'first turn');
  if (RECALL_PHRASES.some(phrase => prompt.toLowerCase().includes(phrase))) return tier('FULL', 'explicit recall');
  if (prompt.length <= DEFAULT_TIER_CONFIG.shortPromptMaxChars && !prompt.includes('?')) return tier('NONE', 'short response');
  if (turnCount % DEFAULT_TIER_CONFIG.fullSearchInterval === 0) return tier('FULL', 'periodic full');
  if (turnCount % DEFAULT_TIER_CONFIG.minimalInterval === 0) return tier('MINIMAL', 'periodic minimal');
  if (isTopicShift(prompt, previous)) return tier('TARGETED', 'topic shift');
  return tier('NONE', 'no trigger');
}
