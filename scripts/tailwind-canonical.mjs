// Fails on a dashboard class that has a canonical Tailwind spelling, the
// same check as the editor's suggestCanonicalClasses hint.

import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { extname, relative, resolve } from "node:path";

const web = resolve(process.argv[2] ?? "web");
const require = createRequire(resolve(web, "package.json"));
const { __unstable__loadDesignSystem } = require("@tailwindcss/node");
const { Scanner } = require("@tailwindcss/oxide");

const cssPath = resolve(web, "src/styles/index.css");
const design = await __unstable__loadDesignSystem(readFileSync(cssPath, "utf8"), {
  base: resolve(cssPath, ".."),
});
const scanner = new Scanner({ sources: [{ base: resolve(web, "src"), pattern: "**/*", negated: false }] });
scanner.scan();

let failed = false;
for (const file of scanner.files) {
  const content = readFileSync(file, "utf8");
  for (const { candidate, position } of scanner.getCandidatesWithPositions({ content, extension: extname(file).slice(1) })) {
    const [canonical] = design.canonicalizeCandidates([candidate]);
    if (canonical === candidate || design.parseCandidate(candidate).length === 0) continue;
    const line = content.slice(0, position).split("\n").length;
    console.log(`${relative(process.cwd(), file)}:${line}: ${candidate} can be written as ${canonical}`);
    failed = true;
  }
}
process.exit(failed ? 1 : 0);
