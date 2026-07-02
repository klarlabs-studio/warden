#!/usr/bin/env node
// Pure launcher: locate the prebuilt warden binary for this platform (shipped
// as an optionalDependency npm resolved for us) and exec it, forwarding args,
// stdio, and the exit code. All warden logic lives in the Go binary; this file
// carries none of it.
"use strict";

const { spawnSync } = require("node:child_process");

function binaryPath() {
  const pkg = `@klarlabs/warden-${process.platform}-${process.arch}`;
  const exe = process.platform === "win32" ? "warden.exe" : "warden";
  try {
    // npm installed only the platform package matching this os/cpu.
    return require.resolve(`${pkg}/bin/${exe}`);
  } catch {
    return null;
  }
}

const bin = binaryPath();
if (!bin) {
  console.error(
    `warden: no prebuilt binary for ${process.platform}-${process.arch}.\n` +
      `Install another way: https://github.com/klarlabs/warden/releases ` +
      `or 'go install go.klarlabs.de/warden@latest'.`,
  );
  process.exit(1);
}

const res = spawnSync(bin, process.argv.slice(2), { stdio: "inherit" });
if (res.error) {
  console.error(res.error.message);
  process.exit(1);
}
process.exit(res.status === null ? 1 : res.status);
