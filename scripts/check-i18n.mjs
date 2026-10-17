// Fails when web/app.js uses a t()/tx() key that has no Chinese translation.
import { readFileSync } from "node:fs";

// Run i18n.js with stub browser globals passed in as parameters.
const ZH = new Function("localStorage", "navigator", "document", readFileSync("web/i18n.js", "utf8") + "\nreturn ZH;")(
  { getItem: () => "zh", setItem() {} },
  { language: "zh" },
  { documentElement: {} },
);
const app = readFileSync("web/app.js", "utf8");
const keys = new Set([...app.matchAll(/\btx?\(\s*"((?:[^"\\]|\\.)*)"/g)].map((m) => JSON.parse(`"${m[1]}"`)));
const missing = [...keys].filter((k) => !(k in ZH));
if (missing.length) {
  console.error(`Missing zh translations (${missing.length}):\n` + missing.map((k) => `  ${k}`).join("\n"));
  process.exit(1);
}
console.log(`i18n: ${keys.size} keys translated`);
