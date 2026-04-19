"use client";

import Link from "next/link";
import { Button, Chip, Drawer, useOverlayState } from "@heroui/react";
import { Menu, ScanSearch } from "lucide-react";

import { TENANT_ID, shortId } from "@/lib/config";
import { SidebarContent } from "./app-sidebar";

export function MobileNav() {
  const state = useOverlayState();

  return (
    <header className="sticky top-0 z-40 flex h-14 items-center gap-3 border-b border-separator/60 bg-background/90 px-4 backdrop-blur-xl lg:hidden">
      <Drawer state={state}>
        <Button
          isIconOnly
          variant="ghost"
          size="sm"
          aria-label="打开导航"
          onPress={state.open}
        >
          <Menu size={18} />
        </Button>
        <Drawer.Backdrop isDismissable>
          <Drawer.Content placement="left" className="w-[280px] bg-background">
            <Drawer.Dialog>
              <Drawer.Header className="sr-only">
                <Drawer.Heading>导航</Drawer.Heading>
              </Drawer.Header>
              <Drawer.Body className="h-full p-0">
                <SidebarContent onNavigate={state.close} />
              </Drawer.Body>
            </Drawer.Dialog>
          </Drawer.Content>
        </Drawer.Backdrop>
      </Drawer>

      <Link href="/" className="flex items-center gap-2">
        <ScanSearch size={18} className="text-accent" />
        <span className="text-sm font-semibold">Sentinel</span>
      </Link>

      <div className="ml-auto">
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
    </header>
  );
}
