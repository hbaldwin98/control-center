/**
 * Renders a plugin-supplied dashboard surface behind an error boundary.
 *
 * The dashboard is the screen an operator looks at to decide whether to hit a kill switch,
 * so one plugin's broken tile must never blank it. A surface that throws is replaced by a
 * note naming the plugin; every other tile keeps rendering, and the host's own facts about
 * the failed plugin — state, spend, jobs — are host-rendered and still there.
 */
import { Component } from "react";
import type { ComponentType, ErrorInfo, ReactNode } from "react";
import { Hint } from "@cc/ui";
import type { PluginSurfaceProps } from "@cc/ui";

type BoundaryProps = {
  pluginId: string;
  children: ReactNode;
};

type BoundaryState = { error: Error | null };

class SurfaceBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { error: null };

  static getDerivedStateFromError(error: unknown): BoundaryState {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }

  componentDidCatch(error: unknown, info: ErrorInfo): void {
    console.error(`plugin surface "${this.props.pluginId}" failed`, error, info.componentStack);
  }

  render(): ReactNode {
    if (this.state.error) {
      return (
        <div className="cc-surface cc-surface--failed" role="status">
          This plugin&rsquo;s own view failed to render.
          <Hint>{this.state.error.message}</Hint>
        </div>
      );
    }
    return this.props.children;
  }
}

/** Mounts `surface` for `pluginId`, or renders nothing when the plugin supplies none. */
export function PluginSurface({
  pluginId,
  enabled,
  surface,
}: {
  pluginId: string;
  enabled: boolean;
  surface: ComponentType<PluginSurfaceProps> | undefined;
}) {
  if (!surface) return null;
  const Surface = surface;
  return (
    <SurfaceBoundary pluginId={pluginId}>
      <div className="cc-surface">
        <Surface pluginId={pluginId} enabled={enabled} />
      </div>
    </SurfaceBoundary>
  );
}
