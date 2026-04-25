---
version: alpha
name: Engram
description: Persistent memory infrastructure for AI coding agents — a trusted senior colleague that always knows the answer.
colors:
  primary: "#EE7410"
  primary-foreground: "#FFFFFF"
  secondary: "#71717A"
  secondary-foreground: "#FAFAFA"
  background: "#FAFAFA"
  foreground: "#09090B"
  card: "#FFFFFF"
  card-foreground: "#09090B"
  muted: "#F4F4F5"
  muted-foreground: "#71717A"
  destructive: "#EF4444"
  border: "#E4E4E7"
  accent: "#F4F4F5"
  accent-foreground: "#18181B"
  dark-background: "#09090B"
  dark-foreground: "#FAFAFA"
  dark-card: "#18181B"
  dark-card-foreground: "#FAFAFA"
  dark-muted: "#27272A"
  dark-muted-foreground: "#A1A1AA"
  dark-border: "#27272A"
typography:
  headline-lg:
    fontFamily: Inter
    fontSize: 36px
    fontWeight: 700
    lineHeight: 1.1
    letterSpacing: -0.025em
  headline-md:
    fontFamily: Inter
    fontSize: 24px
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: -0.02em
  body-md:
    fontFamily: Inter
    fontSize: 16px
    fontWeight: 400
    lineHeight: 1.5
  body-sm:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: 400
    lineHeight: 1.5
  label-md:
    fontFamily: Inter
    fontSize: 14px
    fontWeight: 500
    lineHeight: 1.4
  label-sm:
    fontFamily: Inter
    fontSize: 12px
    fontWeight: 500
    lineHeight: 1.4
    letterSpacing: 0.025em
  mono:
    fontFamily: JetBrains Mono
    fontSize: 14px
    fontWeight: 400
    lineHeight: 1.6
rounded:
  sm: 6px
  md: 8px
  lg: 12px
  xl: 16px
  full: 9999px
spacing:
  xs: 4px
  sm: 8px
  md: 16px
  lg: 24px
  xl: 32px
  2xl: 48px
---

# Engram Design System

## Overview

Engram is persistent memory infrastructure for AI coding agents. The visual identity
reflects a trusted senior colleague — understated, reliable, with hidden depth. The UI
prioritizes clarity over decoration, information density over whitespace waste, and
functional beauty over aesthetic indulgence.

The design supports both light and dark color modes. Light mode is the default, evoking
professionalism and openness. Dark mode is available for low-light environments, using
the same information hierarchy with inverted contrast.

The primary brand color (burnt orange #EE7410) is used sparingly — only for the single
most important action per screen and active navigation states. Everything else uses the
neutral zinc palette.

## Colors

The palette is built on zinc neutrals with a single warm accent.

- **Primary (#EE7410):** Burnt orange — used exclusively for primary actions, active
  states, and brand identity. Never used for backgrounds or large surfaces.
- **Secondary (#71717A):** Zinc-500 — for secondary text, metadata, captions, and
  borders. The workhorse neutral.
- **Background (#FAFAFA / dark: #09090B):** Near-white in light mode, near-black in dark.
  Clean and recessive — content stands forward.
- **Card (#FFFFFF / dark: #18181B):** Surface for elevated content. Subtle distinction
  from background creates visual layers without shadows.
- **Muted (#F4F4F5 / dark: #27272A):** Subdued backgrounds for secondary UI elements,
  hover states, code blocks.
- **Destructive (#EF4444):** Red for delete actions and error states only.

## Typography

Two typefaces serve complementary roles.

- **Inter** — the primary typeface for all UI text. Clean, neutral, optimized for screens.
  Used for headlines (Semi-Bold to Bold), body text (Regular), and labels (Medium).
- **JetBrains Mono** — for code snippets, terminal output, API responses, and technical
  identifiers (token prefixes, credential names, session IDs).

Headlines use negative letter-spacing for density. Labels use positive letter-spacing
for readability at small sizes.

## Layout

Content uses a responsive grid:
- Sidebar: 256px expanded, 48px collapsed (icon-only), Sheet on mobile (<768px)
- Main content: fluid, max-width 1280px, centered with 24px horizontal padding
- Cards: 8px border radius, 1px border (zinc-200 light / zinc-800 dark), 24px inner padding
- Stat cards: 2-column on mobile, 4-column on desktop

Spacing follows a 4px base unit: 4, 8, 16, 24, 32, 48.

## Components

All UI components derive from shadcn-vue (Radix Vue primitives). Custom components are
prohibited unless shadcn has no equivalent.

- **Buttons:** Primary (orange fill, white text), Secondary (zinc outline), Ghost (no border),
  Destructive (red fill). Small size for inline actions.
- **Cards:** 1px border, no shadow. Hover state: subtle border color shift.
- **Tables:** Striped rows in light mode, alternating zinc-900/zinc-950 in dark.
- **Badges:** Pill shape, muted background + colored text. Priority badges: critical=red,
  high=orange, medium=yellow, low=zinc.
- **Dialogs:** Centered modal with backdrop blur. Max-width 480px for forms.
- **Sidebar:** Fixed left, collapsible. Active item: orange text + orange left border.

## Do's and Don'ts

- Do use the primary color only for the single most important action per screen
- Do maintain WCAG AA contrast ratios (4.5:1 for normal text)
- Do use Inter for all UI text — never mix in other sans-serif fonts
- Do use consistent 8px border radius on cards and inputs
- Don't use shadows for elevation — use borders instead
- Don't use more than two font weights on a single screen section
- Don't use the primary orange for backgrounds or large surfaces
- Don't hardcode hex colors — always reference CSS variables
- Don't add decorative elements that don't convey information
