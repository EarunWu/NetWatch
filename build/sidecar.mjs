import { mkdir, rm } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

import { releaseGoEnvironment } from "./go-toolchain.mjs";

const buildDirectory = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(buildDirectory, "..");
const serviceRoot = join(projectRoot, "service");
const binariesRoot = join(projectRoot, "src-tauri", "binaries");

const explicitTarget = process.argv.find((argument) => argument.startsWith("--target="))?.slice(9);
const target = explicitTarget || process.env.TAURI_ENV_TARGET_TRIPLE || hostTarget();
const platform = targetPlatform(target);
const executableName = `NetWatch.Service-${target}${platform.goos === "windows" ? ".exe" : ""}`;
const executablePath = join(binariesRoot, executableName);

function hostTarget() {
  const targets = {
    "win32-x64": "x86_64-pc-windows-msvc",
    "win32-arm64": "aarch64-pc-windows-msvc",
    "darwin-x64": "x86_64-apple-darwin",
    "darwin-arm64": "aarch64-apple-darwin",
  };
  const target = targets[`${process.platform}-${process.arch}`];
  if (!target) {
    throw new Error(`Unsupported desktop build host: ${process.platform}/${process.arch}`);
  }
  return target;
}

function targetPlatform(value) {
  if (value === "x86_64-pc-windows-msvc") return { goos: "windows", goarch: "amd64" };
  if (value === "aarch64-pc-windows-msvc") return { goos: "windows", goarch: "arm64" };
  if (value === "x86_64-apple-darwin") return { goos: "darwin", goarch: "amd64" };
  if (value === "aarch64-apple-darwin") return { goos: "darwin", goarch: "arm64" };
  throw new Error(`Unsupported Tauri target triple: ${value}`);
}

async function run(command, args, cwd = projectRoot, env = process.env) {
  await new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, { cwd, env, stdio: "inherit", shell: false });
    child.once("error", rejectRun);
    child.once("exit", (code, signal) => {
      if (code === 0) {
        resolveRun();
        return;
      }
      rejectRun(new Error(`${command} exited with ${code ?? signal}`));
    });
  });
}

const goEnvironment = releaseGoEnvironment();

await run("go", ["test", "./..."], serviceRoot, goEnvironment);
await mkdir(binariesRoot, { recursive: true });
await rm(executablePath, { force: true });

const linkerFlags = platform.goos === "windows" ? "-s -w -H=windowsgui" : "-s -w";
await run(
  "go",
  ["build", "-trimpath", `-ldflags=${linkerFlags}`, "-o", executablePath, "."],
  serviceRoot,
  {
    ...goEnvironment,
    CGO_ENABLED: "0",
    GOOS: platform.goos,
    GOARCH: platform.goarch,
  },
);
await run("go", ["version", executablePath], projectRoot, goEnvironment);

console.log(`Tauri sidecar ready: ${executablePath}`);
