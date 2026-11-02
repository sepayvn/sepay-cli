#!/usr/bin/env node
"use strict";

// Build the 7 npm packages from GoReleaser's dist/ into dist/npm/.
//
//   node npm/build-packages.js                       local build
//   node npm/build-packages.js --require-signature   demand a cosign signature (CI)
//
// Trust chain. The checksum table embedded in the wrapper must NOT certify itself; it
// has to be derivable from a signed file:
//
//   1. verify the cosign signature over checksums.txt, identity pinned
//   2. compare each archive against checksums.txt
//   3. unpack, and hash THE BINARY TAKEN OUT OF the verified archive
//   4. embed that hash in the wrapper
//
// Step 3 deliberately does NOT hash the loose binary in dist/: the loose binary is not
// listed in checksums.txt, so hashing it cuts the chain at its last link.

const { spawnSync } = require("child_process");
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");

// The repository whose release workflow signed checksums.txt (cosign identity).
// GitHub Actions sets GITHUB_REPOSITORY; outside CI the default applies.
const REPO = process.env.GITHUB_REPOSITORY || "sepayvn/sepay-cli";
// The published names. Defaults are the real ones; a dev namespace overrides them for
// one build without editing anything, so nothing has to be reverted before a release.
//   SEPAY_NPM_SCOPE=@sepay-dev SEPAY_NPM_PKG=@sepay-dev/cli node npm/build-packages.js
const SCOPE = process.env.SEPAY_NPM_SCOPE || "@sepayjsc";
const PKG = process.env.SEPAY_NPM_PKG || "sepay-cli";
// The command a user types and the binary's name inside the archive. NOT a published
// name, so it never changes with the namespace.
const CLI = "sepay";
// Stamped into npm/README.md at build time so the page npmjs.com renders for a
// version tells you to install that version.
const VERSION_TOKEN = "__VERSION__";
const PKG_TOKEN = "__PKG__";
const SCOPE_TOKEN = "__SCOPE__";

const root = path.resolve(__dirname, "..");
const dist = path.join(root, "dist");
const out = path.join(dist, "npm");

// GoReleaser calls 64-bit Intel amd64 and Windows windows. Node never uses either
// word. This table is the only place that is allowed to know both spellings.
const GOOS_TO_NODE = { darwin: "darwin", linux: "linux", windows: "win32" };
const GOARCH_TO_NODE = { amd64: "x64", arm64: "arm64" };

function die(message) {
  // Synchronous: process.exit() discards a queued async pipe write (see npm/bin/sepay.js).
  fs.writeSync(2, "Error: " + message + "\n");
  process.exit(1);
}

function readJson(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (e) {
    die("could not read " + path.relative(root, file) + ": " + e.message);
  }
}

function sha256(file) {
  return crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
}

function run(cmd, args) {
  return spawnSync(cmd, args, { encoding: "utf8", shell: false });
}

function verifySignature(requireIt) {
  const sums = path.join(dist, "checksums.txt");
  const sig = sums + ".sig";
  const pem = sums + ".pem";

  if (!fs.existsSync(sig) || !fs.existsSync(pem)) {
    if (requireIt) {
      die(
        "checksums.txt has no cosign signature.\n" +
          "  Expected: dist/checksums.txt.sig and dist/checksums.txt.pem\n" +
          "  A local `goreleaser --skip=publish` build does not sign. Do not publish from it."
      );
    }
    process.stdout.write(
      "Note: no cosign signature in dist/ — building unsigned (local only).\n" +
        "  CI runs with --require-signature, where this is an error.\n"
    );
    return false;
  }

  if (run("cosign", ["version"]).status !== 0) {
    die(
      "a cosign signature is present but cosign is not installed.\n" +
        "  Install cosign, or the trust chain cannot be checked."
    );
  }

  const r = run("cosign", [
    "verify-blob",
    "--certificate",
    pem,
    "--signature",
    sig,
    "--certificate-identity-regexp",
    "^https://github\\.com/" + REPO + "/\\.github/workflows/",
    "--certificate-oidc-issuer",
    "https://token.actions.githubusercontent.com",
    sums,
  ]);
  if (r.status !== 0) {
    die(
      "cosign could not verify checksums.txt.\n" +
        "  checksums.txt was not signed by the release workflow of " +
        REPO +
        ".\n" +
        "  Do not publish. " +
        String(r.stderr || "").trim()
    );
  }
  process.stdout.write("Signature verified (cosign, identity pinned to " + REPO + ").\n");
  return true;
}

function loadChecksums() {
  const file = path.join(dist, "checksums.txt");
  const table = {};
  const text = fs.readFileSync(file, "utf8");
  text.split("\n").forEach(function (line) {
    const m = line.match(/^([a-f0-9]{64})\s+(.+)$/);
    if (m) table[m[2].trim()] = m[1];
  });
  if (Object.keys(table).length === 0) die("checksums.txt is empty or unreadable.");
  return table;
}

function verifyArchive(archivePath, checksums) {
  const name = path.basename(archivePath);
  const expected = checksums[name];
  if (!expected) die("no checksum entry for " + name + " in checksums.txt.");
  const actual = sha256(archivePath);
  if (actual !== expected) {
    die(
      "checksum verification failed for " +
        name +
        ".\n  Expected: " +
        expected +
        "\n  Actual:   " +
        actual
    );
  }
}

// The archive also carries LICENSE, README.md and completions/, so the member is
// addressed by its exact name at the archive root rather than by position.
function extractBinary(archivePath, binName, tmpDir) {
  const target = path.join(tmpDir, binName);
  let r;
  if (archivePath.endsWith(".zip")) {
    r = spawnSync("unzip", ["-p", archivePath, binName], {
      shell: false,
      maxBuffer: 512 * 1024 * 1024,
    });
  } else {
    r = spawnSync("tar", ["-xzOf", archivePath, binName], {
      shell: false,
      maxBuffer: 512 * 1024 * 1024,
    });
  }
  if (r.status !== 0 || !r.stdout || r.stdout.length === 0) {
    die("could not extract " + binName + " from " + path.basename(archivePath) + ".");
  }
  fs.writeFileSync(target, r.stdout, { mode: 0o755 });
  return target;
}

function main() {
  const requireSignature = process.argv.indexOf("--require-signature") !== -1;

  const metadata = readJson(path.join(dist, "metadata.json"));
  const version = metadata.version;
  if (!version || /^v/.test(version)) {
    die("dist/metadata.json has no usable version (got: " + JSON.stringify(version) + ").");
  }

  verifySignature(requireSignature);
  const checksums = loadChecksums();

  // `type` is what separates the six cross-compiled binaries from everything else
  // GoReleaser writes (archives, checksums, signatures). Never filter by filename.
  const artifacts = readJson(path.join(dist, "artifacts.json"));
  const binaries = artifacts.filter(function (a) {
    return a.type === "Binary";
  });
  const archives = artifacts.filter(function (a) {
    return a.type === "Archive";
  });
  if (binaries.length !== 6) {
    die("expected 6 binaries in dist/artifacts.json, found " + binaries.length + ".");
  }

  fs.rmSync(out, { recursive: true, force: true });
  fs.mkdirSync(out, { recursive: true });
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "sepay-npm-"));

  const template = fs.readFileSync(
    path.join(__dirname, "platform-template", "package.json"),
    "utf8"
  );
  const hashes = {};
  const optional = {};
  const seen = {};

  binaries.forEach(function (bin) {
    const nodeOs = GOOS_TO_NODE[bin.goos];
    const nodeCpu = GOARCH_TO_NODE[bin.goarch];
    if (!nodeOs || !nodeCpu) die("no Node name for " + bin.goos + "/" + bin.goarch + ".");
    const key = nodeOs + "-" + nodeCpu;
    if (seen[key]) die("two binaries map to the same platform: " + key + ".");
    seen[key] = true;

    const archive = archives.filter(function (a) {
      return a.goos === bin.goos && a.goarch === bin.goarch;
    })[0];
    if (!archive) die("no archive for " + key + " in dist/artifacts.json.");

    const archivePath = path.join(root, archive.path);
    verifyArchive(archivePath, checksums);

    const binName = bin.goos === "windows" ? CLI + ".exe" : CLI;
    const extracted = extractBinary(archivePath, binName, tmpDir);
    const hash = sha256(extracted);

    // The loose binary in dist/ must match the one inside the archive. A mismatch
    // means dist/ still holds something from an earlier build — exactly what a rerun
    // without --clean produces.
    const loose = path.join(root, bin.path);
    if (fs.existsSync(loose) && sha256(loose) !== hash) {
      die(
        "dist/" +
          path.relative(dist, loose) +
          " does not match the binary inside " +
          path.basename(archivePath) +
          ".\n  dist/ is stale. Rebuild with `goreleaser release --clean`."
      );
    }

    const pkgName = SCOPE + "/cli-" + key;
    const pkgDir = path.join(out, "cli-" + key);
    fs.mkdirSync(path.join(pkgDir, "bin"), { recursive: true });
    fs.copyFileSync(extracted, path.join(pkgDir, "bin", binName));
    fs.chmodSync(path.join(pkgDir, "bin", binName), 0o755);
    // This package distributes the binary on its own, so Apache-2.0 section 4(a)
    // applies to it and not only to the wrapper.
    fs.copyFileSync(path.join(root, "LICENSE"), path.join(pkgDir, "LICENSE"));

    const manifest = template
      // The placeholder literal in platform-template/package.json, not the scope in
      // use: it must match that file, so it does NOT follow SCOPE. A mismatch ships the
      // placeholder as the name, which verify.js rejects.
      .replace("@sepayjsc/cli-PLATFORM", pkgName)
      .replace('"version": "0.0.0"', '"version": "' + version + '"')
      .replace("OS/CPU", nodeOs + "/" + nodeCpu)
      .replace('"os": ["OS"]', '"os": ["' + nodeOs + '"]')
      .replace('"cpu": ["CPU"]', '"cpu": ["' + nodeCpu + '"]');
    JSON.parse(manifest); // a broken template must die here, not at publish time
    fs.writeFileSync(path.join(pkgDir, "package.json"), manifest);

    hashes[key] = hash;
    optional[pkgName] = version; // exact version, in lockstep with the wrapper
    process.stdout.write("  " + pkgName + "  " + hash.slice(0, 16) + "…\n");
  });

  if (Object.keys(hashes).length !== 6) die("expected 6 platform packages.");

  // The wrapper is built last, once all 6 hashes exist.
  const wrapper = readJson(path.join(__dirname, "package.json"));
  wrapper.name = PKG;
  wrapper.version = version;
  wrapper.optionalDependencies = optional;
  wrapper.sepay = { binaryHashes: hashes };
  if (wrapper.scripts) die("the wrapper package must not declare any scripts.");

  const wrapperDir = path.join(out, CLI);
  fs.mkdirSync(path.join(wrapperDir, "bin"), { recursive: true });
  fs.writeFileSync(
    path.join(wrapperDir, "package.json"),
    JSON.stringify(wrapper, null, 2) + "\n"
  );
  fs.copyFileSync(path.join(__dirname, "bin", CLI + ".js"), path.join(wrapperDir, "bin", CLI + ".js"));
  fs.chmodSync(path.join(wrapperDir, "bin", CLI + ".js"), 0o755);
  const readme = fs.readFileSync(path.join(__dirname, "README.md"), "utf8");
  // The README is the page npmjs.com renders, so every name and version in it has to be
  // the ones actually published — not a literal that drifts when the namespace changes.
  [VERSION_TOKEN, PKG_TOKEN, SCOPE_TOKEN].forEach(function (token) {
    if (readme.indexOf(token) === -1) {
      die("npm/README.md no longer contains " + token + " — it would ship stale names.");
    }
  });
  fs.writeFileSync(
    path.join(wrapperDir, "README.md"),
    readme
      .split(VERSION_TOKEN).join(version)
      .split(PKG_TOKEN).join(PKG)
      .split(SCOPE_TOKEN).join(SCOPE)
  );
  fs.copyFileSync(path.join(root, "LICENSE"), path.join(wrapperDir, "LICENSE"));

  fs.rmSync(tmpDir, { recursive: true, force: true });
  process.stdout.write("  " + PKG + "  (wrapper, 6 hashes embedded)\n");
  process.stdout.write("Built 7 packages for " + version + " in dist/npm/.\n");
}

main()
