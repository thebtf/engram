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

export class TurnTracker {
  private turnCount = 0;
  private lastPrompt = '';
  private readonly config: TierConfig;

  constructor(config?: Partial<TierConfig>) {
    this.config = { ...DEFAULT_TIER_CONFIG, ...config };
  }

  /** Classify the current turn and return the appropriate tier.
   *  When messages are provided, the token budget is scaled by context fill level. */
  classify(prompt: string, messages?: unknown[]): TierResult {
    this.turnCount++;

    const fill = estimateContextFill(messages);
    const mult = FILL_MULTIPLIERS[fill];

    const lowerPrompt = prompt.toLowerCase();
    const hasRecallPhrase = RECALL_PHRASES.some((phrase) => lowerPrompt.includes(phrase));

    const suffix = mult < 1.0 ? ` [fill:${fill}]` : '';

    // Rule 1: First turn always gets full context.
    if (this.turnCount === 1) {
      this.lastPrompt = prompt;
      return makeTier('FULL', 'first turn' + suffix, mult);
    }

    // Rule 2: Explicit recall phrases trigger FULL.
    if (hasRecallPhrase) {
      this.lastPrompt = prompt;
      return makeTier('FULL', 'explicit recall' + suffix, mult);
    }

    // Rule 3: Short prompts without a question mark → NONE.
    if (prompt.length <= this.config.shortPromptMaxChars && !prompt.includes('?')) {
      this.lastPrompt = prompt;
      return makeTier('NONE', 'short response', mult);
    }

    // Rule 4: Periodic FULL.
    if (this.turnCount % this.config.fullSearchInterval === 0) {
      this.lastPrompt = prompt;
      return makeTier('FULL', 'periodic full' + suffix, mult);
    }

    // Rule 5: Periodic MINIMAL.
    if (this.turnCount % this.config.minimalInterval === 0) {
      this.lastPrompt = prompt;
      return makeTier('MINIMAL', 'periodic minimal' + suffix, mult);
    }

    // Rule 6: Topic shift → TARGETED.
    if (isTopicShift(prompt, this.lastPrompt)) {
      this.lastPrompt = prompt;
      return makeTier('TARGETED', 'topic shift' + suffix, mult);
    }

    // Default: no trigger.
    this.lastPrompt = prompt;
    return makeTier('NONE', 'no trigger', mult);
  }

  /** Reset tracker (e.g., on new session). */
  reset(): void {
    this.turnCount = 0;
    this.lastPrompt = '';
  }
}
