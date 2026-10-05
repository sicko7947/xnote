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
const args = process.argv.slice(2);
if (args.length && (args.length !== 2 || args[0] !== "--data"))
  throw new Error("Usage: bun run setup [--data /path/to/XNote]");
const root = resolve(
  args[1] || process.env.XNOTE_DATA_DIR || join(homedir(), "Documents/XNote"),
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
const url = output.match(
  /https:\/\/xnote-share\.[a-z0-9-]+\.workers\.dev/,
)?.[0];
if (!url)
  throw new Error(
    "Deployment completed, but no workers.dev URL was returned. Enable workers.dev, then set xnote config share_url manually.",
  );
run(binary, ["--data", root, "config", "share_url", url]);
const health = await fetch(url + "/health");
if (!health.ok) throw new Error("Deployed, but the health check failed.");
console.log(
  `\nShare site ready: ${url}\nLibrary configured: ${root}\nRestart an already-open XNote to load the private token.\nPublish only the recordings you choose: xnote share RECORDING_ID [--audio]`,
);
