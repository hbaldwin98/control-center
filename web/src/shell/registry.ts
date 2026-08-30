/**
 * Plugin registration checks.
 *
 * The build rejects duplicate frontend ids and route or navigation collisions, and every
 * plugin path must stay below `/<plugin-id>`. At startup, before rendering any plugin UI,
 * the shell compares each `PluginModule.id` against the authenticated backend descriptors
 * and fails closed on unknown, missing, or duplicate ids.
 */
import type { PluginDescriptor, PluginModule } from "@cc/ui";

export class PluginRegistrationError extends Error {
  readonly problems: string[];

  constructor(problems: string[]) {
    super(`plugin registration failed:\n- ${problems.join("\n- ")}`);
    this.name = "PluginRegistrationError";
    this.problems = problems;
  }
}

const ID_PATTERN = /^[a-z][a-z0-9_]*$/;

/** Static checks that need no server: ids, path ownership, and collisions. */
export function validateModules(modules: PluginModule[]): string[] {
  const problems: string[] = [];
  const seenIds = new Set<string>();
  const claimedRoutes = new Map<string, string>();
  const claimedNav = new Map<string, string>();

  for (const m of modules) {
    if (!ID_PATTERN.test(m.id)) {
      problems.push(`plugin id "${m.id}" must match ${ID_PATTERN.source}`);
    }
    if (seenIds.has(m.id)) {
      problems.push(`duplicate frontend plugin id "${m.id}"`);
      continue;
    }
    seenIds.add(m.id);

    for (const r of m.routes) {
      if (!ownsPath(m.id, r.path)) {
        problems.push(`plugin "${m.id}" declares route "${r.path}" outside /${m.id}`);
      }
      const owner = claimedRoutes.get(r.path);
      if (owner) {
        problems.push(`route "${r.path}" is claimed by both "${owner}" and "${m.id}"`);
      } else {
        claimedRoutes.set(r.path, m.id);
      }
    }

    for (const n of m.nav) {
      if (!ownsPath(m.id, n.path)) {
        problems.push(`plugin "${m.id}" declares nav "${n.path}" outside /${m.id}`);
      }
      const owner = claimedNav.get(n.path);
      if (owner) {
        problems.push(`nav path "${n.path}" is claimed by both "${owner}" and "${m.id}"`);
      } else {
        claimedNav.set(n.path, m.id);
      }
    }
  }
  return problems;
}

/** A path belongs to a plugin when it is exactly `/<id>` or a descendant of it. */
function ownsPath(id: string, path: string): boolean {
  return path === `/${id}` || path.startsWith(`/${id}/`);
}

/**
 * Reconciles the compiled-in modules with the backend's registered plugins. Any
 * disagreement is fatal: a frontend that renders a plugin the host does not know, or omits
 * one it does, is not a state worth guessing about.
 */
export function reconcile(
  modules: PluginModule[],
  descriptors: PluginDescriptor[],
): PluginModule[] {
  const problems = validateModules(modules);

  const backendIds = new Set<string>();
  for (const d of descriptors) {
    if (backendIds.has(d.id)) {
      problems.push(`backend reported plugin "${d.id}" more than once`);
    }
    backendIds.add(d.id);
  }

  const frontendIds = new Set(modules.map((m) => m.id));
  for (const id of frontendIds) {
    if (!backendIds.has(id)) {
      problems.push(`frontend plugin "${id}" is not registered on the backend`);
    }
  }
  for (const id of backendIds) {
    if (!frontendIds.has(id)) {
      problems.push(`backend plugin "${id}" has no frontend module`);
    }
  }

  if (problems.length > 0) throw new PluginRegistrationError(problems);
  return modules;
}
