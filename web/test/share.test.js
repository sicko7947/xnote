import { test } from "bun:test";
import assert from "node:assert/strict";
import { createHandler } from "../src/handler.js";
import { validateDocument, markdown, page } from "../src/core.js";
class Bucket {
  data = new Map();
  versions = new Map();
  async put(k, b, options = {}) {
    const old = this.versions.get(k),
      c = options.onlyIf;
    if (c?.etagMatches && c.etagMatches !== old) return null;
    if (c?.etagDoesNotMatch === "*" && old) return null;
    this.data.set(
      k,
      typeof b === "string"
        ? new TextEncoder().encode(b)
        : new Uint8Array(await new Response(b).arrayBuffer()),
    );
    const etag = crypto.randomUUID();
    this.versions.set(k, etag);
    return { etag };
  }
  async delete(k) {
    this.data.delete(k);
    this.versions.delete(k);
  }
  async head(k) {
    const b = this.data.get(k);
    return b ? { size: b.length } : null;
  }
  async get(k, options = {}) {
    const b = this.data.get(k);
    if (!b) return null;
    const r = options.range;
    return {
      etag: this.versions.get(k),
      size: b.length,
      body: r ? b.slice(r.offset, r.offset + r.length) : b,
      json: async () => JSON.parse(new TextDecoder().decode(b)),
    };
  }
}
const id = "a".repeat(48),
  revision = "b".repeat(32),
  token = "test-secret-".repeat(4);
const sample = () => ({
  title: "团队讨论",
  recorded_at: "2026-10-06T10:00:00+11:00",
  duration_seconds: 20,
  transcript: "## 决定\n\n下周交付。",
  segments: [
    {
      start: 0,
      end: 10,
      speaker: "Alice",
      text: "下周交付。",
      timing: "provider",
    },
    {
      start: 10,
      end: 20,
      speaker: "Bob",
      text: "我负责测试。",
      timing: "chunk",
    },
  ],
  insights: {
    summary: "**重点**：下周交付。",
    mindmap: {
      title: "交付",
      branches: [{ title: "测试", points: ["Bob 负责测试"] }],
    },
    model: "test",
    generated_at: "2026-10-06",
  },
});
function setup() {
  const env = { SHARES: new Bucket(), SHARE_ADMIN_TOKEN: token };
  const fetch = createHandler();
  return {
    env,
    req: (path, method = "GET", data, headers = {}) =>
      fetch(
        new Request("https://share.example" + path, {
          method,
          headers: {
            ...(method === "PUT" ? { "Content-Type": "application/json" } : {}),
            ...headers,
          },
          body:
            data === undefined
              ? undefined
              : typeof data === "string"
                ? data
                : JSON.stringify(data),
        }),
        env,
      ),
  };
}
const auth = { Authorization: `Bearer ${token}` };
test("anonymous writes fail closed, reads have no listing and payloads are allowlisted", async () => {
  const { req, env } = setup();
  assert.equal((await req(`/api/shares/${id}`, "PUT", sample())).status, 401);
  assert.equal(env.SHARES.data.size, 0);
  const doc = {
    ...sample(),
    device_serial: "private-serial",
    audio_path: "/home/private",
    cloud_uid: "private-cloud",
  };
  assert.equal((await req(`/api/shares/${id}`, "PUT", doc, auth)).status, 200);
  const r = await req(`/s/${id}.json`);
  assert.equal(r.status, 200);
  const text = await r.text();
  for (const v of ["private-serial", "/home/private", "private-cloud"])
    assert.ok(!text.includes(v));
  assert.equal((await req("/api/shares")).status, 401);
  assert.equal((await req("/s/")).status, 404);
  assert.equal(
    (
      await req(`/api/shares/${id}`, "PUT", doc, {
        ...auth,
        Origin: "https://evil.example",
      })
    ).status,
    403,
  );
  const closed = createHandler();
  assert.equal(
    (
      await closed(
        new Request(`https://a/api/shares/${id}`, {
          method: "DELETE",
          headers: auth,
        }),
        { SHARES: env.SHARES },
      )
    ).status,
    401,
  );
});
test("HTML, markdown and JSON are accessible without JavaScript; timestamps stay honest", async () => {
  const { req } = setup();
  await req(`/api/shares/${id}`, "PUT", sample(), auth);
  const html = await (await req(`/s/${id}`)).text();
  assert.ok(html.includes("下周交付"));
  assert.ok(html.includes("Alice"));
  assert.ok(html.includes("≈ 00:00:10"));
  assert.ok(html.includes("Bob 负责测试"));
  const r = await req(`/s/${id}`, "GET", undefined, {
    Accept: "text/markdown",
  });
  assert.match(r.headers.get("Content-Type"), /text\/markdown/);
  assert.match(await r.text(), /## Summary/);
  assert.equal(
    (
      await req(`/s/${id}`, "GET", undefined, { Accept: "application/json" })
    ).headers.get("Content-Type"),
    "application/json; charset=utf-8",
  );
  for (const path of [
    ".json",
    ".md",
    "/transcript.md",
    "/summary.md",
    "/mindmap.md",
  ]) {
    const r = await req(`/s/${id}${path}`);
    assert.equal(r.status, 200);
    assert.equal(r.headers.get("Cache-Control"), "no-store");
  }
  assert.equal((await req(`/s/${id}`, "HEAD")).body, null);
});
test("stored XSS and remote tracking images are inert", async () => {
  const bad = "<script>alert(1)</script><img src=x onerror=alert(2)>";
  const doc = sample();
  doc.title = bad;
  doc.transcript =
    bad +
    "\n\n[click](javascript:alert(1))\n\n![tracking](https://evil.example/t)";
  doc.segments[0].speaker = bad;
  doc.insights.mindmap.branches[0].points = [bad];
  const html = page(validateDocument(doc), id);
  assert.ok(!html.includes("<script>alert"));
  assert.ok(!html.includes("<img"));
  assert.ok(!html.includes('href="javascript:'));
  assert.ok(html.includes("&lt;script&gt;"));
  assert.ok(!markdown("[x](data:text/html,test)").includes('href="data:'));
  const { req } = setup();
  await req(`/api/shares/${id}`, "PUT", doc, auth);
  const r = await req(`/s/${id}`);
  assert.match(
    r.headers.get("Content-Security-Policy"),
    /frame-ancestors 'none'/,
  );
  assert.equal(r.headers.get("Referrer-Policy"), "no-referrer");
});
test("audio publication, ranges, text-only update and revocation protect every endpoint", async () => {
  const { req } = setup();
  const doc = { ...sample(), audio: revision };
  assert.equal((await req(`/api/shares/${id}`, "PUT", doc, auth)).status, 400);
  assert.equal(
    (
      await req(`/api/shares/${id}/audio/${revision}`, "PUT", "0123456789", {
        ...auth,
        "Content-Type": "audio/mpeg",
        "Content-Length": "10",
      })
    ).status,
    200,
  );
  assert.equal((await req(`/s/${id}/audio`)).status, 404);
  await req(`/api/shares/${id}`, "PUT", doc, auth);
  const range = await req(`/s/${id}/audio`, "GET", undefined, {
    Range: "bytes=2-5",
  });
  assert.equal(range.status, 206);
  assert.equal(range.headers.get("Content-Range"), "bytes 2-5/10");
  assert.equal(await range.text(), "2345");
  const suffix = await req(`/s/${id}/audio`, "GET", undefined, {
    Range: "bytes=-3",
  });
  assert.equal(await suffix.text(), "789");
  for (const value of ["bytes=20-", "bytes=5-3", "bytes=-0", "bytes=0-1,4-5"])
    assert.equal(
      (await req(`/s/${id}/audio`, "GET", undefined, { Range: value })).status,
      416,
    );
  await req(`/api/shares/${id}`, "PUT", sample(), auth);
  assert.equal((await req(`/s/${id}/audio`)).status, 404);
  await req(`/api/shares/${id}`, "DELETE", undefined, auth);
  for (const path of [
    "",
    ".json",
    ".md",
    "/audio",
    "/summary.md",
    "/mindmap.md",
  ])
    assert.equal((await req(`/s/${id}${path}`)).status, 404);
  assert.equal(
    (await req(`/api/shares/${id}`, "PUT", sample(), auth)).status,
    410,
  );
  assert.equal(
    (await req(`/api/shares/${id}`, "DELETE", undefined, auth)).status,
    200,
  );
});
test("malformed, oversize and invalid timeline uploads are rejected", async () => {
  const { req } = setup();
  for (const value of [
    { ...sample(), audio: "../../secret" },
    { ...sample(), segments: [{ start: -1, end: 10, text: "bad" }] },
    { ...sample(), segments: [{ start: 10, end: 1, text: "bad" }] },
    { ...sample(), transcript: "x".repeat(2 * 1024 * 1024) },
  ])
    assert.equal(
      (await req(`/api/shares/${id}`, "PUT", value, auth)).status,
      400,
    );
  assert.equal(
    (
      await req(`/api/shares/${id}/audio/${revision}`, "PUT", "x", {
        ...auth,
        "Content-Type": "audio/mpeg",
        "Content-Length": String(100 * 1024 * 1024),
      })
    ).status,
    413,
  );
});
test("revoke wins over an already-started publication", async () => {
  const { req, env } = setup();
  await req(`/api/shares/${id}`, "PUT", sample(), auth);
  const original = env.SHARES.put.bind(env.SHARES);
  let release, arrive;
  const ready = new Promise((resolve) => {
    arrive = resolve;
  });
  const wait = new Promise((resolve) => {
    release = resolve;
  });
  env.SHARES.put = async (k, b, options) => {
    if (k === `shares/${id}.json` && String(b).includes("updated_at")) {
      arrive();
      await wait;
    }
    return original(k, b, options);
  };
  const update = req(`/api/shares/${id}`, "PUT", sample(), auth);
  await ready;
  assert.equal(
    (await req(`/api/shares/${id}`, "DELETE", undefined, auth)).status,
    200,
  );
  release();
  assert.equal((await update).status, 409);
  assert.equal((await req(`/s/${id}.json`)).status, 404);
});
test("summary-only and mindmap-only documents work independently", async () => {
  const { req } = setup();
  const d = sample();
  delete d.insights.mindmap;
  assert.equal((await req(`/api/shares/${id}`, "PUT", d, auth)).status, 200);
  assert.equal((await req(`/s/${id}/mindmap.md`)).status, 404);
  const mapOnly = sample();
  mapOnly.insights.summary = "";
  await req(`/api/shares/${id}`, "PUT", mapOnly, auth);
  assert.equal((await req(`/s/${id}/summary.md`)).status, 404);
  assert.equal((await req(`/s/${id}/mindmap.md`)).status, 200);
});
