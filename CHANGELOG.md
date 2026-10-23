# Changelog

## [0.19.0](https://github.com/Teagan42/Chorus/compare/v0.18.0...v0.19.0) (2026-10-23)


### Features

* a failed speaker embed is a failure, and readiness asks sidecars what they serve (ADR-0097) ([#134](https://github.com/Teagan42/Chorus/issues/134)) ([15ce9c6](https://github.com/Teagan42/Chorus/commit/15ce9c6000748d9aaa3a54bbba80b60ab1e58af9))
* a satellite's own speaker accept level and click ceiling (ADR-0098) ([#128](https://github.com/Teagan42/Chorus/issues/128)) ([f9db4cc](https://github.com/Teagan42/Chorus/commit/f9db4ccd8b6900e5d34fcbae506de10780e2396f))
* ask a turn in its pause, keep it only if nothing changed (ADR-0095) ([#130](https://github.com/Teagan42/Chorus/issues/130)) ([82fbe77](https://github.com/Teagan42/Chorus/commit/82fbe7717ec5536511caa8d7b2eb907141058f0c))
* **esphome:** send no mic audio while muted (ADR-0100) ([#136](https://github.com/Teagan42/Chorus/issues/136)) ([f7513c4](https://github.com/Teagan42/Chorus/commit/f7513c4c640cd96fe670f19cee14fd38bcdfff04))
* OpenTelemetry spans derived from the journal, over OTLP/HTTP (ADR-0104) ([#140](https://github.com/Teagan42/Chorus/issues/140)) ([3a59eb8](https://github.com/Teagan42/Chorus/commit/3a59eb87d6cddcacab662679e5d27d9dcbc16b57))
* read Smart Turn in bands, and journal what ended each turn (ADR-0094) ([#131](https://github.com/Teagan42/Chorus/issues/131)) ([e6748aa](https://github.com/Teagan42/Chorus/commit/e6748aa971e772c0192a6284f50818df68936f12))
* refuse the device's own voice at a barge-in echo stage (ADR-0096) ([#132](https://github.com/Teagan42/Chorus/issues/132)) ([09f6419](https://github.com/Teagan42/Chorus/commit/09f641954b1908f49ecf4dcceecf44df76289dd9))
* render the voice on Apple Silicon's GPU via an MLX sidecar ([#126](https://github.com/Teagan42/Chorus/issues/126)) ([b5ffd1c](https://github.com/Teagan42/Chorus/commit/b5ffd1cd1fa5778b828684fd39f755c2b083a5db))
* **reviewui:** judge a Voices clip at its satellite's accept level (ADR-0099) ([#135](https://github.com/Teagan42/Chorus/issues/135)) ([c8f9d29](https://github.com/Teagan42/Chorus/commit/c8f9d298911f30dec597fe49f5cd0b10d8d0e67c))
* speaker ID that works on a Voice PE's own audio ([#124](https://github.com/Teagan42/Chorus/issues/124)) ([c96b35c](https://github.com/Teagan42/Chorus/commit/c96b35ce4b282c5f33c4952d3f60f0ef4334caa3))


### Bug Fixes

* a Voice PE that answers once, and only to the room ([#127](https://github.com/Teagan42/Chorus/issues/127)) ([79bdc15](https://github.com/Teagan42/Chorus/commit/79bdc157a7743933f1241d02f28a22f2724b403b))
* honest startup warnings, chorus-eval on vLLM, and real-server tests for OpenAI/Wyoming ([#129](https://github.com/Teagan42/Chorus/issues/129)) ([14ce443](https://github.com/Teagan42/Chorus/commit/14ce44341e4cc874d077d687fc37dc6b01dd8684))
* **reviewui:** name the Voices clip players for screen readers ([#133](https://github.com/Teagan42/Chorus/issues/133)) ([adb79cf](https://github.com/Teagan42/Chorus/commit/adb79cf9d9135affed57c707fcb9a94e53d68547))

## [0.18.0](https://github.com/Teagan42/Chorus/compare/v0.17.0...v0.18.0) (2026-10-13)


### Features

* pluggable TTS and STT providers — OpenAI-compatible and Wyoming (ADR-0081) ([#120](https://github.com/Teagan42/Chorus/issues/120)) ([9805b26](https://github.com/Teagan42/Chorus/commit/9805b26c7f4e37ca49a6a7b8904f88cbe55441b6))


### Documentation

* bring SPEC, README and guides up to the implementation ([#123](https://github.com/Teagan42/Chorus/issues/123)) ([8af35fb](https://github.com/Teagan42/Chorus/commit/8af35fb5d9c0908f78f501d23eedac470a68d4fb))
* **readme:** chorusd runs a Satellite1 on protocol 4 firmware ([#122](https://github.com/Teagan42/Chorus/issues/122)) ([41b1591](https://github.com/Teagan42/Chorus/commit/41b1591c733ff1ba34c3ddfa326468e9227344a5))

## [0.17.0](https://github.com/Teagan42/Chorus/compare/v0.16.0...v0.17.0) (2026-10-13)


### Features

* a vLLM turn engine, chosen with CHORUS_LLM_PROVIDER (ADR-0077) ([#113](https://github.com/Teagan42/Chorus/issues/113)) ([f65676b](https://github.com/Teagan42/Chorus/commit/f65676be28c94b5d18412c76a7d999d0cc2c35cb))
* arbitrate a wake several satellites heard, and record the wake word (ADR-0072) ([#105](https://github.com/Teagan42/Chorus/issues/105)) ([0679dd6](https://github.com/Teagan42/Chorus/commit/0679dd66a061c040fe2e67fecc5de19245afc890))
* **bridge:** authenticate the audio link device, protocol 3 (ADR-0066) ([#94](https://github.com/Teagan42/Chorus/issues/94)) ([5376049](https://github.com/Teagan42/Chorus/commit/5376049011162ae87a67e295b3ae6ce9affe2dd9))
* **chorusd:** health, readiness and metrics on a loopback ops listener ([#99](https://github.com/Teagan42/Chorus/issues/99)) ([d24087f](https://github.com/Teagan42/Chorus/commit/d24087f47991a6b12ca63885b2f8d96678c08b94))
* Dockerize Chorus services and reject unknown wakes ([#118](https://github.com/Teagan42/Chorus/issues/118)) ([b8a0966](https://github.com/Teagan42/Chorus/commit/b8a096698d6922fe546c5548572534db0165c5e9))
* **esphome:** a Voice PE config wired from its stock firmware (ADR-0073) ([#103](https://github.com/Teagan42/Chorus/issues/103)) ([70f0730](https://github.com/Teagan42/Chorus/commit/70f0730d90f130238bcb7a0c250766b2085e3d15))
* eval replay suites, gated on what regressed (ADR-0079) ([#114](https://github.com/Teagan42/Chorus/issues/114)) ([fc3e1fb](https://github.com/Teagan42/Chorus/commit/fc3e1fb3e2a6e3cad9126813859b8716e01ac624))
* **firmware:** wake on the household's own "Hey Eddie" ([#92](https://github.com/Teagan42/Chorus/issues/92)) ([026352f](https://github.com/Teagan42/Chorus/commit/026352f4e3d7422aba6a02b858dfaebe8f42ee31))
* protocol 4, the host proves itself and the audio link is sealed, forward-secret (ADR-0074) ([#111](https://github.com/Teagan42/Chorus/issues/111)) ([fd98aa2](https://github.com/Teagan42/Chorus/commit/fd98aa2067eea0a7320073e2b47ed6826ecec8c1))
* **retention:** prune audio and logs by per-satellite horizons ([#96](https://github.com/Teagan42/Chorus/issues/96)) ([8b823b4](https://github.com/Teagan42/Chorus/commit/8b823b4dea8df31c4e91bf06d762e86ec9bc69e1))
* **reviewui:** an arbitration report, and dropping a clip from a satellite's own log (ADR-0076) ([#109](https://github.com/Teagan42/Chorus/issues/109)) ([07b2fab](https://github.com/Teagan42/Chorus/commit/07b2fabe3134fb05353f2cd41e4a3afe14620e0b))
* **reviewui:** delete conversations, drop clips and forget memories ([#101](https://github.com/Teagan42/Chorus/issues/101)) ([6bcd1d4](https://github.com/Teagan42/Chorus/commit/6bcd1d4c33868bcf4f4ff56373a1403b8e094c18))
* **reviewui:** export format 2 with each turn's prompt, tools, split and audio ([#102](https://github.com/Teagan42/Chorus/issues/102)) ([1710b55](https://github.com/Teagan42/Chorus/commit/1710b55e1f234206967e899fe4b5c403159c4bce))
* **reviewui:** speaker enrollment on a Voices screen (ADR-0080) ([#119](https://github.com/Teagan42/Chorus/issues/119)) ([699a027](https://github.com/Teagan42/Chorus/commit/699a027365bc381fe286916f63eeb73c43c9289e))
* say failures the household would meet as silence, and show mute and errors on the ring (ADR-0069) ([#100](https://github.com/Teagan42/Chorus/issues/100)) ([b01a24b](https://github.com/Teagan42/Chorus/commit/b01a24be9fe25945bb86136dd65fa0cdaebf1703))
* session_opened records the wake word that opened it ([#108](https://github.com/Teagan42/Chorus/issues/108)) ([539aec3](https://github.com/Teagan42/Chorus/commit/539aec39c1b42e80ba988bbf787f1da308ab91d7))
* set_volume reports the level the speaker confirms; follow-ups to [#110](https://github.com/Teagan42/Chorus/issues/110) and [#111](https://github.com/Teagan42/Chorus/issues/111) ([#112](https://github.com/Teagan42/Chorus/issues/112)) ([d9a2981](https://github.com/Teagan42/Chorus/commit/d9a29811353b740eb4b5fabc170124ef401f4331))
* speaker volume over the native API, and the HAT's mute switch (ADR-0067) ([#98](https://github.com/Teagan42/Chorus/issues/98)) ([cd14350](https://github.com/Teagan42/Chorus/commit/cd1435045c5f5853a8abe9e125c978f50f5be6bf))
* the MCP provider, under conservative default policies (ADR-0075) ([#110](https://github.com/Teagan42/Chorus/issues/110)) ([bd6c361](https://github.com/Teagan42/Chorus/commit/bd6c361ccb66177e93d7fa5f7eaad7777150e635))
* wake-word retraining pipeline (ADR-0078); the harvester learns the ear's unheard line ([#115](https://github.com/Teagan42/Chorus/issues/115)) ([a4016e5](https://github.com/Teagan42/Chorus/commit/a4016e5b3ed8b505a13b4c63850fe936cafecca3))


### Bug Fixes

* **harvest:** keep a wake nothing could decode out of the wake corpus ([#107](https://github.com/Teagan42/Chorus/issues/107)) ([a9e0e2b](https://github.com/Teagan42/Chorus/commit/a9e0e2be2b1ba25769806eb4c5644b96e8b07976))
* root causes of three load-only test failures, refuse announcements into closing sessions, requirements*.txt as deps ([#116](https://github.com/Teagan42/Chorus/issues/116)) ([8f49ba9](https://github.com/Teagan42/Chorus/commit/8f49ba900e5068aa9696c289e420db6052f5d57d))


### Documentation

* bring the docs and the phase plan up to date with [#88](https://github.com/Teagan42/Chorus/issues/88)–[#102](https://github.com/Teagan42/Chorus/issues/102) ([#104](https://github.com/Teagan42/Chorus/issues/104)) ([c6572fa](https://github.com/Teagan42/Chorus/commit/c6572faf8e437b9d149b90cda97934a330bc3fc8))
* record the Voice PE config and wake arbitration as done ([#106](https://github.com/Teagan42/Chorus/issues/106)) ([c4200b4](https://github.com/Teagan42/Chorus/commit/c4200b459709d41c2a1a5f18cd08d44e5d799acc))

## [0.16.0](https://github.com/Teagan42/Chorus/compare/v0.15.0...v0.16.0) (2026-10-11)


### Features

* **enroll:** add, list and remove voiceprints from the command line ([#88](https://github.com/Teagan42/Chorus/issues/88)) ([51e72b1](https://github.com/Teagan42/Chorus/commit/51e72b1506f952e7b9fa207f5b0743cd5d7d054c))
* **firmware:** wake the bridge from micro_wake_word, password OTA, bound frames, validate in CI ([#89](https://github.com/Teagan42/Chorus/issues/89)) ([da76912](https://github.com/Teagan42/Chorus/commit/da76912717ebb43e25055783ab2057b378a23e55))
* **journal:** bind a held call's yes to the person who asked, and hold the generic services by their target ([#91](https://github.com/Teagan42/Chorus/issues/91)) ([233ec27](https://github.com/Teagan42/Chorus/commit/233ec27008ca943267e0dfca035d354178cfdea9))


### Bug Fixes

* **triage,reviewui:** review follow-ups from [#82](https://github.com/Teagan42/Chorus/issues/82) ([#84](https://github.com/Teagan42/Chorus/issues/84)) ([6ce1269](https://github.com/Teagan42/Chorus/commit/6ce1269f522669901ebfe67d0e63e20d4282828e))

## [0.15.0](https://github.com/Teagan42/Chorus/compare/v0.14.0...v0.15.0) (2026-10-10)


### Features

* **hardware:** a main board under the Satellite1 HAT replaces rev A ([#74](https://github.com/Teagan42/Chorus/issues/74)) ([1ce720f](https://github.com/Teagan42/Chorus/commit/1ce720f4859a037437f4d20580cc7389739978a1))
* interject pauses and resumes speech, and sessions drive the LED ring ([#78](https://github.com/Teagan42/Chorus/issues/78)) ([0d33150](https://github.com/Teagan42/Chorus/commit/0d33150ffc71c571353897a5099c55fc94122a97))
* main-board ESPHome config, and four-mic DoA on the satellite (ADR-0061) ([#83](https://github.com/Teagan42/Chorus/issues/83)) ([4275bd5](https://github.com/Teagan42/Chorus/commit/4275bd54ecea1f8c7ab4c802f480a2f40a225b7e))
* **reviewui:** edit the tool schema on Replay, and keep every re-run ([#81](https://github.com/Teagan42/Chorus/issues/81)) ([340413d](https://github.com/Teagan42/Chorus/commit/340413da3408ba141ab11356b9c9c4126f6268f1))
* **reviewui:** rejected wakes, weak positives and repeats as review signals ([#82](https://github.com/Teagan42/Chorus/issues/82)) ([057d514](https://github.com/Teagan42/Chorus/commit/057d5149aecf4370cb0ec055a3468fa0510d9392))


### Bug Fixes

* **chorusd:** timeouts, closes, wakes, barge-in audio and tool policy ([#79](https://github.com/Teagan42/Chorus/issues/79)) ([6d1b8df](https://github.com/Teagan42/Chorus/commit/6d1b8dfce9c8a95b1ba4df646e82e974b1614f12))
* **hardware:** J7 receptacle checked against Hirose, power contacts measured ([#76](https://github.com/Teagan42/Chorus/issues/76)) ([aad15d0](https://github.com/Teagan42/Chorus/commit/aad15d0135b46b6b506a07b7c98dfc1dea2e4e0a))
* **reviewui:** harden the review UI against bad input, bad logs and other sites ([#77](https://github.com/Teagan42/Chorus/issues/77)) ([71a9432](https://github.com/Teagan42/Chorus/commit/71a9432c9f03e0bcd909f222744ad0ed40443a2e))


### Documentation

* make eleven stale claims say what the code does ([#80](https://github.com/Teagan42/Chorus/issues/80)) ([721589e](https://github.com/Teagan42/Chorus/commit/721589e212144f576010e8964a02d939a072e3eb))

## [0.14.0](https://github.com/Teagan42/Chorus/compare/v0.13.0...v0.14.0) (2026-10-10)


### Features

* **hardware:** build the satellite's KiCad project in CI, ready to open ([#72](https://github.com/Teagan42/Chorus/issues/72)) ([a4a9ed8](https://github.com/Teagan42/Chorus/commit/a4a9ed8c112a4ee5a577e2c552bd434d76709e9e))
* **hardware:** chorus-sat schematic, KiCad netlist and JLCPCB BOM ([#63](https://github.com/Teagan42/Chorus/issues/63)) ([4280e7b](https://github.com/Teagan42/Chorus/commit/4280e7bb18a82db658610874ffdcc0d61e824f58))
* **hardware:** license the SnapEDA footprints, download the TAS2780's ([#69](https://github.com/Teagan42/Chorus/issues/69)) ([3ec290d](https://github.com/Teagan42/Chorus/commit/3ec290d43cd33ad5e262397bebf82e8ffbb04e29))
* **reviewui:** SPEC §9.2 labels, promote verdict, and no more blank rows ([#67](https://github.com/Teagan42/Chorus/issues/67)) ([0f66145](https://github.com/Teagan42/Chorus/commit/0f66145991498c896b1f4d3b8593dc6a317a813e))
* the satellite's room, both mic channels, and mmWave presence ([#66](https://github.com/Teagan42/Chorus/issues/66)) ([d8f9add](https://github.com/Teagan42/Chorus/commit/d8f9add4df7ca934a2ab6ca02a58d3a2e6d932bf))
* tool calls in DPO pair text, and promote re-runs that only call ([#71](https://github.com/Teagan42/Chorus/issues/71)) ([5bcf5ea](https://github.com/Teagan42/Chorus/commit/5bcf5eaeed017021f1c58a8c040ec34a9341cabb))


### Bug Fixes

* a guest is a guest, and the TV the gate refused is not a turn ([#65](https://github.com/Teagan42/Chorus/issues/65)) ([9986d83](https://github.com/Teagan42/Chorus/commit/9986d83cd7ac7a68e02ec7b6f2d3263f5d6e0ea5))
* chorusd e2e flakes, and a shutdown that waits for a reply nobody reads ([#73](https://github.com/Teagan42/Chorus/issues/73)) ([c801a44](https://github.com/Teagan42/Chorus/commit/c801a44100075ef3ddede114cffc525096453651))
* **listen:** time Smart Turn's verdict by the clock, not the audio ([#64](https://github.com/Teagan42/Chorus/issues/64)) ([82f1223](https://github.com/Teagan42/Chorus/commit/82f1223a69ba6e0295596a09476d5a39ab824e91))
* **session:** say a failed model or voice aloud, and bound the model ([#68](https://github.com/Teagan42/Chorus/issues/68)) ([2849e36](https://github.com/Teagan42/Chorus/commit/2849e360942a325ab4bb452dd53413072deec5cc))
* two test-suite flakes the race detector and parallel load found ([#62](https://github.com/Teagan42/Chorus/issues/62)) ([6b0f900](https://github.com/Teagan42/Chorus/commit/6b0f90036a2854cc2a8411f2ebeb1bf0f6fa3e82))


### Documentation

* direction of arrival is estimated on the host (ADR-0053) ([#70](https://github.com/Teagan42/Chorus/issues/70)) ([6133466](https://github.com/Teagan42/Chorus/commit/61334669f06f151ecca27e40fd71fb53731ad863))


### Refactoring

* rename the Go module to github.com/teagan42/chorus ([#60](https://github.com/Teagan42/Chorus/issues/60)) ([aca496c](https://github.com/Teagan42/Chorus/commit/aca496c56c7fe2809ad8c4b7dd406afc6faa72af))

## [0.13.0](https://github.com/Teagan42/Chorus/compare/v0.12.0...v0.13.0) (2026-10-10)


### Features

* **householdvoice:** clone named people's voices with Chatterbox ([#54](https://github.com/Teagan42/Chorus/issues/54)) ([19ef9f3](https://github.com/Teagan42/Chorus/commit/19ef9f383d4c87384bd3e2bafe6295d5dac7e48b))
* recall what's relevant to the question, not just the newest ([#52](https://github.com/Teagan42/Chorus/issues/52)) ([700f009](https://github.com/Teagan42/Chorus/commit/700f009c08f751b8b1cc575d20c645106f6eaa8d))
* timers that go off where they were set, and announcements answerable with no wake word ([#53](https://github.com/Teagan42/Chorus/issues/53)) ([08df210](https://github.com/Teagan42/Chorus/commit/08df21053cc573618cc1e3a4d8bcf7f7398eece2))


### Bug Fixes

* **hardware:** XMOS reset, SPI muxes and MCLK from the Satellite1 sources, and eight mics for DoA ([#58](https://github.com/Teagan42/Chorus/issues/58)) ([b2a02ff](https://github.com/Teagan42/Chorus/commit/b2a02ff71ab3172e8b91b0f8e17a818d675c1067))
* speak answers the model writes as content, and what else the household models runs found ([#56](https://github.com/Teagan42/Chorus/issues/56)) ([5308b36](https://github.com/Teagan42/Chorus/commit/5308b3671f709a032b195c42eb9f4e83e8cc4312))


### Documentation

* **hardware:** a satellite board designed around what Chorus needs ([#57](https://github.com/Teagan42/Chorus/issues/57)) ([8f43c7e](https://github.com/Teagan42/Chorus/commit/8f43c7e0deeca32429e0b614192c9e57b88e9960))
* **spec:** mic channel 1 is processed audio, not a raw mic ([#59](https://github.com/Teagan42/Chorus/issues/59)) ([fc51c03](https://github.com/Teagan42/Chorus/commit/fc51c03343c07e42ed0148185b35e3049ed30e12))

## [0.12.0](https://github.com/Teagan42/Chorus/compare/v0.11.0...v0.12.0) (2026-10-10)


### Features

* hold a turn whose words end on one no command ends on ([#50](https://github.com/Teagan42/Chorus/issues/50)) ([4e4cc58](https://github.com/Teagan42/Chorus/commit/4e4cc582d8e1aa998a639274b2548744737f989a))
* hold the garage door for a yes, and let the blinds through ([#48](https://github.com/Teagan42/Chorus/issues/48)) ([01bcf16](https://github.com/Teagan42/Chorus/commit/01bcf1601a8c81e7332cc1d1c2b18ddcec47fe47))
* tell each turn the time and what the person asked lately ([#49](https://github.com/Teagan42/Chorus/issues/49)) ([42fc06a](https://github.com/Teagan42/Chorus/commit/42fc06a76d9f4ada5d324bef69e73542d02f3542))

## [0.11.0](https://github.com/Teagan42/Chorus/compare/v0.10.0...v0.11.0) (2026-10-09)


### Features

* remember what each person asks to be remembered ([#46](https://github.com/Teagan42/Chorus/issues/46)) ([7a3bb42](https://github.com/Teagan42/Chorus/commit/7a3bb421c0ab8499488ba88b1b5d7445e38a27b3))


### Bug Fixes

* **docsite:** name the shipped release in the header, not a cached one ([#44](https://github.com/Teagan42/Chorus/issues/44)) ([3b30b76](https://github.com/Teagan42/Chorus/commit/3b30b76093500a567adacdd15ec258ccd110df4a))

## [0.10.0](https://github.com/Teagan42/Chorus/compare/v0.9.0...v0.10.0) (2026-10-09)


### Features

* a slow tool carries what to say while it works ([#43](https://github.com/Teagan42/Chorus/issues/43)) ([7a80d63](https://github.com/Teagan42/Chorus/commit/7a80d63584905cd62979e580ffe027073425b882))
* ask the model again with its tool results and the conversation ([#40](https://github.com/Teagan42/Chorus/issues/40)) ([e623b22](https://github.com/Teagan42/Chorus/commit/e623b2269a92530a464f3c393743750ef08b0862))
* end a turn when Smart Turn says it is over ([#37](https://github.com/Teagan42/Chorus/issues/37)) ([64945fd](https://github.com/Teagan42/Chorus/commit/64945fd2bfba096816d85d623dab7148a282f8f5))
* hold unlocking and disarming until the person says yes ([#42](https://github.com/Teagan42/Chorus/issues/42)) ([144802e](https://github.com/Teagan42/Chorus/commit/144802ea7fead6fd3f9a4dd638cc4fe63ec43288))
* **reviewui:** a hosted demo of the review UI, running in the browser on Pages ([#41](https://github.com/Teagan42/Chorus/issues/41)) ([8d852ea](https://github.com/Teagan42/Chorus/commit/8d852eafbe4f6db39032b4cbce940e52ad77378c))

## [0.9.0](https://github.com/Teagan42/Chorus/compare/v0.8.0...v0.9.0) (2026-10-09)


### Features

* journal the first frame each turn played, and flag slow answers ([#33](https://github.com/Teagan42/Chorus/issues/33)) ([52b3023](https://github.com/Teagan42/Chorus/commit/52b30233df4cbf5edc18a1d8da1703ffbd374a8d))


### Documentation

* a review UI guide, and README/CONTRIBUTING caught up to main ([#35](https://github.com/Teagan42/Chorus/issues/35)) ([b836e39](https://github.com/Teagan42/Chorus/commit/b836e39b8db054b78781384e9ab0b28e8d75bab7))

## [0.8.0](https://github.com/Teagan42/Chorus/compare/v0.7.0...v0.8.0) (2026-10-09)


### Features

* **reviewui:** Replay re-runs a conversation under an edited prompt ([#30](https://github.com/Teagan42/Chorus/issues/30)) ([835502f](https://github.com/Teagan42/Chorus/commit/835502f9500fb6027ceda4462694e9613f86b7de))
* **triage:** flag a request asked again soon after ([#32](https://github.com/Teagan42/Chorus/issues/32)) ([90720ca](https://github.com/Teagan42/Chorus/commit/90720caac75bccedb7c5edc35e89ff2e9c4b747b))

## [0.7.0](https://github.com/Teagan42/Chorus/compare/v0.6.0...v0.7.0) (2026-10-09)


### Features

* **reviewui:** Browse the household's day and each conversation's log ([#27](https://github.com/Teagan42/Chorus/issues/27)) ([5bee7a4](https://github.com/Teagan42/Chorus/commit/5bee7a41223c56e03033cbd10fae0ff6727a7ee0))

## [0.6.0](https://github.com/Teagan42/Chorus/compare/v0.5.0...v0.6.0) (2026-10-08)


### Features

* **reviewui:** the Curate screen over the journal ([#22](https://github.com/Teagan42/Chorus/issues/22)) ([856d5b7](https://github.com/Teagan42/Chorus/commit/856d5b71b08e365165803f8669e1e3aa41aecf17))
* **reviewui:** the Export screen and DPO JSONL download ([#25](https://github.com/Teagan42/Chorus/issues/25)) ([7bfb0cf](https://github.com/Teagan42/Chorus/commit/7bfb0cfc28ccad00c79c92f0a64f066401910cea))
* **reviewui:** the Review screen with inline playback ([#24](https://github.com/Teagan42/Chorus/issues/24)) ([8251d9e](https://github.com/Teagan42/Chorus/commit/8251d9e6f95619e350ab453ce692013b8ae25335))
* **reviewui:** the Triage screen over journal signals ([#26](https://github.com/Teagan42/Chorus/issues/26)) ([b65fe66](https://github.com/Teagan42/Chorus/commit/b65fe66f7da5239deb399749f4c673a9c694fd90))

## [0.5.0](https://github.com/Teagan42/Chorus/compare/v0.4.0...v0.5.0) (2026-10-08)


### Features

* run the phase-1 cascade end to end ([#20](https://github.com/Teagan42/Chorus/issues/20)) ([e01ae44](https://github.com/Teagan42/Chorus/commit/e01ae4423b08598a70b363d825762418e5afc40f))

## [0.4.0](https://github.com/Teagan42/Chorus/compare/v0.3.0...v0.4.0) (2026-10-08)


### Features

* **provider:** render speech with Kokoro at the device's rate ([#19](https://github.com/Teagan42/Chorus/issues/19)) ([15cdb53](https://github.com/Teagan42/Chorus/commit/15cdb53fbcac8a998d2f98931765e7577b9755f4))


### Bug Fixes

* **session:** one live session per conversation, one sequence per log ([#17](https://github.com/Teagan42/Chorus/issues/17)) ([f465001](https://github.com/Teagan42/Chorus/commit/f46500101276067daa7ca27ed473e4974af19ac0))

## [0.3.0](https://github.com/Teagan42/Chorus/compare/v0.2.0...v0.3.0) (2026-10-07)


### Features

* **provider:** dial Ollama for a turn ([#14](https://github.com/Teagan42/Chorus/issues/14)) ([5caa4d4](https://github.com/Teagan42/Chorus/commit/5caa4d4aca53e9c53e6b4c1aac54327f5c0a3c30))

## [0.2.0](https://github.com/Teagan42/Chorus/compare/v0.1.0...v0.2.0) (2026-10-07)


### Features

* **blob:** store the audio a journal event refers to ([#11](https://github.com/Teagan42/Chorus/issues/11)) ([520bfac](https://github.com/Teagan42/Chorus/commit/520bfacda5375d47b177f744ab22ad8fa86cbd9a))
* **bridge:** prove full duplex on real hardware ([#7](https://github.com/Teagan42/Chorus/issues/7)) ([c51eef6](https://github.com/Teagan42/Chorus/commit/c51eef6def699a2c1fcd373255ddcc8d152ab3ef))
* **provider:** decode Ollama chat streams into session actions ([#13](https://github.com/Teagan42/Chorus/issues/13)) ([08cdd4f](https://github.com/Teagan42/Chorus/commit/08cdd4fee7f688012c428cba5b13ee181f7d7cd0))
* **satellite:** resolve the truncation point to a clause ([#12](https://github.com/Teagan42/Chorus/issues/12)) ([197be0f](https://github.com/Teagan42/Chorus/commit/197be0fb00615bdbe36a69228026d3bdce8ab963))
* **satellite:** take the truncation point from the device's DAC ([#10](https://github.com/Teagan42/Chorus/issues/10)) ([c16dd50](https://github.com/Teagan42/Chorus/commit/c16dd5059a6f280203067c967fe4b817ba70e872))


### Bug Fixes

* **chorus_bridge:** stop the uplink starving itself on a busy band ([#9](https://github.com/Teagan42/Chorus/issues/9)) ([d56e8f6](https://github.com/Teagan42/Chorus/commit/d56e8f69f3dd36f059fa8d3ca18eaf1ba8827969))

## 0.1.0 (2026-10-06)


### Features

* **bridge:** define the chorus_bridge audio wire protocol ([4af7a47](https://github.com/Teagan42/Chorus/commit/4af7a476e3e0e7ae75d2c09ad54d2eb6ddba3fb4))
* **config:** load and validate the satellite inventory ([0899e76](https://github.com/Teagan42/Chorus/commit/0899e76d9e5e2299dbc9f724a05a8ba33aa8ea9b))
* **esphome:** add Noise transport and native API client ([97dd744](https://github.com/Teagan42/Chorus/commit/97dd7442ad4700032bceef792153e92ed54f7867))
* **esphome:** add the chorus_bridge external component ([9bfda4c](https://github.com/Teagan42/Chorus/commit/9bfda4c271785e9f22b23c4cff5bdfec6dcd6041))
* **esphome:** add the satellite YAML package and example ([777075b](https://github.com/Teagan42/Chorus/commit/777075b6e9f970dc789072bae1c34ebd3c21b3b4))
* **journal:** pure reducer and deterministic replay ([0d49287](https://github.com/Teagan42/Chorus/commit/0d492871d7c8888ce3932dc77184c9d87f70fa80))
* **journal:** reduce discarded speech into the unheard half only ([3901b99](https://github.com/Teagan42/Chorus/commit/3901b99f6f0fb3bcc36cfdb360b869a00d78c9c6))
* **journal:** store the log in Postgres, partitioned by conversation ([02b7bb2](https://github.com/Teagan42/Chorus/commit/02b7bb25d119b2b5299771e59cf7ef3085a01032))
* **journal:** validated append-only log with a pluggable store ([9a67c0e](https://github.com/Teagan42/Chorus/commit/9a67c0e1137e7d49552f7f67caa837f645ae7dd3))
* **msgid:** derive ESPHome wire ids from descriptors ([06600a0](https://github.com/Teagan42/Chorus/commit/06600a0781c1587c2cf0eb9b9961eed5941dfefe))
* **probe:** add a hardware probe command ([75fbdd1](https://github.com/Teagan42/Chorus/commit/75fbdd16e932e9aed04a94cdbf0ea17144454b48))
* **schema:** declare the tool registry and event taxonomy in CUE ([dd2acd9](https://github.com/Teagan42/Chorus/commit/dd2acd960b3cfcb0fe306bee14468dd11810ca65))
* **schema:** distinguish discarded speech from truncated speech ([812af25](https://github.com/Teagan42/Chorus/commit/812af25dd1441f65442f287a83440322d5fbd897))
* **schema:** record model completions and barge-in timing as events ([7560300](https://github.com/Teagan42/Chorus/commit/7560300c465a1c2a34d18c62b0133d5663e35722))
* **session:** keep interrupted-turn truth and per-tool policy ([3942ef2](https://github.com/Teagan42/Chorus/commit/3942ef2164b60b7531bf3453e0ce109d339ee8fd))
* **session:** key conversations on the person, not the device ([2858d83](https://github.com/Teagan42/Chorus/commit/2858d8317778c6b72e4d9cd61136edf1718ad3fe))
* **session:** open on wake, close when the model says so ([f9ad8c5](https://github.com/Teagan42/Chorus/commit/f9ad8c51a204e5a81217931d898afffd15427b48))
* **tools:** add spec clause to test traceability ([c524f0c](https://github.com/Teagan42/Chorus/commit/c524f0c1b778b1e274588fd7b5d8015c7dac6e23))
* **tools:** enforce commit atomicity mechanically ([7ba017e](https://github.com/Teagan42/Chorus/commit/7ba017e413d5fef92ee9362a1b25292cdce093bb))
* **tools:** generate Go, JSON Schema, and docs from CUE ([e7d0cea](https://github.com/Teagan42/Chorus/commit/e7d0cea9376214cf95a8c87bd8a888f78d2efba0))
* **tools:** run the code examples in markdown ([08b63da](https://github.com/Teagan42/Chorus/commit/08b63dadc8738b0677de7197869c0323c1958908))


### Bug Fixes

* **journal:** stop a reader mutating the append-only log ([c4ef9df](https://github.com/Teagan42/Chorus/commit/c4ef9dfb8d1e9d6c7b736efa260ce74a90a7ef0b))
* **journal:** truncate the stored wall clock to microseconds ([72b56a3](https://github.com/Teagan42/Chorus/commit/72b56a37181e681c4b167876be21fd1a31b24496))
* **session:** decide a tool result's outcome when the tool returns ([#4](https://github.com/Teagan42/Chorus/issues/4)) ([fbd0613](https://github.com/Teagan42/Chorus/commit/fbd0613818838d1ccd39b20c2abfd6918833c99b))
* **session:** register the speaking child before its first write ([6eeed75](https://github.com/Teagan42/Chorus/commit/6eeed75eee8468fd631b793bf0ee05972d66ea42))


### Documentation

* add a README ([#1](https://github.com/Teagan42/Chorus/issues/1)) ([99406a5](https://github.com/Teagan42/Chorus/commit/99406a5c5852da18b89bb1dc84728867486be9c1))
* add the normative spec ([3575fcc](https://github.com/Teagan42/Chorus/commit/3575fcce9978f8fdb864b67e1ea04ab826f99e19))
* **adr:** add the ADR template ([e947acb](https://github.com/Teagan42/Chorus/commit/e947acb36517f1472dfa3e7937a595b341d6975a))
* **adr:** backfill the device layer decisions ([89bbd30](https://github.com/Teagan42/Chorus/commit/89bbd30bed096b3c5832858512d0af7306dcfa2c))
* **adr:** backfill the journal and annotation decisions ([44b292f](https://github.com/Teagan42/Chorus/commit/44b292fc5de5291514494b7d7c575f8865626d84))
* **adr:** backfill the registry, model, and identity decisions ([85cfd82](https://github.com/Teagan42/Chorus/commit/85cfd8266b7633817acd2ea9b7335a23e969b00a))
* **adr:** backfill the scope and project-shape decisions ([a99a84b](https://github.com/Teagan42/Chorus/commit/a99a84b3e7a1deacdc5b2e59c28d81a0a3d372c0))
* **adr:** backfill the session and interruption decisions ([53c8501](https://github.com/Teagan42/Chorus/commit/53c8501b6a22e147ee4edde1876795dcbdccc3b6))
* **adr:** correct two device API facts from 0010 ([13f0eee](https://github.com/Teagan42/Chorus/commit/13f0eee54be783a72dcc3e2a87a01520bcb543ef))
* **adr:** index the records and link it from CONTRIBUTING ([54ee8e6](https://github.com/Teagan42/Chorus/commit/54ee8e6cab0619d6be54f677ea6f45cd1e56b1a9))
* **adr:** record the journal's partitioning and sequence decisions ([808aa39](https://github.com/Teagan42/Chorus/commit/808aa396ff773257c88e92887e8b950b98e72b9e))
* define how to contribute ([dd440dc](https://github.com/Teagan42/Chorus/commit/dd440dc4d6800687fb5c8346012a185ecb414441))
* **esphome:** document the chorus_bridge wire protocol ([4e780b4](https://github.com/Teagan42/Chorus/commit/4e780b4ba178fd2ddacf261a636e75d92c42540f))
* **spec:** correct two device API facts in §3.1 and §3.2.1 ([d353438](https://github.com/Teagan42/Chorus/commit/d353438eb8e723c8b53563b33ee3e87637872bbf))
* **spec:** record the asymmetric addressing constraint in §13 ([6e0071a](https://github.com/Teagan42/Chorus/commit/6e0071a6cfef8daf3da869ba841784ed50ab34ec))
* **spec:** rewrap §3.1 and §3.2.1 after the fact correction ([94dcee8](https://github.com/Teagan42/Chorus/commit/94dcee85dbf68900f862ee18c0ae0bc6820d5795))
