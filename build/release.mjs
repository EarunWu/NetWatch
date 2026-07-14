import { mkdir, rm } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

const buildDirectory = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(buildDirectory, "..");
const serviceRoot = join(projectRoot, "service");
const outputRoot = join(projectRoot, "outputs");
const viteCLI = join(projectRoot, "node_modules", "vite", "bin", "vite.js");
const executableName = process.platform === "win32" ? "NetWatch.Service.exe" : "netwatch-service";
const executablePath = join(outputRoot, executableName);

async function run(command, args, cwd = projectRoot) {
  await new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, {
      cwd,
      env: process.env,
      stdio: "inherit",
      shell: false,
    });
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

await run(process.execPath, [viteCLI, "build"]);
await run(process.execPath, [join(buildDirectory, "sync-static.mjs")]);
await run("go", ["test", "./..."], serviceRoot);

await mkdir(outputRoot, { recursive: true });
await rm(executablePath, { force: true });

const linkerFlags = process.platform === "win32" ? "-s -w -H=windowsgui" : "-s -w";
await run(
  "go",
  ["build", "-trimpath", `-ldflags=${linkerFlags}`, "-o", executablePath, "."],
  serviceRoot,
);

console.log(`Release ready: ${executablePath}`);
