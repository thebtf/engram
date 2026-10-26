import { z } from 'zod';

const stringArray = z.array(z.string());

// Public validate stops at the first synchronous invalid type; safeParse accumulates every item error.
// It still allocates an O(n) output array internally, so this is not a constant-memory input limit.
export const StringArraySchema = z.custom<string[]>(
  (value) => stringArray.validate(value),
  { error: 'Expected an array of strings' },
);
