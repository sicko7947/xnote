#!/usr/bin/env bun
// Run after `bun install --frozen-lockfile` and `bunx wrangler login`. Never prints or embeds the admin token.
import {
  readFileSync,
  writeFileSync,
  mkdirSync,
  chmodSync,
  mkdtempSync,
  rmSync,
  existsSync,
} from "node:fs";
import { spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { homedir } from "node:os";
import { resolve, join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
const web = resolve(dirname(fileURLToPath(import.meta.url)), "..");
let dataDir = "";
let explicitURL = process.env.XNOTE_SHARE_URL || "";
const args = process.argv.slice(2);
for (let i = 0; i < args.length; i++) {
  if (args[i] === "--data" && args[i + 1]) dataDir = args[++i];
  else if (args[i] === "--url" && args[i + 1]) explicitURL = args[++i];
  else
    throw new Error(
      "Usage: bun run setup [--data /path/to/XNote] [--url https://share.example.com]",
    );
}
const root = resolve(
  dataDir || process.env.XNOTE_DATA_DIR || join(homedir(), "Documents/XNote"),
);
const binary = resolve(web, "../dist/xnote");
if (!existsSync(binary))
  throw new Error(
    "Build XNote first: run scripts/build.sh from the repository root.",
  );
const wrangler = join(web, "node_modules/.bin/wrangler");
function run(command, argv, input) {
  const result = spawnSync(command, argv, {
    cwd: web,
    encoding: "utf8",
    input,
    env: { ...process.env, WRANGLER_SEND_METRICS: "false" },
    maxBuffer: 4 * 1024 * 1024,
  });
  const output = (result.stdout || "") + (result.stderr || "");
  if (result.status !== 0) {
    process.stderr.write(output);
    throw new Error(
      `${argv.slice(0, 3).join(" ")} failed. Check Cloudflare login and R2 access.`,
    );
  }
  process.stdout.write(output);
  return output;
}
function origin(value, label) {
  let u;
  try {
    u = new URL(value);
  } catch {
    throw new Error(`${label} must be an HTTPS origin: ${value}`);
  }
  if (
    u.protocol !== "https:" ||
    u.username ||
    u.password ||
    u.search ||
    u.hash ||
    (u.pathname !== "/" && u.pathname !== "")
  )
    throw new Error(`${label} must be an HTTPS origin without a path: ${value}`);
  return u.origin;
}
// Read the declared custom domain before deploying, so a workers.dev URL never overrides it.
function customDomain() {
  const raw = readFileSync(join(web, "wrangler.jsonc"), "utf8")
    .replace(/^\s*\/\/.*$/gm, "")
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/,\s*([}\]])/g, "$1");
  const routes = JSON.parse(raw).routes;
  if (!Array.isArray(routes)) return "";
  const route = routes.find(
    (r) => r?.custom_domain && typeof r.pattern === "string",
  );
  return route ? origin("https://" + route.pattern, "wrangler.jsonc route") : "";
}
const buckets = run(wrangler, ["r2", "bucket", "list"]);
if (!/(?:name:\s*|"name"\s*:\s*")xnote-shares(?:"|\s|$)/.test(buckets))
  run(wrangler, ["r2", "bucket", "create", "xnote-shares"]);
mkdirSync(root, { recursive: true, mode: 0o700 });
mkdirSync(join(root, ".work"), { recursive: true, mode: 0o700 });
const envFile = join(root, ".env");
let content = existsSync(envFile) ? readFileSync(envFile, "utf8") : "";
const match = content.match(
  /^(?:export[ \t]+)?XNOTE_SHARE_TOKEN[ \t]*=[ \t]*(.*?)[ \t]*$/m,
);
let token = match?.[1]?.replace(/^(['"])(.*)\1$/, "$2");
if (!token) {
  token = randomBytes(32).toString("hex");
  content = content.replace(
    /^(?:export[ \t]+)?XNOTE_SHARE_TOKEN[ \t]*=.*\r?\n?/m,
    "",
  );
  content += "\nXNOTE_SHARE_TOKEN=" + token + "\n";
  writeFileSync(envFile, content, { mode: 0o600 });
}
if (!/^[A-Za-z0-9_-]{32,}$/.test(token))
  throw new Error(
    "Existing XNOTE_SHARE_TOKEN must contain at least 32 letters/digits/_/-. It was not changed.",
  );
chmodSync(envFile, 0o600);
const temp = mkdtempSync(join(root, ".work/share-deploy-"));
let output;
try {
  const secrets = join(temp, "secrets.env");
  writeFileSync(secrets, "SHARE_ADMIN_TOKEN=" + token + "\n", { mode: 0o600 });
  output = run(wrangler, ["deploy", "--minify", "--secrets-file", secrets]);
} finally {
  rmSync(temp, { recursive: true, force: true });
}
const url = explicitURL
  ? origin(explicitURL, "--url")
  : customDomain() ||
    output.match(/https:\/\/xnote-share\.[a-z0-9-]+\.workers\.dev/)?.[0];
if (!url)
  throw new Error(
    "Deployment completed, but no share URL was found. Pass --url https://your.domain, set XNOTE_SHARE_URL, or add a custom domain route to wrangler.jsonc.",
  );
run(binary, ["--data", root, "config", "share_url", url]);
const health = await fetch(url + "/health");
if (!health.ok) throw new Error("Deployed, but the health check failed.");
console.log(
  `\nShare site ready: ${url}\nLibrary configured: ${root}\nRestart an already-open XNote to load the private token.\nPublish only the recordings you choose: xnote share RECORDING_ID [--audio]`,
);
