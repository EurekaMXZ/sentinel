import Link from "next/link";
import { Chip } from "@heroui/react";

import { TENANT_ID, shortId } from "@/lib/config";

function NavLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <Link
      href={href}
      className="rounded-lg px-3 py-1.5 text-sm text-muted transition-colors hover:bg-default/60 hover:text-foreground"
    >
      {children}
    </Link>
  );
}

export function AppShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col">
      <header className="sticky top-0 z-40 border-b border-separator/60 bg-background/85 backdrop-blur-xl">
        <div className="mx-auto flex h-14 w-full max-w-[1500px] items-center gap-6 px-4 sm:px-6">
          <Link href="/" className="flex items-center gap-2.5">
            <span className="grid size-8 place-items-center rounded-xl bg-accent text-sm font-semibold text-accent-foreground shadow-surface">
              S
            </span>
            <span className="leading-tight">
              <span className="block text-sm font-semibold tracking-tight">Sentinel</span>
              <span className="hidden text-[11px] text-muted sm:block">只读源码分析</span>
            </span>
          </Link>

          <nav className="flex items-center gap-1">
            <NavLink href="/">新建分析</NavLink>
            <NavLink href="/#recent">最近任务</NavLink>
          </nav>

          <div className="ml-auto flex items-center gap-2">
            <Chip
              size="sm"
              variant="soft"
              color="default"
              title={TENANT_ID}
              className="font-mono"
            >
              <Chip.Label>租户 {shortId(TENANT_ID)}</Chip.Label>
            </Chip>
          </div>
        </div>
      </header>

      <main className="flex-1">{children}</main>

      <footer className="border-t border-separator/60 px-4 py-5 text-center text-xs text-muted">
        Sentinel · 只读源码分析 Agent · 事件通过 SSE 回放
      </footer>
    </div>
  );
}
