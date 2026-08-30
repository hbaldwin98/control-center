/**
 * Dot-segment pattern matching, mirroring the server's grammar.
 *
 * The shell opens one stream and multiplexes patterns locally; clients never send
 * server-side subscription patterns. `*` matches exactly one segment and `**` matches zero
 * or more.
 */

const SEGMENT = /^[a-z][a-z0-9_]*$/;

export function isValidPattern(pattern: string): boolean {
  if (pattern === "") return false;
  return pattern
    .split(".")
    .every((s) => s === "*" || s === "**" || SEGMENT.test(s));
}

export function matchesPattern(pattern: string, eventType: string): boolean {
  return matchSegments(pattern.split("."), eventType.split("."));
}

function matchSegments(pat: string[], typ: string[]): boolean {
  let p = 0;
  let t = 0;

  while (p < pat.length) {
    const seg = pat[p];
    if (seg === "**") {
      const rest = pat.slice(p + 1);
      if (rest.length === 0) return true;
      // `**` matches zero or more segments: try every split.
      for (let i = t; i <= typ.length; i++) {
        if (matchSegments(rest, typ.slice(i))) return true;
      }
      return false;
    }
    if (seg === "*") {
      if (t >= typ.length) return false;
      p++;
      t++;
      continue;
    }
    if (t >= typ.length || typ[t] !== seg) return false;
    p++;
    t++;
  }
  return t === typ.length;
}
