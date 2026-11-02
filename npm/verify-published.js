#!/usr/bin/env node
"use strict";

// Step 4 of the release sequence — THE MOST IMPORTANT GATE.
//
// Re-download the 6 platform packages FROM THE REGISTRY, re-hash the binary inside
// each, and compare against the checksum table in the wrapper that has NOT been
// published yet. Catches two things nothing else catches: a binary altered on the
// registry side, and a mistake in the build/upload itself.
//
//   node npm/verify-published.js [--registry <url>]
//
// Deliberately does NOT compare against the local dist/: dist/ is what was just
// uploaded, so comparing it with itself proves nothing. The only source with any value
// at this step is what the registry hands a real user.
//
// ONLY MEANINGFUL INSIDE THE RELEASE JOB. It compares registry packages against the
// checksum table in the dist/ of the SAME build. Run on a developer machine against a
// locally built dist/ it will almost certainly differ, and differ harmlessly: a
// different Go patch release alone is enough to produce a different binary. A
// "does not match" then reads as a security incident when nothing happened — and an
// alarm that cries wolf is an alarm that gets ignored the day it is right.

const { spawnSync } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");

const CLI = "sepay";
const root = path.resolve(__dirname, "..");

// Synchronous: process.exit() discards a queued async pipe write (see npm/bin/sepay.js).
// This script runs after the 6 platform packages are published, so a failure whose
// reason never reaches the log costs a version number.
function bail(text) {
  fs.writeSync(2, text);
  process.exit(1);
}

const wrapperManifest = path.join(root, "dist", "npm", CLI, "package.json");
if (!fs.existsSync(wrapperManifest)) {
  bail(
    "Error: " + path.relative(root, wrapperManifest) + " does not exist.\n" +
      "  Run: node npm/build-packages.js\n"
  );
}
const wrapper = JSON.parse(fs.readFileSync(wrapperManifest, "utf8"));

const i = process.argv.indexOf("--registry");
const registry = i !== -1 ? process.argv[i + 1] : null;

const version = wrapper.version;
const hashes = (wrapper.sepay && wrapper.sepay.binaryHashes) || {};
const names = Object.keys(wrapper.optionalDependencies || {});

if (names.length !== 6) {
  bail("Error: wrapper does not declare 6 platform packages.\n");
}

if (!process.env.CI) {
  process.stdout.write(
    "Note: not running in CI. A mismatch below is most likely a different Go\n" +
      "  patch release on this machine, not a tampered package. This check is\n" +
      "  only conclusive on the dist/ produced by the release job itself.\n"
  );
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "sepay-verify-"));
const problems = [];

// ONE shared deadline for all 6 packages. With a 10-minute deadline each, a permanent
// failure would take an hour to report.
const wi = process.argv.indexOf("--max-wait");
const MAX_WAIT_S = wi !== -1 ? Number(process.argv[wi + 1]) : 600;
// A bad value must fail here. NaN makes `Date.now() >= DEADLINE` permanently false, and
// the retry loop below has no other time-based exit — the job would hang until the
// runner's own limit instead of reporting anything.
if (!isFinite(MAX_WAIT_S) || MAX_WAIT_S <= 0) {
  bail("Error: --max-wait needs a positive number of seconds.\n");
}
const DEADLINE = Date.now() + MAX_WAIT_S * 1000;

// Only these codes are worth giving up on immediately: they do not change with time.
// EVERYTHING ELSE waits, including an unrecognised code. A safety gate's default is
// patience. The real error while the registry is still propagating is ETARGET
// ("package exists, this version not visible yet") — which contains no "404".
const PERMANENT = /E401|E403|ENEEDAUTH|EPERM|EUSAGE|EINVALIDTAGNAME/;

names.forEach(function (name) {
  const key = name.replace(/^@[^/]+\/cli-/, "");
  const binName = key.indexOf("win32") === 0 ? CLI + ".exe" : CLI;

  // --prefer-online: without it npm may serve from the local cache, and this step
  // would be comparing the package just built against itself instead of against what
  // the registry returns.
  const args = [
    "pack", name + "@" + version, "--pack-destination", tmp, "--prefer-online",
  ];
  if (registry) args.push("--registry", registry);

  // The retry window is measured in MINUTES, not seconds. The registry accepts the
  // package (HTTP 200 on publish) but the version takes a while to be readable
  // everywhere; measured at 3-5 minutes on registry.npmjs.org, and unevenly across
  // packages — one run had 3 of 6 ready and 3 not.
  //
  // A false failure here is the most expensive failure in the pipeline: the version
  // number is burnt and all 7 packages have to be bumped. Waiting a few more minutes
  // is far cheaper. Conversely, the check must NOT be dropped for speed: it is the
  // only gate that sees what the registry actually serves.
  const started = Date.now();
  let packed;
  for (let attempt = 1; ; attempt++) {
    packed = spawnSync("npm", args, { encoding: "utf8", shell: false, cwd: tmp });
    if (packed.status === 0) break;

    // Give up early only on a legible error that is not "not there yet". An illegible
    // error still waits.
    const why = String(packed.stderr || "");
    if (PERMANENT.test(why)) break;
    if (Date.now() >= DEADLINE) break;

    const backoff = Math.min(15, attempt * 2);
    if (attempt === 1 || attempt % 5 === 0) {
      const elapsed = Math.round((Date.now() - started) / 1000);
      const left = Math.max(0, Math.round((DEADLINE - Date.now()) / 1000));
      process.stdout.write(
        "  " + name + ": not served by the registry yet, waited " + elapsed + "s (" + left + "s left)\n"
      );
    }
    // Synchronous sleep with no dependency and no child process. spawnSync("sleep")
    // returns instantly instead of throwing when the binary is absent, which turns
    // this backoff into a tight loop against the registry.
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, backoff * 1000);
  }
  if (packed.status !== 0) {
    problems.push(name + ": could not download from the registry — " + String(packed.stderr).trim());
    return;
  }

  const tgz = fs
    .readdirSync(tmp)
    .filter(function (f) {
      return f.indexOf(key) !== -1 && f.endsWith(".tgz");
    })
    .pop();
  if (!tgz) {
    problems.push(name + ": npm pack produced no tarball");
    return;
  }

  const got = spawnSync("tar", ["-xzOf", path.join(tmp, tgz), "package/bin/" + binName], {
    shell: false,
    maxBuffer: 512 * 1024 * 1024,
  });
  if (got.status !== 0 || !got.stdout || got.stdout.length === 0) {
    problems.push(name + ": the published tarball has no bin/" + binName);
    return;
  }

  const actual = crypto.createHash("sha256").update(got.stdout).digest("hex");
  const expected = hashes[key];
  if (actual !== expected) {
    problems.push(
      name +
        ": the binary on the registry does not match the wrapper's table\n" +
        "    Expected: " +
        expected +
        "\n    Actual:   " +
        actual
    );
    return;
  }
  process.stdout.write("  " + name + "@" + version + "  " + actual.slice(0, 16) + "… ok\n");
});

fs.rmSync(tmp, { recursive: true, force: true });

if (problems.length > 0) {
  bail(
    "Error: do NOT publish the wrapper.\n" +
      problems.map(function (p) { return "  " + p + "\n"; }).join("")
  );
}

process.stdout.write("All 6 published binaries match the wrapper's checksum table.\n");
