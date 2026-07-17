export const GO_RELEASE_TOOLCHAIN = "go1.26.5";

export function releaseGoEnvironment(overrides = {}) {
  return {
    ...process.env,
    // Release artifacts must not silently inherit an older vulnerable Go
    // installation from PATH. Go 1.21+ downloads this exact toolchain when it
    // is not already available locally.
    GOTOOLCHAIN: GO_RELEASE_TOOLCHAIN,
    ...overrides,
  };
}
