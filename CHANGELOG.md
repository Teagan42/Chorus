# Changelog

## 1.0.0 (2026-10-06)


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
