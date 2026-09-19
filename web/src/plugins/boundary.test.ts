/**
 * The frontend half of the plugin boundary.
 *
 * `internal/core/pluginhost/boundary_test.go` already rejects a Go plugin that reaches
 * into core, the application module, or another plugin. The same rule governs plugin UI
 * -- a plugin may import `@cc/ui` and its own directory, nothing else -- but until now
 * only a comment in `ui/types.ts` said so, which meant a breach would land as a review
 * miss rather than a red test.
 *
 * A plugin that imports core is no longer liftable: it cannot be extracted, and core can
 * no longer be refactored without breaking it. That is worth a failing test, including
 * for test files -- a shared test harness couples a plugin just as firmly as shared
 * production code does.
 */
import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const pluginsDir = dirname(fileURLToPath(import.meta.url));
const srcDir = resolve(pluginsDir, "..");

/** Every source file under one plugin's directory, tests included. */
function sourcesIn(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) out.push(...sourcesIn(full));
    else if (/\.(ts|tsx)$/.test(entry)) out.push(full);
  }
  return out;
}

function pluginDirs(): { id: string; dir: string }[] {
  return readdirSync(pluginsDir)
    .filter((e) => statSync(join(pluginsDir, e)).isDirectory())
    .map((id) => ({ id, dir: join(pluginsDir, id) }));
}

/** Every module specifier the file imports, static and dynamic. */
function importsOf(source: string): string[] {
  const specifiers: string[] = [];
  const patterns = [
    /(?:^|\n)\s*import\s[^;]*?from\s*["']([^"']+)["']/g,
    /(?:^|\n)\s*import\s*["']([^"']+)["']/g,
    /(?:^|\n)\s*export\s[^;]*?from\s*["']([^"']+)["']/g,
    /\bimport\s*\(\s*["']([^"']+)["']\s*\)/g,
    /\brequire\s*\(\s*["']([^"']+)["']\s*\)/g,
  ];
  for (const re of patterns) {
    for (const m of source.matchAll(re)) if (m[1]) specifiers.push(m[1]);
  }
  return specifiers;
}

describe("plugin boundary", () => {
  const plugins = pluginDirs();

  it("finds the plugins to check", () => {
    // A silently empty walk would make every assertion below vacuous.
    expect(plugins.length).toBeGreaterThan(0);
  });

  it.each(plugins.map((p) => [p.id, p.dir] as const))(
    "%s imports only @cc/ui and its own directory",
    (id, dir) => {
      const violations: string[] = [];

      for (const file of sourcesIn(dir)) {
        // A plugin test sits in the same directory, so it is held to the same rule plus
        // the test tooling every test needs. Production code gets no such allowance.
        const isTest = /\.(test|spec)\.tsx?$/.test(file);
        const allowed = new Set(["@cc/ui", "react"]);
        if (isTest) {
          allowed.add("react-dom/client");
          allowed.add("react-router-dom");
          allowed.add("vitest");
        }

        for (const spec of importsOf(readFileSync(file, "utf8"))) {
          const where = `${relative(srcDir, file).replace(/\\/g, "/")} -> ${spec}`;

          // Bare specifiers: an allowlist, so a new shared surface has to be added
          // deliberately rather than slipping in under a broad `@cc/` prefix.
          if (!spec.startsWith(".")) {
            if (allowed.has(spec)) continue;
            violations.push(
              `${where} (allowed: ${[...allowed].sort().join(", ")})`,
            );
            continue;
          }

          // Relative: it must resolve back inside this plugin's own directory.
          const target = resolve(dirname(file), spec);
          if (target !== dir && !target.startsWith(dir + "\\") && !target.startsWith(dir + "/")) {
            violations.push(where);
          }
        }
      }

      expect(violations, `${id} reaches outside its own directory`).toEqual([]);
    },
  );
});
