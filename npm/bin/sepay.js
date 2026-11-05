#!/usr/bin/env node
"use strict";

// The gatekeeper of the npm channel.
//
// Three things, in order: find the platform package matching this machine, check the
// binary's SHA-256 against the table baked into this package's own package.json, and
// only then exec it.
//
// Two deliberate departures from esbuild's launcher. Do not "fix" them back:
//
//   1. esbuild compares checksums in install.js, a script that runs at install time.
//      This package runs no install script at all, so the comparison moves to run
//      time, i.e. here. The reason: a customer who installs with --ignore-scripts
//      must actually be protected by that flag.
//
//   2. esbuild has ESBUILD_BINARY_PATH to point at a different binary. There is no
//      equivalent here and none may be added: one such variable disables the entire
//      checksum chain in a single line.
//
// No third-party dependency. Each one is another entry to track for vulnerabilities
// (PCI-DSS 6.3.2) and another way in.

const { spawn } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");

const pkg = require("../package.json");

const SUPPORTED = [
  "darwin-x64",
  "darwin-arm64",
  "linux-x64",
  "linux-arm64",
  "win32-x64",
  "win32-arm64",
];

const DOCS = "https://developer.sepay.vn/en/agents/cli";

// Message style: English, no colour, lowercase right after
// "Error:", detail lines indented two spaces, everything on stderr.
function fail(message) {
  const details = Array.prototype.slice.call(arguments, 1);
  let out = "Error: " + message + "\n";
  for (let i = 0; i < details.length; i++) {
    out += "  " + details[i] + "\n";
  }
  // A write to a pipe is asynchronous on POSIX, and process.exit() below discards
  // whatever is still queued, so this has to be a synchronous write or the customer
  // gets a bare exit 1 with no reason. `npx sepay` in any CI pipes stderr.
  try {
    fs.writeSync(2, out);
  } catch (e) {
    process.stderr.write(out);
  }
  process.exit(1);
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

// Cache of checksum results.
//
// The binary is ~13 MB, so hashing costs roughly 50 ms, and a CLI gets called inside
// shell loops.
//
// NOT os.tmpdir(): on a multi-user machine everyone can write there, so a planted
// cache file turns "verified" into something an attacker asserts about themselves.
// The cache lives in the user's own directory, AND is only trusted when the binary
// belongs to that same user — a global install (root-owned binary) always re-hashes,
// because there the cache sits on the weaker side of the binary. Windows has no
// usable uid here, so it does not cache at all.

function cacheFile() {
  const base = process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache");
  return path.join(base, "sepay", "verified.json");
}

function cacheKey(st, expected) {
  // ctime is required, not redundant: no system call sets it, so every write to the
  // file pushes it forward — including a binary overwritten in place at the same size
  // with `touch -r` restoring mtime. Drop ctime and that is exactly the path you open,
  // and the cache will assert a swapped binary has been verified.
  return [st.dev, st.ino, st.size, Math.floor(st.mtimeMs), Math.floor(st.ctimeMs), expected].join(":");
}

function ownedByUs(st) {
  return typeof process.getuid === "function" && st.uid === process.getuid();
}

function alreadyVerified(binPath, expected) {
  if (process.platform === "win32") return false;
  try {
    const st = fs.statSync(binPath);
    if (!ownedByUs(st)) return false;
    const file = cacheFile();
    const meta = fs.lstatSync(file);
    if (!meta.isFile() || !ownedByUs(meta)) return false;
    const data = JSON.parse(fs.readFileSync(file, "utf8"));
    return data[binPath] === cacheKey(st, expected);
  } catch (e) {
    return false;
  }
}

// `st` MUST be the file state read BEFORE hashing, not re-read here. Re-reading after
// hashing records the pair (NEW state, OLD hash): anyone who swaps the file between
// those two operations gets a permanent certificate from the cache, and every later
// run skips hashing. A momentary race becomes a standing hole.
function rememberVerified(binPath, expected, st) {
  if (process.platform === "win32") return;
  try {
    if (!ownedByUs(st)) return;
    const file = cacheFile();
    fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
    let data = {};
    try {
      const meta = fs.lstatSync(file);
      if (meta.isFile() && ownedByUs(meta)) {
        data = JSON.parse(fs.readFileSync(file, "utf8"));
      }
    } catch (e) {
      data = {};
    }
    data[binPath] = cacheKey(st, expected);
    // Write to a temp file and rename. A direct write follows a symlink, so a symlink
    // planted at the cache path would have its target overwritten; and `mode` only
    // applies when CREATING a file, so a pre-existing world-readable file keeps its
    // permissions. Renaming replaces the inode, and both problems disappear.
    const temp = file + "." + process.pid + ".tmp";
    fs.writeFileSync(temp, JSON.stringify(data), { mode: 0o600 });
    fs.renameSync(temp, file);
  } catch (e) {
    // A broken cache only makes the command slower. It must never make it fail.
  }
}

function main() {
  const key = process.platform + "-" + process.arch;

  if (SUPPORTED.indexOf(key) === -1) {
    fail(
      "unsupported platform: " + process.platform + " " + process.arch + ".",
      "Supported: darwin/x64, darwin/arm64, linux/x64, linux/arm64, win32/x64, win32/arm64.",
      "Installation and supported platforms: " + DOCS
    );
  }

  // The scope comes from what this wrapper declares, not from a literal repeated here.
  // One source of truth, and a build under a different namespace needs no change.
  const suffix = "/cli-" + key;
  const matches = Object.keys(pkg.optionalDependencies || {}).filter(function (n) {
    return n.slice(-suffix.length) === suffix;
  });
  if (matches.length !== 1) {
    fail(
      "this package declares " + matches.length + " platform dependencies for " + key + ", expected 1.",
      "It was not built by the release pipeline."
    );
  }
  const platformPkg = matches[0];
  const binName = process.platform === "win32" ? "sepay.exe" : "sepay";

  let pkgDir;
  try {
    pkgDir = path.dirname(require.resolve(platformPkg + "/package.json"));
  } catch (e) {
    // Two causes, not one. A message naming only the first sends the user round a
    // reinstall loop that cannot succeed when the real problem is that the package is
    // not on the registry.
    fail(
      "the sepay binary for " + process.platform + "/" + process.arch + " is not installed.",
      "Expected package: " + platformPkg + "@" + pkg.version,
      "Usually optional dependencies were skipped (npm install --no-optional or",
      "--omit=optional). Reinstall with them enabled.",
      "If they were not skipped, that package is not on the registry. Check with:",
      "  npm view " + platformPkg + "@" + pkg.version
    );
  }

  // Resolve every symlink FIRST, then check the path is inside the package. Checking
  // before resolving checks nothing: a symlink named bin/sepay pointing at /tmp/evil
  // is still "inside the package" to a string comparison.
  let realDir;
  let binPath;
  const expectedPath = function () {
    return path.join(realDir, "bin", binName);
  };
  try {
    realDir = fs.realpathSync(pkgDir);
    binPath = fs.realpathSync(expectedPath());
  } catch (e) {
    fail(
      "the sepay binary is missing from " + platformPkg + ".",
      "Expected file: bin/" + binName,
      "Reinstall the package."
    );
  }
  if (binPath !== expectedPath()) {
    fail(
      "the sepay binary resolves outside its package.",
      "Expected: " + expectedPath(),
      "Actual:   " + binPath,
      "Do not run it. Reinstall the package."
    );
  }

  // A platform package left over from an earlier version is an ordinary install
  // artefact, not a sign of tampering. Uncaught here it falls through to the checksum
  // comparison and the user is told "do not run the binary" — which reads as a
  // security incident.
  try {
    const platformVersion = require(path.join(realDir, "package.json")).version;
    if (platformVersion !== pkg.version) {
      fail(
        "the installed " + platformPkg + " is version " + platformVersion + ", not " + pkg.version + ".",
        "This happens when a previous version is left behind by an upgrade.",
        "Remove node_modules and the lockfile entry, then reinstall."
      );
    }
  } catch (e) {
    // unreadable — let the checksum comparison below deal with it
  }

  // No checksum table means STOP, not run anyway. A wrapper built without this table
  // is a wrapper that can verify nothing — it has to fail loudly, not silently.
  const table = (pkg.sepay && pkg.sepay.binaryHashes) || {};
  const expected = table[key];
  if (typeof expected !== "string" || !/^[a-f0-9]{64}$/.test(expected)) {
    fail(
      "no checksum is recorded for " + key + " in this package.",
      "This package was not built by the release pipeline.",
      "Do not run the binary. Reinstall from a published version."
    );
  }

  if (!alreadyVerified(binPath, expected)) {
    let stBefore = null;
    try {
      stBefore = fs.statSync(binPath);
    } catch (e) {
      stBefore = null; // unreadable: only the cache is lost
    }
    const actual = sha256(binPath);
    if (actual !== expected) {
      fail(
        "checksum verification failed for the sepay binary.",
        "Expected: " + expected,
        "Actual:   " + actual,
        "Reinstall the package. If this persists, do not run the binary."
      );
    }
    if (stBefore) rememberVerified(binPath, expected, stBefore);
  }

  const env = Object.assign({}, process.env);
  delete env.NODE_OPTIONS; // NODE_OPTIONS=--require=/tmp/evil.js injects code into the
  delete env.NODE_PATH; // child process if that process is also Node.

  const child = spawn(binPath, process.argv.slice(2), {
    stdio: "inherit", // share the terminal + keyboard with the user
    shell: false, // explicit: with a shell, --ref "a & calc" becomes two commands
    env: env,
    windowsHide: true,
  });

  // On Windows `child.kill(sig)` is TerminateProcess, not a signal: the binary never
  // runs its own cleanup. A real Ctrl-C in a console is delivered by Windows to the
  // whole process group, so that path still shuts down cleanly — only the
  // child.kill/taskkill route from another program does not. Case 10 measures the real
  // Ctrl-C path; nobody has measured the other one.
  const forward = function (sig) {
    return function () {
      try {
        child.kill(sig);
      } catch (e) {
        // child already gone
      }
    };
  };
  process.on("SIGINT", forward("SIGINT"));
  process.on("SIGTERM", forward("SIGTERM"));

  child.on("error", function (err) {
    fail(
      "could not start the sepay binary.",
      err.code === "EACCES"
        ? "The file is not executable. Reinstall the package."
        : String(err.message),
      "Path: " + binPath
    );
  });

  // Pass the exit code through. Getting this wrong means a customer's CI reports
  // success while the command failed — with a payments CLI that is the worst kind of
  // bug. `sepay help exit-codes` is the contract this preserves.
  child.on("exit", function (code, signal) {
    if (signal) {
      const num = os.constants.signals[signal];
      process.exit(num ? 128 + num : 1);
    }
    process.exit(code === null ? 1 : code);
  });
}

// Never let Node print a raw stack trace at a customer.
try {
  main();
} catch (e) {
  fail("sepay could not start.", String((e && e.message) || e), "Reinstall the package.");
}
