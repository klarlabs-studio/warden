#!/usr/bin/env node
// Assemble the per-platform npm packages from goreleaser's built binaries.
//
//   node npm/scripts/build-packages.mjs <version> [distDir] [outDir]
//
// For each supported platform it creates @klarlabs/warden-<os>-<arch> holding
// one prebuilt binary plus os/cpu fields, so `npm install` fetches only the
// matching package. It also rewrites the main package's version + the
// optionalDependencies to the release version. Node built-ins only.
import { existsSync, mkdirSync, readdirSync, copyFileSync, chmodSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const npmRoot = join(here, "..");

const version = process.argv[2];
const distDir = process.argv[3] || join(npmRoot, "..", "dist");
const outDir = process.argv[4] || join(npmRoot, "packages");

if (!version) {
  console.error("usage: build-packages.mjs <version> [distDir] [outDir]");
  process.exit(2);
}

// (goos, goarch) -> (npm platform, npm arch). Matches process.platform/arch.
const TARGETS = [
  { goos: "linux", goarch: "amd64", platform: "linux", arch: "x64" },
  { goos: "linux", goarch: "arm64", platform: "linux", arch: "arm64" },
  { goos: "darwin", goarch: "amd64", platform: "darwin", arch: "x64" },
  { goos: "darwin", goarch: "arm64", platform: "darwin", arch: "arm64" },
  { goos: "windows", goarch: "amd64", platform: "win32", arch: "x64" },
  { goos: "windows", goarch: "arm64", platform: "win32", arch: "arm64" },
];

// findBinary locates goreleaser's binary for a target. goreleaser writes to
// dist/warden_<goos>_<goarch>[_variant]/warden[.exe]; the variant suffix
// (_v1, _v8.0, …) varies, so match by prefix.
function findBinary(t) {
  const prefix = `warden_${t.goos}_${t.goarch}`;
  const dirs = readdirSync(distDir, { withFileTypes: true })
    .filter((d) => d.isDirectory() && d.name.startsWith(prefix))
    .map((d) => d.name);
  const exe = t.goos === "windows" ? "warden.exe" : "warden";
  for (const d of dirs) {
    const p = join(distDir, d, exe);
    if (existsSync(p)) return p;
  }
  return null;
}

rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

const built = [];
for (const t of TARGETS) {
  const src = findBinary(t);
  if (!src) {
    console.warn(`skip ${t.platform}-${t.arch}: no binary under ${distDir}`);
    continue;
  }
  const name = `@klarlabs/warden-${t.platform}-${t.arch}`;
  const pkgDir = join(outDir, `warden-${t.platform}-${t.arch}`);
  const binDir = join(pkgDir, "bin");
  mkdirSync(binDir, { recursive: true });

  const exe = t.goos === "windows" ? "warden.exe" : "warden";
  const dst = join(binDir, exe);
  copyFileSync(src, dst);
  chmodSync(dst, 0o755);

  writeFileSync(
    join(pkgDir, "package.json"),
    JSON.stringify(
      {
        name,
        version,
        description: `warden binary for ${t.platform}-${t.arch}`,
        os: [t.platform],
        cpu: [t.arch],
        files: [`bin/${exe}`],
        license: "MIT",
      },
      null,
      2,
    ) + "\n",
  );
  built.push(name);
  console.log(`built ${name}`);
}

// Rewrite the main package version + optionalDependencies to this release.
const mainPath = join(npmRoot, "package.json");
const main = JSON.parse(readFileSync(mainPath, "utf8"));
main.version = version;
main.optionalDependencies = Object.fromEntries(built.map((n) => [n, version]));
writeFileSync(mainPath, JSON.stringify(main, null, 2) + "\n");

console.log(`\n${built.length} platform packages -> ${outDir}`);
console.log(`main package -> version ${version}, ${built.length} optionalDependencies`);
