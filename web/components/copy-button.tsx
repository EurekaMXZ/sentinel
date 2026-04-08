"use client";

import { useState } from "react";
import { Button } from "@heroui/react";

export function CopyButton({
  value,
  label = "复制",
  className,
}: {
  value: string;
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      className={className}
      onPress={copy}
      aria-label={label}
    >
      {copied ? "已复制" : label}
    </Button>
  );
}
