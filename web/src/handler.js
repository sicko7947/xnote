import {
  idPattern,
  revisionPattern,
  maxAudio,
  maxDocument,
  validateDocument,
  asMarkdown,
  mindmapMarkdown,
  page,
} from "./core.js";
const baseHeaders = {
  "Cache-Control": "no-store",
  "X-Content-Type-Options": "nosniff",
  "Referrer-Policy": "no-referrer",
  "X-Robots-Tag": "noindex, nofollow, noarchive",
  "Content-Security-Policy":
    "default-src 'none'; script-src 'self'; style-src 'self'; media-src 'self'; connect-src 'self'; img-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'",
  "Permissions-Policy": "camera=(), microphone=(), geolocation=()",
  "Strict-Transport-Security": "max-age=31536000",
};
const reply = (
  body,
  status = 200,
  type = "application/json; charset=utf-8",
  headers = {},
) =>
  new Response(body, {
    status,
    headers: { ...baseHeaders, "Content-Type": type, ...headers },
  });
const json = (v, status = 200) => reply(JSON.stringify(v), status);
const fail = (status, message) => json({ error: message }, status);
const key = (id) => `shares/${id}.json`;
const audioKey = (id, revision) => `audio/${id}/${revision}.mp3`;
const unchanged = (old) =>
  old ? { etagMatches: old.etag } : { etagDoesNotMatch: "*" };
async function authorized(request, env) {
  if (!env.SHARE_ADMIN_TOKEN || env.SHARE_ADMIN_TOKEN.length < 32) return false;
  const given = request.headers.get("Authorization") || "";
  const enc = new TextEncoder();
  const [a, b] = await Promise.all(
    [given, `Bearer ${env.SHARE_ADMIN_TOKEN}`].map((v) =>
      crypto.subtle.digest("SHA-256", enc.encode(v)),
    ),
  );
  const aa = new Uint8Array(a),
    bb = new Uint8Array(b);
  let diff = 0;
  for (let i = 0; i < aa.length; i++) diff |= aa[i] ^ bb[i];
  return diff === 0;
}
async function readDocument(request) {
  if (!request.headers.get("Content-Type")?.startsWith("application/json"))
    throw new Error("Expected application/json");
  const reader = request.body?.getReader();
  if (!reader) throw new Error("Missing body");
  let size = 0;
  const chunks = [];
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > maxDocument) throw new Error("Document exceeds 2 MiB");
      chunks.push(value);
    }
  } finally {
    await reader.cancel();
  }
  const all = new Uint8Array(size);
  let p = 0;
  for (const v of chunks) {
    all.set(v, p);
    p += v.length;
  }
  return validateDocument(JSON.parse(new TextDecoder().decode(all)));
}
export function createHandler(assets = { style: "", app: "" }) {
  return async function fetch(request, env) {
    try {
      const url = new URL(request.url),
        path = url.pathname,
        method = request.method;
      if (path.startsWith("/api/")) {
        if (!(await authorized(request, env))) return fail(401, "Unauthorized");
        if (
          request.headers.get("Origin") &&
          request.headers.get("Origin") !== url.origin
        )
          return fail(403, "Cross-origin writes are disabled");
        const m = path.match(
          /^\/api\/shares\/([a-f0-9]{48})(?:\/audio\/([a-f0-9]{32}))?$/,
        );
        if (!m) return fail(404, "Not found");
        const [, id, revision] = m;
        if (revision) {
          const k = audioKey(id, revision);
          if (method === "PUT") {
            const manifest = await env.SHARES.get(key(id));
            if (manifest && (await manifest.json()).revoked)
              return fail(410, "This share was revoked; create a new ID");
            const length = Number(request.headers.get("Content-Length"));
            if (
              !Number.isSafeInteger(length) ||
              length <= 0 ||
              length > maxAudio
            )
              return fail(
                413,
                "Audio must be 1 byte to 95 MiB with Content-Length",
              );
            if (request.headers.get("Content-Type") !== "audio/mpeg")
              return fail(415, "Expected audio/mpeg");
            await env.SHARES.put(k, request.body, {
              httpMetadata: { contentType: "audio/mpeg" },
            });
            return json({ ok: true });
          }
          if (method === "DELETE") {
            await env.SHARES.delete(k);
            return json({ ok: true });
          }
          return fail(405, "Method not allowed");
        }
        if (method === "PUT") {
          let doc;
          try {
            doc = await readDocument(request);
          } catch {
            return fail(400, "Invalid share document (maximum 2 MiB)");
          }
          if (doc.audio && !(await env.SHARES.head(audioKey(id, doc.audio))))
            return fail(400, "Upload audio before publishing");
          doc.updated_at = new Date().toISOString();
          const old = await env.SHARES.get(key(id));
          const previous = old ? await old.json() : null;
          if (previous?.revoked)
            return fail(410, "This share was revoked; create a new ID");
          const written = await env.SHARES.put(key(id), JSON.stringify(doc), {
            onlyIf: unchanged(old),
            httpMetadata: { contentType: "application/json" },
          });
          if (!written) return fail(409, "Share changed concurrently; retry");
          if (previous?.audio && previous.audio !== doc.audio)
            await env.SHARES.delete(audioKey(id, previous.audio));
          return json({ url: `${url.origin}/s/${id}`, id });
        }
        if (method === "DELETE") {
          // A tiny tombstone prevents an in-flight or delayed upload resurrecting a revoked URL.
          for (let attempt = 0; attempt < 3; attempt++) {
            const old = await env.SHARES.get(key(id));
            const previous = old ? await old.json() : null;
            if (previous?.revoked) return json({ ok: true });
            const written = await env.SHARES.put(
              key(id),
              JSON.stringify({ revoked: true }),
              { onlyIf: unchanged(old) },
            );
            if (!written) continue;
            if (previous?.audio)
              await env.SHARES.delete(audioKey(id, previous.audio));
            return json({ ok: true });
          }
          return fail(409, "Share changed concurrently; retry revocation");
        }
        return fail(405, "Method not allowed");
      }
      if (!["GET", "HEAD"].includes(method))
        return fail(405, "Method not allowed");
      const finish = (response) =>
        method === "HEAD" ? new Response(null, response) : response;
      if (path === "/health")
        return finish(json({ ok: true, service: "xnote-share" }));
      if (path === "/robots.txt")
        return finish(reply("User-agent: *\nDisallow: /\n", 200, "text/plain"));
      if (path === "/assets/style.css")
        return finish(reply(assets.style, 200, "text/css; charset=utf-8"));
      if (path === "/assets/app.js")
        return finish(reply(assets.app, 200, "text/javascript; charset=utf-8"));
      if (path === "/")
        return finish(
          reply(
            '<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>X Note</title><link rel="stylesheet" href="/assets/style.css"><main><h1>X Note</h1><p>Open a shared link to view a recording and its transcript.</p><p class="muted">There is no public recording directory.</p></main></html>',
            200,
            "text/html; charset=utf-8",
          ),
        );
      const m = path.match(
        /^\/s\/([a-f0-9]{48})(?:(\.json|\.md)|\/(audio|transcript\.md|summary\.md|mindmap\.md))?$/,
      );
      if (!m || !idPattern.test(m[1]))
        return finish(fail(404, "Share not found"));
      const [, id, extension, resource] = m;
      const object = await env.SHARES.get(key(id));
      if (!object) return finish(fail(404, "Share not found"));
      const doc = await object.json();
      if (doc.revoked) return finish(fail(404, "Share not found"));
      if (resource === "audio") {
        if (!doc.audio || !revisionPattern.test(doc.audio))
          return finish(fail(404, "Audio not shared"));
        const head = await env.SHARES.head(audioKey(id, doc.audio));
        if (!head) return finish(fail(404, "Audio not found"));
        const headers = {
          "Accept-Ranges": "bytes",
          "Content-Length": String(head.size),
        };
        let range;
        const h = request.headers.get("Range");
        if (h) {
          const r = h.match(/^bytes=(\d*)-(\d*)$/);
          if (!r || (!r[1] && !r[2]))
            return finish(
              reply(null, 416, "audio/mpeg", {
                "Content-Range": `bytes */${head.size}`,
              }),
            );
          let start = r[1]
            ? Number(r[1])
            : Math.max(0, head.size - Number(r[2]));
          let end = r[1]
            ? r[2]
              ? Math.min(Number(r[2]), head.size - 1)
              : head.size - 1
            : head.size - 1;
          if (
            !Number.isSafeInteger(start) ||
            !Number.isSafeInteger(end) ||
            start > end ||
            start >= head.size
          )
            return finish(
              reply(null, 416, "audio/mpeg", {
                "Content-Range": `bytes */${head.size}`,
              }),
            );
          range = { offset: start, length: end - start + 1 };
          headers["Content-Range"] = `bytes ${start}-${end}/${head.size}`;
          headers["Content-Length"] = String(range.length);
        }
        if (method === "HEAD")
          return reply(null, range ? 206 : 200, "audio/mpeg", headers);
        const audio = await env.SHARES.get(
          audioKey(id, doc.audio),
          range ? { range } : {},
        );
        if (!audio) return fail(404, "Audio not found");
        return reply(audio.body, range ? 206 : 200, "audio/mpeg", headers);
      }
      const publicDoc = { ...doc, audio: doc.audio ? `/s/${id}/audio` : null };
      const accept = request.headers.get("Accept") || "";
      if (
        extension === ".json" ||
        (!resource && !extension && accept.includes("application/json"))
      )
        return finish(json(publicDoc));
      let content;
      if (resource === "transcript.md") content = doc.transcript;
      else if (resource === "summary.md")
        content = doc.insights?.summary || undefined;
      else if (resource === "mindmap.md")
        content = doc.insights?.mindmap
          ? mindmapMarkdown(doc.insights.mindmap)
          : undefined;
      else if (
        extension === ".md" ||
        (!resource && accept.includes("text/markdown"))
      )
        content = asMarkdown(doc);
      if (resource && content === undefined)
        return finish(fail(404, "Content not generated"));
      if (content !== undefined)
        return finish(
          reply(content, 200, "text/markdown; charset=utf-8", {
            Vary: "Accept",
          }),
        );
      return finish(
        reply(page(publicDoc, id), 200, "text/html; charset=utf-8", {
          Vary: "Accept",
          Link: `</s/${id}.json>; rel="alternate"; type="application/json", </s/${id}.md>; rel="alternate"; type="text/markdown"`,
        }),
      );
    } catch {
      // Never include request bodies, credentials or storage errors in public responses/logs.
      return fail(500, "Share service unavailable");
    }
  };
}
