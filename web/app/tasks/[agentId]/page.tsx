import { TaskView } from "@/components/task-view";

// The task route reads dynamic params and is intentionally rendered on demand.
export const instant = false;

export default async function TaskPage({
  params,
}: {
  params: Promise<{ agentId: string }>;
}) {
  const { agentId } = await params;
  return <TaskView agentId={agentId} />;
}
