/** Whether a release such as "v1.19.33" is at least `minimum`, such as "1.19.32". */
export function atLeast(version: string, minimum: string): boolean {
  const have = parse(version);
  const need = parse(minimum);
  for (const [i, n] of need.entries()) {
    const h = have[i] ?? 0;
    if (h !== n) return h > n;
  }
  return true;
}

function parse(version: string): number[] {
  return (version.match(/\d+/g) ?? []).slice(0, 3).map(Number);
}
