import MarkdownIt from "markdown-it";

// Raw HTML, remote images and javascript: links never become executable markup.
const md = new MarkdownIt({ html: false, linkify: false, breaks: true });
md.disable("image");
export const escapeHTML = (value) =>
  String(value ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
export const markdown = (value) => md.render(value || "");
export const idPattern = /^[a-f0-9]{48}$/;
export const revisionPattern = /^[a-f0-9]{32}$/;
export const maxAudio = 95 * 1024 * 1024;
export const maxDocument = 2 * 1024 * 1024;
const text = (v, max) => typeof v === "string" && v.length <= max;
const finite = (v) => typeof v === "number" && Number.isFinite(v) && v >= 0;
export function validateDocument(v) {
  if (
    !v ||
    !text(v.title, 500) ||
    !text(v.transcript, 1500000) ||
    !text(v.recorded_at, 100) ||
    !finite(v.duration_seconds)
  )
    throw new Error("Invalid recording");
  if (!Array.isArray(v.segments) || v.segments.length > 20000)
    throw new Error("Invalid timeline");
  const segments = v.segments.map((s) => {
    if (
      !s ||
      !finite(s.start) ||
      !finite(s.end) ||
      s.end < s.start ||
      !text(s.text, 100000) ||
      !text(s.speaker ?? "", 200) ||
      !["", "chunk", "provider", "segment", "word"].includes(s.timing ?? "")
    )
      throw new Error("Invalid segment");
    return {
      start: s.start,
      end: s.end,
      speaker: s.speaker || "",
      text: s.text,
      timing: s.timing || "",
    };
  });
  if (v.audio && !revisionPattern.test(v.audio))
    throw new Error("Invalid audio reference");
  let insights = null;
  if (v.insights) {
    const a = v.insights;
    if (
      !text(a.summary, 100000) ||
      !text(a.model, 200) ||
      !text(a.generated_at, 100)
    )
      throw new Error("Invalid insights");
    if (
      a.mindmap &&
      (!text(a.mindmap.title, 500) ||
        !Array.isArray(a.mindmap.branches) ||
        a.mindmap.branches.length > 30)
    )
      throw new Error("Invalid mindmap");
    const branches = (a.mindmap?.branches || []).map((b) => {
      if (
        !b ||
        !text(b.title, 500) ||
        !Array.isArray(b.points) ||
        b.points.length > 30 ||
        !b.points.every((p) => text(p, 2000))
      )
        throw new Error("Invalid mindmap");
      return { title: b.title, points: b.points };
    });
    insights = {
      summary: a.summary,
      mindmap: a.mindmap ? { title: a.mindmap.title, branches } : null,
      model: a.model,
      generated_at: a.generated_at,
    };
  }
  // Explicit allowlist: local IDs, paths, device serials, tokens and cloud IDs are never published.
  return {
    schema_version: 1,
    title: v.title,
    transcript: v.transcript,
    recorded_at: v.recorded_at,
    duration_seconds: v.duration_seconds,
    segments,
    insights,
    audio: v.audio || null,
  };
}
export function clock(n) {
  n = Math.floor(n || 0);
  return [Math.floor(n / 3600), Math.floor(n / 60) % 60, n % 60]
    .map((x) => String(x).padStart(2, "0"))
    .join(":");
}
export function mindmapMarkdown(a) {
  if (!a) return "";
  return (
    `# ${a.title}\n\n` +
    a.branches
      .map((b) => `- ${b.title}\n` + b.points.map((p) => `  - ${p}\n`).join(""))
      .join("")
  );
}
export function asMarkdown(d) {
  let out = `# ${d.title}\n\nRecorded: ${d.recorded_at}\n\n`;
  if (d.insights?.summary) out += `## Summary\n\n${d.insights.summary}\n\n`;
  if (d.insights?.mindmap)
    out += `## Mindmap\n\n${mindmapMarkdown(d.insights.mindmap).replace(/^# .+\n/, "")}\n`;
  out += `## Transcript\n\n${d.transcript}\n`;
  if (d.segments.length)
    out +=
      "\n## Timeline\n\n" +
      d.segments
        .map(
          (s) =>
            `### ${s.timing === "chunk" ? "≈ " : ""}${clock(s.start)}${s.speaker ? " · " + s.speaker : ""}\n\n${s.text}\n`,
        )
        .join("\n");
  return out;
}
const e = escapeHTML;
export function page(d, id) {
  const base = `/s/${id}`;
  const speakers = [
    ...new Set(d.segments.map((s) => s.speaker).filter(Boolean)),
  ];
  const hasApprox = d.segments.some((s) => s.timing === "chunk");
  const timeline = d.segments
    .map(
      (s) =>
        `<article class="segment" data-start="${s.start}" data-end="${s.end}" data-speaker="${e(s.speaker)}"><div class="segment-meta"><button class="timestamp" data-seek="${s.start}" ${!d.audio ? "disabled" : ""}>${s.timing === "chunk" ? "≈ " : ""}${clock(s.start)}</button><span class="speaker">${e(s.speaker || "未标注说话人")}</span></div><p>${e(s.text)}</p></article>`,
    )
    .join("");
  const map = d.insights?.mindmap;
  const mapHTML = map
    ? `<div class="mindmap"><div class="map-root">${e(map.title)}</div><ul class="branches">${map.branches.map((b) => `<li class="branch"><h3>${e(b.title)}</h3><ul>${b.points.map((p) => `<li>${e(p)}</li>`).join("")}</ul></li>`).join("")}</ul></div>`
    : '<p class="muted">尚未生成思维导图。</p>';
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex,nofollow,noarchive"><title>${e(d.title)} · X Note</title><link rel="stylesheet" href="/assets/style.css"><link rel="alternate" type="text/markdown" href="${base}.md"><link rel="alternate" type="application/json" href="${base}.json"><script src="/assets/app.js" defer></script></head><body><header class="topbar"><a class="brand" href="/">X Note</a><nav><a href="${base}.md">Markdown</a><a href="${base}.json">JSON</a></nav></header><main><h1>${e(d.title)}</h1><div class="metadata"><span>${e(d.recorded_at)}</span><span>${clock(d.duration_seconds)}</span><span>${speakers.length ? `${speakers.length} 位说话人` : "未提供说话人信息"}</span></div>${d.audio ? `<div class="player"><audio controls preload="metadata" src="${base}/audio"></audio><label>倍速 <select id="speed"><option>1</option><option>1.25</option><option>1.5</option><option>2</option></select></label></div>` : '<p class="muted">此分享仅包含文字。</p>'}<nav class="tabs" aria-label="内容">${d.insights?.summary ? '<a href="#summary">总结</a>' : ""}${map ? '<a href="#mindmap">思维导图</a>' : ""}<a href="#transcript">转写原文</a><a href="#timeline">时间线</a></nav>${d.insights?.summary ? '<section id="summary" class="panel">' : '<section id="summary" class="panel" hidden>'}<div class="section-heading"><h2>总结</h2></div>${d.insights?.summary ? `<div class="prose">${markdown(d.insights.summary)}</div><p class="caption">AI 生成，请对照原文核实 · ${e(d.insights.model)} · ${e(d.insights.generated_at)}</p>` : '<p class="muted">尚未生成总结。</p>'}</section>${map ? '<section id="mindmap" class="panel">' : '<section id="mindmap" class="panel" hidden>'}<div class="section-heading"><h2>思维导图</h2><a href="${base}/mindmap.md">下载大纲</a></div>${mapHTML}</section><section id="transcript" class="panel"><h2>转写原文</h2><div class="prose">${markdown(d.transcript)}</div></section><section id="timeline" class="panel"><div class="section-heading"><h2>时间线</h2><span class="muted">${d.segments.length} 个片段</span></div>${hasApprox ? '<p class="caption">≈ 表示音频分段起点，不是逐句时间戳。</p>' : ""}<div class="filters"><input id="search" type="search" placeholder="搜索转写内容" aria-label="搜索转写内容"><select id="speaker" aria-label="筛选说话人"><option value="">全部说话人</option>${speakers.map((s) => `<option>${e(s)}</option>`).join("")}</select></div><div id="segments">${timeline || '<p class="muted">此转写没有提供时间片段。</p>'}</div><p id="no-results" hidden>没有匹配的片段。</p></section><footer>持有此链接的人可以查看内容。</footer></main></body></html>`;
}
