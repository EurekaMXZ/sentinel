import type { NextRequest } from "next/server";

const API_BASE = (process.env.SENTINEL_API_URL ?? "http://127.0.0.1:8080").replace(
  /\/+$/,
  "",
);

// This route deliberately lives outside /v1 so it cannot be intercepted by the
// /v1/:path* rewrite. Next.js rewrites buffer text/event-stream responses;
// returning the upstream ReadableStream here keeps chunks streaming.
export async function GET(
  request: NextRequest,
  context: { params: Promise<{ agentId: string }> },
) {
  const { agentId } = await context.params;
  const target = new URL(
    `/v1/agents/${encodeURIComponent(agentId)}/stream`,
    API_BASE,
  );
  target.search = new URL(request.url).search;

  const headers = new Headers();
  for (const name of ["accept", "last-event-id", "x-tenant-id", "cookie"]) {
    const value = request.headers.get(name);
    if (value) headers.set(name, value);
  }

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: "GET",
      headers,
      cache: "no-store",
      signal: request.signal,
    });
  } catch {
    return Response.json(
      {
        error: "failed to connect to the Agent stream",
        code: "stream_unavailable",
      },
      { status: 502 },
    );
  }

  if (!upstream.ok || !upstream.body) {
    return new Response(upstream.body, {
      status: upstream.status,
      headers: {
        "Content-Type":
          upstream.headers.get("content-type") ?? "application/json",
      },
    });
  }

  return new Response(upstream.body, {
    status: 200,
    headers: {
      "Content-Type": "text/event-stream",
      "Cache-Control": "no-cache, no-transform",
      Connection: "keep-alive",
      "X-Accel-Buffering": "no",
      "X-Sentinel-Stream-Proxy": "next-route",
    },
  });
}
