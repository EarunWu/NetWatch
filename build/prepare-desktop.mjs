import { spawn } from "node:child_process";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const buildDirectory = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(buildDirectory, "..");
const viteCLI = join(projectRoot, "node_modules", "vite", "bin", "vite.js");

async function run(command, args) {
  await new Promise((resolveRun, rejectRun) => {
    const child = spawn(command, args, {
      cwd: projectRoot,
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
await run(process.execPath, [join(buildDirectory, "sidecar.mjs")]);
