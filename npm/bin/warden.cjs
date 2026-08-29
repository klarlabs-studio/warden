#!/usr/bin/env node
// Pure launcher: locate the prebuilt warden binary for this platform (shipped
// as an optionalDependency npm resolved for us) and exec it, forwarding args,
// stdio, and the exit code. All warden logic lives in the Go binary; this file
// carries none of it.
"use strict";

const { spawnSync } = require("node:child_process");

function binaryPath() {
  const pkg = `@klarlabs-studio/warden-${process.platform}-${process.arch}`;
  const exe = process.platform === "win32" ? "warden.exe" : "warden";
  try {
    // npm installed only the platform package matching this os/cpu.
    return require.resolve(`${pkg}/bin/${exe}`);
  } catch {
    return null;
  }
}

// Whether warden PUBLISHES a binary for this platform, which is a different
// question from whether one is installed. The wrapper's own
// optionalDependencies list every platform package built for this release, so
// it answers the first question without a network call.
function isPublishedPlatform(pkg) {
  try {
    const own = require("../package.json");
    return Object.prototype.hasOwnProperty.call(own.optionalDependencies || {}, pkg);
  } catch {
    // Cannot tell. Say the weaker thing rather than guess.
    return false;
  }
}

const bin = binaryPath();
if (!bin) {
  // These two failures look identical here and have opposite fixes, so they
  // must not share a message. Telling someone their platform is unsupported
  // when the real problem is a missing install sends them to build from source
  // to solve something a reinstall fixes.
  const pkg = `@klarlabs-studio/warden-${process.platform}-${process.arch}`;
  if (isPublishedPlatform(pkg)) {
    console.error(
      `warden: ${pkg} is published for this release but is not installed here.\n` +
        `This is an install problem, not an unsupported platform. Common causes:\n` +
        `  - installed with --no-optional or --omit=optional, which skips it\n` +
        `  - the release published minutes ago and the registry has not caught up\n` +
        `  - a partial or interrupted install\n` +
        `Try: npm install ${pkg}\n` +
        `Binaries: https://github.com/klarlabs-studio/warden/releases`,
    );
  } else {
    console.error(
      `warden: no prebuilt binary for ${process.platform}-${process.arch}.\n` +
        `Install another way: https://github.com/klarlabs-studio/warden/releases ` +
        `or 'go install go.klarlabs.de/warden@latest'.`,
    );
  }
  process.exit(1);
}

const res = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (res.error) {
  console.error(res.error.message);
  process.exit(1);
}
process.exit(res.status === null ? 1 : res.status);
