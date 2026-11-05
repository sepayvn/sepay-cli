# Changelog

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
