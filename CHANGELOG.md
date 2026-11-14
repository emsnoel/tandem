# Changelog

## [0.12.0](https://github.com/emsnoel/tandem/compare/v0.11.2...v0.12.0) (2026-11-14)


### Features

* read each pane before a pair is called ready ([6801473](https://github.com/emsnoel/tandem/commit/6801473fff46eeaa8bb44dd775f00f5044f9936b))

## [0.11.2](https://github.com/emsnoel/tandem/compare/v0.11.1...v0.11.2) (2026-11-14)


### Bug Fixes

* the Codex hook check warns again and follows plugin manifests ([47e499f](https://github.com/emsnoel/tandem/commit/47e499faeb33b1ee0b21b3c57edecf2a29ed22db))

## [0.11.1](https://github.com/emsnoel/tandem/compare/v0.11.0...v0.11.1) (2026-11-14)


### Bug Fixes

* refuse to launch Codex into its hook review screen ([80d6dfb](https://github.com/emsnoel/tandem/commit/80d6dfb966c72e284e8bf61b74de3d736d8237b9))

## [0.11.0](https://github.com/emsnoel/tandem/compare/v0.10.0...v0.11.0) (2026-11-14)


### Features

* ship a tandem-plan skill for the planner ([f25908e](https://github.com/emsnoel/tandem/commit/f25908ed6fca3ebe1da8864f7409cf483bf5e905))

## [0.10.0](https://github.com/emsnoel/tandem/compare/v0.9.0...v0.10.0) (2026-11-14)


### Features

* name the five stop cases in the task footer ([c1d2e31](https://github.com/emsnoel/tandem/commit/c1d2e3163af29e957662b3169210dfa866250133))
* tell the builder when to stop, and drop the unused executor agent ([6718eb5](https://github.com/emsnoel/tandem/commit/6718eb52b79281cd959de190c9a6c241f88829f5))

## [0.9.0](https://github.com/emsnoel/tandem/compare/v0.8.0...v0.9.0) (2026-11-14)


### Features

* let Claude Code or Copilot be the planner ([11bf222](https://github.com/emsnoel/tandem/commit/11bf222e6b4b5288d52bd439bf842f1d6a0d5208))
* tell the builder how to wait in every task prompt ([e8d9b68](https://github.com/emsnoel/tandem/commit/e8d9b686ec01ee211f0acd80e82d794dc2b3e6f8))

## [0.8.0](https://github.com/emsnoel/tandem/compare/v0.7.0...v0.8.0) (2026-11-13)


### Features

* carry the project settings and the ruling in every task prompt ([65bbdd6](https://github.com/emsnoel/tandem/commit/65bbdd641bb251b3196b63ccba8b5a88df67ecaa))

## [0.7.0](https://github.com/emsnoel/tandem/compare/v0.6.0...v0.7.0) (2026-11-09)


### ⚠ BREAKING CHANGES

* re-pair every session after upgrading. A bridge left running from the previous version exits on its next pass, and its run stops moving until you pair again. The last release with the legacy engine is v0.6.0.
* make durable the only build engine

### Features

* make durable the only build engine ([dd1ea7e](https://github.com/emsnoel/tandem/commit/dd1ea7e8a4b2f1161dd3f7c01670d3617c8bb8f4))
* refuse a leftover legacy build with a clear note ([471a234](https://github.com/emsnoel/tandem/commit/471a2343b510decc22ac0e62a4ddac06f241ef0a))


### Bug Fixes

* correct the text and refusals the final review found ([921721c](https://github.com/emsnoel/tandem/commit/921721ccd6da7d8797633709653a4f8e869fcc88))
* name the prompt an unknown delivery is about ([e16a09a](https://github.com/emsnoel/tandem/commit/e16a09aea574177396eb2ec65cb829ded023b109))
* release a budget block once the usage it waited for is known ([fd6dabd](https://github.com/emsnoel/tandem/commit/fd6dabd9e373178805837025e09dc708fa9ff956))
* wait for a builder's acknowledgement before pausing a send ([305fd77](https://github.com/emsnoel/tandem/commit/305fd77ab10dfe956fbd144f9381c468ebac12bc))

## [0.6.0](https://github.com/emsnoel/tandem/compare/v0.5.0...v0.6.0) (2026-11-06)


### Features

* end a run whose prompt was never read ([c92edfb](https://github.com/emsnoel/tandem/commit/c92edfbb84f5b4040b81d716863907403fcad93e))
* pause a run whose builder waits on a dialog ([94f0bf1](https://github.com/emsnoel/tandem/commit/94f0bf11c00e52a8f5245a06495bd5280ad8009e))
* pause a run whose started builder goes silent ([8c17ce3](https://github.com/emsnoel/tandem/commit/8c17ce3c9f867154667d65adb2b44cf76f90fa57))
* tell the planner what stopped a run and what to do ([f30a0c0](https://github.com/emsnoel/tandem/commit/f30a0c0d42e6eb5755f823591f30826deceed776))


### Bug Fixes

* abandon a prompt as an event and close every printed route ([cab538e](https://github.com/emsnoel/tandem/commit/cab538eb4e1d8df3272dea18230837b7bc5322b7))
* keep pause notes true when a fact cannot be read ([1ee19cc](https://github.com/emsnoel/tandem/commit/1ee19cc21080fdf2aab0d661285fc9fa7869f3bc))
* let a stopping bridge pass a removed mailbox quietly ([804fb76](https://github.com/emsnoel/tandem/commit/804fb764121ac0efda45f1a49787f693086cf39d))
* let a stuck handback still pause a silent builder ([5c634ca](https://github.com/emsnoel/tandem/commit/5c634cafeffd3f3ba1063e20ee21f56ae976df73))
* let the bridge note a prompt nobody acknowledged ([30cdfcb](https://github.com/emsnoel/tandem/commit/30cdfcbe093d74fc979d8c847f28bcf3cea26243))
* make pause notes accurate and fillable ([2f8da82](https://github.com/emsnoel/tandem/commit/2f8da82ae502bfb3812c340255cb43b5464b44fc))
* point the silent note at the log line for a tree tandem cannot capture ([01ac093](https://github.com/emsnoel/tandem/commit/01ac0937a6e147379acd360d17d1460e4f68a0bb))
* report a pause made by a tick that also failed ([5857158](https://github.com/emsnoel/tandem/commit/585715896cf13a793def79d83003a736e4653ed2))
* retry a pause note the planner did not receive ([4ae3d3c](https://github.com/emsnoel/tandem/commit/4ae3d3cb75daa779b7b705af02299986db55dce2))
* run one bridge per mailbox ([afe604a](https://github.com/emsnoel/tandem/commit/afe604ad49bd01178bdcbabbd1b162fe2ae1df87))
* say a failed check printed nothing instead of calling its log unreadable ([afd8e5f](https://github.com/emsnoel/tandem/commit/afd8e5fe3789b36532fa8294b972123e97d4d677))
* start the stopped note at the recover step ([8c3c2bd](https://github.com/emsnoel/tandem/commit/8c3c2bdba0c0f9f2774c3e17987ddf32d4d72179))
* state the silence window the run used in the silent note ([e28731d](https://github.com/emsnoel/tandem/commit/e28731de4ec10c1817970d3b2c6849543edc8f3e))

## [0.5.0](https://github.com/emsnoel/tandem/compare/v0.4.0...v0.5.0) (2026-11-05)


### ⚠ BREAKING CHANGES

* discontinue OpenCode support

### Features

* discontinue OpenCode support ([9fd05be](https://github.com/emsnoel/tandem/commit/9fd05be3b7c0084213236c0757c76a98c9e499f2))


### Bug Fixes

* cancel open deliveries to a discontinued pane when a stop finishes ([3339ea7](https://github.com/emsnoel/tandem/commit/3339ea7a7d82d54dbb8e83836c3b3b3fcfa85662))
* keep planner deliveries open and hold recovery on a discontinued run ([de249c4](https://github.com/emsnoel/tandem/commit/de249c498055dbdfd7b85d6107d7554fa1f2e887))
* let a stop finish on a run with a discontinued agent kind ([2bde2c5](https://github.com/emsnoel/tandem/commit/2bde2c50dc44f86601a88b06052e34972cc28d17))
* pause a run whose agent kind is no longer supported ([b8b2d52](https://github.com/emsnoel/tandem/commit/b8b2d52d25680a616ff1300a56d9852c6ddce962))
* refuse a discontinued kind everywhere a pair or handoff can reach it ([0ad7bf7](https://github.com/emsnoel/tandem/commit/0ad7bf728f3bb612c92e421b6fb6eeab8d366334))

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
