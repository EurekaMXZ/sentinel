import { RepositoryView } from "@/components/repository-view";

export const instant = false;

export default async function RepositoryPage({
  params,
}: {
  params: Promise<{ repositoryId: string }>;
}) {
  const { repositoryId } = await params;
  return <RepositoryView repositoryId={repositoryId} />;
}
