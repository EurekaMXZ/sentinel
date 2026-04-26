import { Suspense } from "react";

import { NewAnalysis } from "@/components/new-analysis";

function NewAnalysisFallback() {
  return <div className="mx-auto w-full max-w-[1000px] px-4 py-10 sm:px-6" />;
}

export default function NewAnalysisPage() {
  return (
    <Suspense fallback={<NewAnalysisFallback />}>
      <NewAnalysis />
    </Suspense>
  );
}
