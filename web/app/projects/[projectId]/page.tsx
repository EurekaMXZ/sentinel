import { ProjectView } from "@/components/project-view";

export const instant = false;

export default async function ProjectPage({
  params,
}: {
  params: Promise<{ projectId: string }>;
}) {
  const { projectId } = await params;
  return <ProjectView projectId={projectId} />;
}
