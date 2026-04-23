import { Streamdown } from "streamdown";

export function AnalysisReport({ markdown }: { markdown?: string }) {
  if (!markdown) return null;

  return (
    <section className="mt-5">
      <h2 className="mb-2 text-sm font-semibold">分析报告</h2>
      <div className="rounded-xl border border-separator/60 bg-surface/30 p-5">
        <Streamdown
          mode="static"
          animated={false}
          className="streamdown max-w-none text-sm leading-6 text-foreground/90"
        >
          {markdown}
        </Streamdown>
      </div>
    </section>
  );
}
