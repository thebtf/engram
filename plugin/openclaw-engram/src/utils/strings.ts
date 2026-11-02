/** Keep the existing UTF-16 code-unit budget without splitting a surrogate pair. */
export function truncateUtf16(value: string, maxUnits: number): string {
 if (value.length <= maxUnits) return value;
 let end = maxUnits;
 const last = value.charCodeAt(end - 1);
 const next = value.charCodeAt(end);
 if (last >= 0xD800 && last <= 0xDBFF && next >= 0xDC00 && next <= 0xDFFF) end--;
 return value.slice(0, end);
}
