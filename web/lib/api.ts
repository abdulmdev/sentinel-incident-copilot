// Browser-side helper: everything goes through the BFF proxy, so no token handling here.
export class ApiError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

export async function api<T>(path: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const res = await fetch(`/api/proxy${path}`, {
    method: init?.method ?? "GET",
    body: init?.body === undefined ? undefined : JSON.stringify(init.body),
    headers: { "Content-Type": "application/json" },
    cache: "no-store",
  });
  if (res.status === 401) {
    window.location.href = "/login";
    throw new ApiError(401, "signed out");
  }
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new ApiError(res.status, data?.detail ?? data?.error ?? `HTTP ${res.status}`);
  return data as T;
}

export const when = (iso?: string) => (iso ? new Date(iso).toLocaleString() : "");

export const duration = (s?: number | null) =>
  s == null ? "—" : s < 90 ? `${Math.round(s)}s` : s < 5400 ? `${Math.round(s / 60)}m` : `${(s / 3600).toFixed(1)}h`;
