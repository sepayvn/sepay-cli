# Changelog

## [0.1.7](https://github.com/sepayvn/sepay-cli/compare/v0.1.6...v0.1.7) (2026-11-08)


### Bug Fixes

* **release:** sign and notarize macOS binaries ([b6454de](https://github.com/sepayvn/sepay-cli/commit/b6454de13ccfd3b6673744b0e701d9703b660519))

## [0.1.6](https://github.com/sepayvn/sepay-cli/compare/v0.1.5...v0.1.6) (2026-11-07)


### Documentation

* **npm:** edit README ([e059f4a](https://github.com/sepayvn/sepay-cli/commit/e059f4a154ef87e7e10bb5f986d353a9f806a6b0))
* **npm:** edit README ([10a84f1](https://github.com/sepayvn/sepay-cli/commit/10a84f1b26bb44a1f8fb8ed2e6f3242a82e3270c))

## [0.1.5](https://github.com/sepayvn/sepay-cli/compare/v0.1.4...v0.1.5) (2026-11-07)


### Documentation

* **npm:** edit README ([6d11044](https://github.com/sepayvn/sepay-cli/commit/6d11044970a7bac7c4c6f719b759c2aace24f3b7))

## [0.1.4](https://github.com/sepayvn/sepay-cli/compare/v0.1.3...v0.1.4) (2026-11-06)


### Features

* **auth:** copy the login link and code with keys, and follow the locale on the login screen ([fb778ad](https://github.com/sepayvn/sepay-cli/commit/fb778ad0f2666665287fbf94ad98979a652a36c4))
* **webhook:** replay on production with a preview and a typed --force ([fb778ad](https://github.com/sepayvn/sepay-cli/commit/fb778ad0f2666665287fbf94ad98979a652a36c4))


### Bug Fixes

* **webhook:** say plainly what the listen banner delivers ([fb778ad](https://github.com/sepayvn/sepay-cli/commit/fb778ad0f2666665287fbf94ad98979a652a36c4))

## [0.1.3](https://github.com/sepayvn/sepay-cli/compare/v0.1.2...v0.1.3) (2026-11-05)


### ⚠ BREAKING CHANGES

* **version:** paycode get/set are now paycode list/update; webhook listen runs on sandbox only (--env production exits 2); sepay docs is replaced by sepay help <topic>.

### Features

* **auth:** auth ls now runs auth status ([84a218f](https://github.com/sepayvn/sepay-cli/commit/84a218f1033a0ecc0816731f942f6166920059b3))
* **version:** version --json drops the v prefix and adds go_version, os, arch ([84a218f](https://github.com/sepayvn/sepay-cli/commit/84a218f1033a0ecc0816731f942f6166920059b3))


### Bug Fixes

* **listen:** release the .own marker handle so Windows can remove it ([e4fc59b](https://github.com/sepayvn/sepay-cli/commit/e4fc59bb9eaca913da427da89632a092de82d590))
* **network:** recognise connection refused on Windows ([e4fc59b](https://github.com/sepayvn/sepay-cli/commit/e4fc59bb9eaca913da427da89632a092de82d590))

## [0.1.2](https://github.com/sepayvn/sepay-cli/compare/v0.1.1...v0.1.2) (2026-11-03)


### Bug Fixes

* **release:** publish 0.1.2, 0.1.1 was never published ([a4fffce](https://github.com/sepayvn/sepay-cli/commit/a4fffce989d45a552fdfdf8755deabe1ca1ee9af))

## [0.1.1](https://github.com/sepayvn/sepay-cli/compare/v0.1.0...v0.1.1) (2026-11-03)


### Features

* **install:** add an install script for macOS and Linux ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))
* **paycode:** add paycode create ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))
* **release:** publish a Homebrew cask and a Scoop manifest with each release ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))
* **update:** show the upgrade command for the channel sepay was installed from ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))


### Bug Fixes

* **auth:** keep the session when several commands renew the token at the same time ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))
* **auth:** keep the token in a 0600 file at login when the OS keychain is unusable ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))
* explain network failures in plain words ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))
* **version:** report the installed version when built with go install ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))
* **webhook:** print webhook and transaction IDs in full ([v0.1.1](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.1))

## 0.1.0 (2026-11-02)


### Features

* initial release ([v0.1.0](https://github.com/sepayvn/sepay-cli/releases/tag/v0.1.0))
