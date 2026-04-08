import { Chip } from "@heroui/react";

import { statusLabel, statusTone } from "@/lib/format";

export function StatusChip({
  status,
  size = "sm",
}: {
  status: string | undefined;
  size?: "sm" | "md" | "lg";
}) {
  return (
    <Chip color={statusTone(status)} variant="soft" size={size}>
      <Chip.Label>{statusLabel(status)}</Chip.Label>
    </Chip>
  );
}
