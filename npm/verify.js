#!/usr/bin/env node
"use strict";

// Self-check dist/npm/ before packing or publishing. The npm channel's equivalent of
// `goreleaser check`: runs locally, in CI, and as step 1 of the release job.
//
//   node npm/verify.js
//
// Principle: everything here fails loudly. A wrapper missing a checksum, missing a
// platform, or out of step with its binary is something that otherwise only surfaces
// on a customer's machine.

const { spawnSync } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const path = require("path");

const CLI = "sepay";
const EXPECTED = [
  "darwin-arm64",
  "darwin-x64",
  "linux-arm64",
  "linux-x64",
  "win32-arm64",
  "win32-x64",
];

const root = path.resolve(__dirname, "..");
const out = path.join(root, "dist", "npm");

const problems = [];
function check(ok, message) {
  if (!ok) problems.push(message);
  return ok;
}

function readJson(file) {
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

// Synchronous: process.exit() discards a queued async pipe write (see npm/bin/sepay.js).
function bail(text) {
  fs.writeSync(2, text);
  process.exit(1);
}

if (!fs.existsSync(out)) {
  bail("Error: dist/npm/ does not exist.\n  Run: node npm/build-packages.js\n");
}

const wrapperDir = path.join(out, CLI);
const wrapper = readJson(path.join(wrapperDir, "package.json"));
const version = wrapper.version;

check(!!version && version !== "0.0.0", "wrapper version is still 0.0.0 — build did not stamp it");
check(!wrapper.scripts, "wrapper declares scripts — no script may run at install time");
check(!!wrapper.repository && !!wrapper.repository.url, "wrapper has no repository (needed for provenance)");
check(
  Array.isArray(wrapper.files) && wrapper.files.length === 1 && wrapper.files[0] === "bin",
  "wrapper files must be exactly [\"bin\"]"
);

// The README is what npmjs.com renders for this version, so its install line has to
// name this version and must never ship the unsubstituted token.
const readme = fs.existsSync(path.join(wrapperDir, "README.md"))
  ? fs.readFileSync(path.join(wrapperDir, "README.md"), "utf8")
  : "";
check(readme !== "", "wrapper has no README.md");
["__VERSION__", "__PKG__", "__SCOPE__"].forEach(function (token) {
  check(readme.indexOf(token) === -1, "wrapper README still contains the " + token + " token");
});
check(
  readme.indexOf(wrapper.name + "@" + version) !== -1,
  "wrapper README does not tell the reader to install " + version
);
check(fs.existsSync(path.join(wrapperDir, "LICENSE")), "wrapper has no LICENSE");

const hashes = (wrapper.sepay && wrapper.sepay.binaryHashes) || {};
const optional = wrapper.optionalDependencies || {};

// The scope is read off the artifact, not from the environment: `node npm/verify.js`
// must reach the same verdict whatever the shell that built it had exported.
const firstOptional = Object.keys(optional)[0] || "";
const SCOPE = firstOptional.indexOf("/") !== -1 ? firstOptional.split("/")[0] : "@sepayjsc";

// All 6 combinations: none missing, none extra, none duplicated.
EXPECTED.forEach(function (key) {
  const pkgName = SCOPE + "/cli-" + key;
  const dir = path.join(out, "cli-" + key);

  if (!check(fs.existsSync(dir), "missing platform package: " + pkgName)) return;

  const manifest = readJson(path.join(dir, "package.json"));
  const parts = key.split("-");
  const binName = parts[0] === "win32" ? CLI + ".exe" : CLI;
  const binPath = path.join(dir, "bin", binName);

  check(manifest.name === pkgName, pkgName + ": name is " + manifest.name);
  check(manifest.version === version, pkgName + ": version " + manifest.version + " != wrapper " + version);
  check(!manifest.scripts, pkgName + ": declares scripts");
  check(!manifest.bin, pkgName + ": declares bin — only the wrapper may");
  check(String(manifest.os) === parts[0], pkgName + ": os is " + JSON.stringify(manifest.os));
  check(String(manifest.cpu) === parts[1], pkgName + ": cpu is " + JSON.stringify(manifest.cpu));
  check(optional[pkgName] === version, pkgName + ": not pinned to " + version + " in optionalDependencies");

  check(fs.existsSync(path.join(dir, "LICENSE")), pkgName + ": no LICENSE (Apache-2.0 4a)");

  if (!check(fs.existsSync(binPath), pkgName + ": missing bin/" + binName)) return;

  const stat = fs.statSync(binPath);
  check(stat.size > 0, pkgName + ": binary is empty");
  if (parts[0] !== "win32") {
    check((stat.mode & 0o111) !== 0, pkgName + ": binary is not executable");
  }
  check(
    /^[a-f0-9]{64}$/.test(hashes[key] || ""),
    pkgName + ": no checksum recorded in the wrapper"
  );
  check(
    hashes[key] === sha256(binPath),
    pkgName + ": recorded checksum does not match the shipped binary"
  );
});

Object.keys(hashes).forEach(function (key) {
  check(EXPECTED.indexOf(key) !== -1, "unexpected checksum entry: " + key);
});
Object.keys(optional).forEach(function (name) {
  check(
    EXPECTED.indexOf(name.replace(SCOPE + "/cli-", "")) !== -1,
    "unexpected optional dependency: " + name
  );
});

// The mandatory cross-check: the npm package's version must match the version BAKED
// INTO the binary. This is the only step that proves both directions — the right
// version of the package, and the binary inside it really being that version's binary
// rather than a leftover in dist/.
const nativeKey = process.platform + "-" + process.arch;
if (EXPECTED.indexOf(nativeKey) !== -1) {
  const nativeBinName = nativeKey.indexOf("win32") === 0 ? CLI + ".exe" : CLI;
  const nativeBin = path.join(out, "cli-" + nativeKey, "bin", nativeBinName);
  // A missing file must SHOUT. Skipping it silently turns this mandatory cross-check
  // into a step that is always green on exactly the machines it never ran on.
  if (!fs.existsSync(nativeBin)) {
    problems.push("cannot cross-check the version: " + nativeBin + " is missing");
  } else {
    const r = spawnSync(nativeBin, ["version", "--json"], { encoding: "utf8", shell: false });
    if (r.status !== 0) {
      problems.push("could not run the " + nativeKey + " binary: " + String(r.stderr).trim());
    } else {
      let reported;
      try {
        reported = JSON.parse(r.stdout).version;
        check(
          reported === version,
          "binary reports version " + reported + " but the package says " + version
        );
      } catch (e) {
        problems.push("`" + CLI + " version --json` did not return JSON");
      }
    }
  }
} else {
  process.stdout.write("Note: " + nativeKey + " is not a target — skipping the run-the-binary check.\n");
}

if (problems.length > 0) {
  bail(
    "Error: dist/npm/ is not fit to publish.\n" +
      problems.map(function (p) { return "  " + p + "\n"; }).join("")
  );
}

process.stdout.write("verify: 7 packages, 6 platforms, all checksums match, version " + version + ".\n");
