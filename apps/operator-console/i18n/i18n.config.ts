// Vue I18n base config, read by @nuxtjs/i18n at build time.
// Composition API mode (legacy:false) so pages use useI18n()/$t. ru is the fallback —
// a missing en key shows the Russian contract string, never a raw key id. This makes
// translation incremental: a half-translated en.json still renders a coherent console.

/**
 * Russian (Slavic) plural rule. Vue I18n's default rule is English-style (2 forms:
 * one | other) and picks the WRONG form for Russian counts — it cannot tell "1 секрет"
 * from "2 секрета" from "5 секретов". Russian has 3 grammatical forms (one/few/many) and
 * the rule keys off BOTH the last digit and the last two digits (the 11–14 "teen"
 * exception recurs every hundred: 11–14, 111–114, …). Maps a count to a slot index.
 *
 * NOTE: the canonical vue-i18n docs example tests teen as `n>10 && n<20`, which is wrong
 * for 111–114 (it returns "one/few" where Russian needs "many"). This uses `% 100` so it
 * is correct across all hundreds. Authority: Unicode CLDR plural rules for `ru` +
 * vue-i18n custom-plural mechanism (intlify/vue-i18n) — verified 2026-06-20.
 *
 * Supports two message shapes:
 *   4 slots:  "нет секретов | {n} секрет | {n} секрета | {n} секретов"  (zero|one|few|many)
 *   3 slots:  "{n} секрет | {n} секрета | {n} секретов"                  (one|few|many)
 */
function ruPluralRule(choice: number, choicesLength: number): number {
  const mod10 = choice % 10
  const mod100 = choice % 100
  const isOne = mod10 === 1 && mod100 !== 11
  const isFew = mod10 >= 2 && mod10 <= 4 && !(mod100 >= 12 && mod100 <= 14)
  if (choicesLength >= 4) {
    if (choice === 0) return 0
    return isOne ? 1 : isFew ? 2 : 3
  }
  // 3-slot form: one | few | many
  return isOne ? 0 : isFew ? 1 : 2
}

/**
 * Chinese plural rule. Chinese has ONE grammatical number form, so a 1-slot message
 * ("{n} 个密钥") is the norm and any count maps to slot 0. The catch: we still want an
 * optional zero form ("没有密钥 | {n} 个密钥") for empty-state copy — but Vue I18n's default
 * 2-slot rule is English (n=0 → slot 1 → "0 个密钥", not the zero form). This rule fixes
 * that: count 0 → first slot, every other count → last slot. 1-slot messages always → 0.
 *   2 slots: "没有密钥 | {n} 个密钥"   (zero | other)
 *   1 slot:  "{n} 个密钥"             (other only)
 */
function zhPluralRule(choice: number, choicesLength: number): number {
  if (choicesLength <= 1) return 0
  return choice === 0 ? 0 : choicesLength - 1
}

export default defineI18nConfig(() => ({
  legacy: false,
  fallbackLocale: 'ru',
  // Missing-key warnings are noise during a long translation pass; the fallback covers UX.
  missingWarn: false,
  fallbackWarn: false,
  // Per-locale plural rules. en uses the built-in 2-form rule; ru needs the Slavic one,
  // zh needs the one-form rule (with a zero-form escape) above.
  pluralRules: {
    ru: ruPluralRule,
    zh: zhPluralRule,
  },
}))
