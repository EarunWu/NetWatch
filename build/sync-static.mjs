import { cp, mkdir, readdir, rm, stat } from "node:fs/promises";
import { dirname, isAbsolute, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const buildDirectory = dirname(fileURLToPath(import.meta.url));
const projectRoot = resolve(buildDirectory, "..");
const source = resolve(projectRoot, "dist", "client");
const destination = resolve(projectRoot, "service", "web");
const expectedDestination = resolve(projectRoot, "service", "web");
const destinationFromRoot = relative(projectRoot, destination);

if (
  destination !== expectedDestination ||
  destinationFromRoot.startsWith("..") ||
  isAbsolute(destinationFromRoot)
) {
  throw new Error(`Refusing to replace unexpected directory: ${destination}`);
}

const sourceInfo = await stat(source).catch(() => null);
if (!sourceInfo?.isDirectory()) {
  throw new Error(`Static export not found at ${source}. Run npm run build:web first.`);
}

await rm(destination, { recursive: true, force: true });
await mkdir(destination, { recursive: true });
await cp(source, destination, { recursive: true });

const files = await readdir(destination, { recursive: true });
if (!files.includes("index.html")) {
  throw new Error("Static export is missing index.html");
}

console.log(`Synced ${files.length} dashboard entries to ${destination}`);
