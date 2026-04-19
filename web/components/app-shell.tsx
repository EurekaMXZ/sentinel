import { Suspense } from "react";

import { AppSidebar } from "./app-sidebar";
import { MobileNav } from "./mobile-nav";

function SidebarFallback() {
  return (
    <aside className="sticky top-0 hidden h-dvh w-[248px] shrink-0 border-r border-separator/60 bg-surface/20 p-4 lg:block">
      <div className="h-9 w-36 rounded-xl bg-default/60" />
      <div className="mt-6 h-10 w-full rounded-xl bg-default/60" />
      <div className="mt-6 space-y-2">
        <div className="h-3 w-20 rounded bg-default/50" />
        <div className="h-10 w-full rounded-lg bg-default/40" />
        <div className="h-10 w-full rounded-lg bg-default/40" />
        <div className="h-10 w-full rounded-lg bg-default/40" />
      </div>
    </aside>
  );
}

function MobileFallback() {
  return <header className="h-14 border-b border-separator/60 lg:hidden" />;
}

export function AppShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh bg-background">
      <Suspense fallback={<SidebarFallback />}>
        <AppSidebar />
      </Suspense>
      <div className="flex min-w-0 flex-1 flex-col">
        <Suspense fallback={<MobileFallback />}>
          <MobileNav />
        </Suspense>
        <main className="min-w-0 flex-1">{children}</main>
      </div>
    </div>
  );
}
