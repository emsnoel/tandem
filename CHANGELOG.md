# Changelog

## [0.4.0](https://github.com/emsnoel/tandem/compare/v0.3.4...v0.4.0) (2026-11-01)


### Features

* install and run tandem without jq ([0448abe](https://github.com/emsnoel/tandem/commit/0448abee0f8fbfe0c6a77d43e885fb923abdf583))

## [0.3.4](https://github.com/emsnoel/tandem/compare/v0.3.3...v0.3.4) (2026-11-01)


### Bug Fixes

* stop calling each next task of a build a resume after a stop ([8f91976](https://github.com/emsnoel/tandem/commit/8f91976cb50a3f7634833af49207553580619bec))

## [0.3.3](https://github.com/emsnoel/tandem/compare/v0.3.2...v0.3.3) (2026-11-01)


### Bug Fixes

* refuse a missing handoff prompt template instead of sending an empty prompt ([22589bb](https://github.com/emsnoel/tandem/commit/22589bbc7ab9913adde41eb1eadd2040c4a925b6))

## [0.3.2](https://github.com/emsnoel/tandem/compare/v0.3.1...v0.3.2) (2026-11-01)


### Bug Fixes

* let tandem-pair explain an unresolved builder, and check every pair file for jq ([0783fef](https://github.com/emsnoel/tandem/commit/0783fefa1d9beac66d606d1e99cfd01e79bfb410))
* report a running reviewer's unenforced model once ([d1254a3](https://github.com/emsnoel/tandem/commit/d1254a3f2b1eefa4a7c69a15f01fd1019aca661d))

## [0.3.1](https://github.com/emsnoel/tandem/compare/v0.3.0...v0.3.1) (2026-10-31)


### Bug Fixes

* keep showing a trust gate that failed to write after doctor passes ([ba8a4ff](https://github.com/emsnoel/tandem/commit/ba8a4ff315df6a238f343f66d62df23f82561103))
* list --problems in tandem doctor's usage line ([ee686a8](https://github.com/emsnoel/tandem/commit/ee686a897bb1543d59c3d97e6f694d47069d5003))
* say Herdr did not answer when doctor's Herdr probe times out ([fda9483](https://github.com/emsnoel/tandem/commit/fda948366aa60820cc31bfc29a8c0232d48d99c9))
* show a trust gate warning once when doctor has already reported it ([2a52a8a](https://github.com/emsnoel/tandem/commit/2a52a8a3ad520be7e703e465860cab17da7ad47a))
* show tandem doctor's warnings when tandem-pair passes the check ([854307b](https://github.com/emsnoel/tandem/commit/854307b54c8e7f927a7d2ca24cbb80d781f082b1))
* skip doctor checks for roles that already run, as tandem-pair does ([21958a0](https://github.com/emsnoel/tandem/commit/21958a0858f8222cf135194c9423697122f31aa9))
* tell the user to re-run install.sh when tandem doctor is missing ([cf6e367](https://github.com/emsnoel/tandem/commit/cf6e3678b57e938de656c3b9c45250c5d8d7919d))

## [0.3.0](https://github.com/emsnoel/tandem/compare/v0.2.0...v0.3.0) (2026-10-31)


### Features

* add tandem doctor to find setup problems before pairing ([ba5ad04](https://github.com/emsnoel/tandem/commit/ba5ad0491658851a5805cac45ca08c3f7a9cfd54))
* **pair:** run tandem doctor before opening any pane ([da20a02](https://github.com/emsnoel/tandem/commit/da20a020611c36299ed50e4165a14988af657ca7))


### Bug Fixes

* leave Codex's home untouched on a dry-run hook check ([0e48a9a](https://github.com/emsnoel/tandem/commit/0e48a9a4f0edbaa41840fea758ef5847a01b308a))
* let prelaunch accept a kind named on tandem-pair's command line ([83b6f31](https://github.com/emsnoel/tandem/commit/83b6f31b650a893166e41b49ad02b2309f4eba3b))

## [0.2.0](https://github.com/emsnoel/tandem/compare/v0.1.0...v0.2.0) (2026-10-26)


### Features

* add install.sh --uninstall ([0728ee8](https://github.com/emsnoel/tandem/commit/0728ee88f3cee57be97b7eb0d62c13509057c9dc))
* add install.sh --uninstall ([efee341](https://github.com/emsnoel/tandem/commit/efee3413fb191aa7ee55719337aa82b4c764c68d))

## 0.1.0 (2026-10-25)


### Features

* tandem, a planner and builder harness for Herdr ([f7f24df](https://github.com/emsnoel/tandem/commit/f7f24df00ad27dac1b3bec9a8749126024148a23))


### Bug Fixes

* ship third-party license notices in release tarballs ([43473b4](https://github.com/emsnoel/tandem/commit/43473b413c6084c3d94d7a88f2398321c0b0b676))
* ship third-party license notices in release tarballs ([0b2ca6f](https://github.com/emsnoel/tandem/commit/0b2ca6fa59213c6396686a9108a88565d9129cf6))
