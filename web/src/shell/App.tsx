/** The shell entry point: auth gate, plugin reconciliation, routing. */
import { useMemo } from "react";
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom";
import { EmptyState, Page, PageHeader } from "@cc/ui";
import type { PluginModule } from "@cc/ui";

import { plugins as compiledPlugins } from "../plugins";
import { Costs } from "../core/Costs";
import { Dashboard } from "../core/Dashboard";
import { Events } from "../core/Events";
import { Jobs } from "../core/Jobs";
import { Plugins } from "../core/Plugins";
import { Settings } from "../core/Settings";

import { FatalScreen, LoadingScreen, LoginScreen, SetupScreen } from "./Gate";
import { Layout } from "./Layout";
import { reconcile } from "./registry";
import { SessionProvider, useSession } from "./session";

export function App() {
  return (
    <SessionProvider>
      <Root />
    </SessionProvider>
  );
}

function Root() {
  const { state } = useSession();

  switch (state.phase) {
    case "loading":
      return <LoadingScreen />;
    case "setup":
      return <SetupScreen available={state.available} />;
    case "login":
      return <LoginScreen />;
    case "error":
      return <FatalScreen error={state.error} />;
    case "ready":
      return <Authenticated descriptors={state.bootstrap.plugins} />;
  }
}

function Authenticated({ descriptors }: { descriptors: { id: string; name: string; enabled: boolean }[] }) {
  // Fail closed before rendering any plugin UI.
  let plugins: PluginModule[];
  try {
    plugins = reconcile(compiledPlugins, descriptors);
  } catch (err) {
    return <FatalScreen error={err instanceof Error ? err : new Error(String(err))} />;
  }
  return <Shell plugins={plugins} descriptors={descriptors} />;
}

function Shell({
  plugins,
  descriptors,
}: {
  plugins: PluginModule[];
  descriptors: { id: string; name: string; enabled: boolean }[];
}) {
  const pluginRoutes = useMemo(
    () => plugins.flatMap((p) => p.routes.map((r) => ({ ...r, key: `${p.id}:${r.path}` }))),
    [plugins],
  );

  return (
    <BrowserRouter>
      <Routes>
        <Route element={<Layout plugins={plugins} descriptors={descriptors} />}>
          <Route index element={<Dashboard />} />
          <Route path="/plugins" element={<Plugins />} />
          <Route path="/jobs" element={<Jobs />} />
          <Route path="/events" element={<Events />} />
          <Route path="/costs" element={<Costs />} />
          <Route path="/settings" element={<Settings />} />
          {pluginRoutes.map((r) => (
            <Route key={r.key} path={r.path} element={r.element} />
          ))}
          <Route path="*" element={<NotFound />} />
        </Route>
        <Route path="/login" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  );
}

function NotFound() {
  return (
    <Page>
      <PageHeader title="Not found" />
      <EmptyState>No screen is registered at this path.</EmptyState>
    </Page>
  );
}
