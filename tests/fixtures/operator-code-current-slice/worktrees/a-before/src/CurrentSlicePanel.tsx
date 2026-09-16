import { readCurrentSliceAlias as renderCurrentSlice } from "./relay"

export function CurrentSlicePanel(): string {
  return renderCurrentSlice()
}
