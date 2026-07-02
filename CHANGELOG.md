# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 1.0.0 (2026-07-02)


### Features

* add netbehavior suite for Linux guests ([19ade61](https://github.com/deploymenttheory/guestweave-macos/commit/19ade613f841ff7cadfa3e08f2df06937307720b))
* added functional clipboard for text and files with security policies. ([#57](https://github.com/deploymenttheory/guestweave-macos/issues/57)) ([7fe2856](https://github.com/deploymenttheory/guestweave-macos/commit/7fe28568346ea0125f5ec77042cbba4c49538c0f))
* **api:** OpenAPI schema-conformance and end-to-end acceptance suites ([f6bb4d0](https://github.com/deploymenttheory/guestweave-macos/commit/f6bb4d052bf1527ee9ce1cac9f8dcf8555ebaf14))
* Bring the `weave serve` HTTP REST API to full parity with the `weave` CLI ([3e57c77](https://github.com/deploymenttheory/guestweave-macos/commit/3e57c77ce8ea2764995f13db5458df72c5f09e85))
* enhance guestweave setup and documentation ([a92a4c1](https://github.com/deploymenttheory/guestweave-macos/commit/a92a4c13fe7d61e2a5f453f13790f79eb0bcfc0a))
* enhance logging commands and configuration ([e94b50a](https://github.com/deploymenttheory/guestweave-macos/commit/e94b50a5f10fa3566baf4216c9affdf3ccbd829f))
* hvmm implementation ([#21](https://github.com/deploymenttheory/guestweave-macos/issues/21)) ([1d8f416](https://github.com/deploymenttheory/guestweave-macos/commit/1d8f416fb708e61c2bd44830d39ed6f66a04cb46))
* **hvmm:** add virtio-blk support for disk images ([#35](https://github.com/deploymenttheory/guestweave-macos/issues/35)) ([dfc03b0](https://github.com/deploymenttheory/guestweave-macos/commit/dfc03b0b112b12caafcc19b608b6bfaa5d076999))
* **hvmm:** implement dynamic device tree generation for QEMU-virt machine ([#36](https://github.com/deploymenttheory/guestweave-macos/issues/36)) ([9ec96d2](https://github.com/deploymenttheory/guestweave-macos/commit/9ec96d21fa162679e93e13fb3c7f798657b29c65))
* **hvmm:** NVMe controller — boot from an NVMe disk ([#42](https://github.com/deploymenttheory/guestweave-macos/issues/42)) ([bac1bf1](https://github.com/deploymenttheory/guestweave-macos/commit/bac1bf1d0d6699c23a38f87cf60a3772c816b09c))
* **hvmm:** NVMe MSI-X interrupts via Apple GIC (HvGicSendMsi) ([#43](https://github.com/deploymenttheory/guestweave-macos/issues/43)) ([0186893](https://github.com/deploymenttheory/guestweave-macos/commit/0186893b4de46742cc0ede8f881dff1019d53105))
* **hvmm:** physical-timer edk2 firmware (FB21649319 workaround) ([#30](https://github.com/deploymenttheory/guestweave-macos/issues/30)) ([816c31d](https://github.com/deploymenttheory/guestweave-macos/commit/816c31dd4ebdff7fc7251d73b22b415a4e9c17de))
* **hvmm:** physical-timer edk2 firmware (FB21649319 workaround) ([#49](https://github.com/deploymenttheory/guestweave-macos/issues/49)) ([1d44df7](https://github.com/deploymenttheory/guestweave-macos/commit/1d44df74ad39dc90cc079e3b1faeb724e4c7830d))
* **hvmm:** QEMU ACPI fw_cfg blobs (Windows ACPI bring-up) ([#39](https://github.com/deploymenttheory/guestweave-macos/issues/39)) ([37e6f96](https://github.com/deploymenttheory/guestweave-macos/commit/37e6f9621753c55648a5cc830ae64abad1f3001d))
* **hvmm:** ramfb display + fw_cfg DMA interface ([#44](https://github.com/deploymenttheory/guestweave-macos/issues/44)) ([63ca49b](https://github.com/deploymenttheory/guestweave-macos/commit/63ca49b1eb85c4d9914238510cc4d99598b9e493))
* **hvmm:** serve QEMU ACPI tables via fw_cfg (Windows ACPI bring-up) ([#41](https://github.com/deploymenttheory/guestweave-macos/issues/41)) ([db3fc82](https://github.com/deploymenttheory/guestweave-macos/commit/db3fc820d54a5c156ad1524a49d73c3aad352673))
* **hvmm:** wire UART console input (interactive guest serial) ([#31](https://github.com/deploymenttheory/guestweave-macos/issues/31)) ([6f9331b](https://github.com/deploymenttheory/guestweave-macos/commit/6f9331b9aa2662560c88a97080d39814bc2be750))
* implement HTTP API server and request handling ([bfa6314](https://github.com/deploymenttheory/guestweave-macos/commit/bfa631463d308c58aa8129779f86f033d3aa42ac))
* initial commit ([e0419d7](https://github.com/deploymenttheory/guestweave-macos/commit/e0419d7593011d86a657d153589fc58e6602da61))
* integrate vTPM 2.0 support for Windows 11 guests in QEMU backend ([#20](https://github.com/deploymenttheory/guestweave-macos/issues/20)) ([b72c370](https://github.com/deploymenttheory/guestweave-macos/commit/b72c370eabd775723f04b8bf5b179f837137dca8))
* refactor to use go standard libraries and migration to idiomatic macosplatform frameworks ([80d575d](https://github.com/deploymenttheory/guestweave-macos/commit/80d575d2ddec5a5c963ab913262e3b8c0d8c7be8))
* replace foundation bindings with os and filepath for file operations ([b896193](https://github.com/deploymenttheory/guestweave-macos/commit/b896193379605572042c36a8e3c9bf1f23213da4))
* seperated out ui to it's own package ([fa185e7](https://github.com/deploymenttheory/guestweave-macos/commit/fa185e7460741170486c7ad8052fa37cee3f7800))
* **snapshot:** add in-process VM snapshot revert functionality ([#55](https://github.com/deploymenttheory/guestweave-macos/issues/55)) ([f31372b](https://github.com/deploymenttheory/guestweave-macos/commit/f31372b70cd74a1692ff7d9862522ecdb6b1725c))
* **ui:** implement custom About window with brand styling and dynamic info ([#62](https://github.com/deploymenttheory/guestweave-macos/issues/62)) ([c9a34e9](https://github.com/deploymenttheory/guestweave-macos/commit/c9a34e94fd732023656ede3cdee49cdd69f30a12))
* updated http api to reflect all commands in the cli ([a7dbc65](https://github.com/deploymenttheory/guestweave-macos/commit/a7dbc65ec99d394ee9427f0cea3448dd608cb12f))
* **winimage:** acquire Windows media from software-download ISOs ([#54](https://github.com/deploymenttheory/guestweave-macos/issues/54)) ([b227a52](https://github.com/deploymenttheory/guestweave-macos/commit/b227a529e20dd0c715a57f04e2207b3cdc395779))


### Bug Fixes

* **build:** refine comments on edk2 memory protection for Windows boot ([#48](https://github.com/deploymenttheory/guestweave-macos/issues/48)) ([a0663c0](https://github.com/deploymenttheory/guestweave-macos/commit/a0663c0b8b3f344cab81456b650fc824b0d08e0b))
* clarify comment in build-el2-firmware workflow ([#29](https://github.com/deploymenttheory/guestweave-macos/issues/29)) ([ec51870](https://github.com/deploymenttheory/guestweave-macos/commit/ec51870a8de885a2e76cc7b183cd5491533fd5f4))
* enhance submodule initialization in build-el2-firmware workflow ([#28](https://github.com/deploymenttheory/guestweave-macos/issues/28)) ([d241dad](https://github.com/deploymenttheory/guestweave-macos/commit/d241dadd5a2db30bb2a762ff9059ad86f5eae5f7))
* **hvmm:** expose an emulated PMU to the EL2 guest (Windows bootmgfw) ([#45](https://github.com/deploymenttheory/guestweave-macos/issues/45)) ([06d2349](https://github.com/deploymenttheory/guestweave-macos/commit/06d2349c5642ef0c0d0b6fdd16c43d05f5d56376))
* **hvmm:** relax edk2 memory protection + RO firmware for Windows boot ([#46](https://github.com/deploymenttheory/guestweave-macos/issues/46)) ([015736a](https://github.com/deploymenttheory/guestweave-macos/commit/015736a364c4b47b64632b547797b72fdcf9be4d))
* move main-thread dispatch onto the idiomatic mainthread API ([7bf73bb](https://github.com/deploymenttheory/guestweave-macos/commit/7bf73bbc0607b85203a79feaed0b3dd32a19e4d4))
* move main-thread dispatch onto the idiomatic mainthread API ([e470514](https://github.com/deploymenttheory/guestweave-macos/commit/e470514759ee9f446bc3c15114ddc2b333d2bc4c))
* optimize submodule initialization in build-el2-firmware workflow ([#23](https://github.com/deploymenttheory/guestweave-macos/issues/23)) ([4ae6ee4](https://github.com/deploymenttheory/guestweave-macos/commit/4ae6ee4f24b2d7479560e357aa324536b3698aae))
* pipeline ([#37](https://github.com/deploymenttheory/guestweave-macos/issues/37)) ([307bd77](https://github.com/deploymenttheory/guestweave-macos/commit/307bd7717a3875e34ce6baa1153e33c67a780a39))
* refine submodule initialization in build-el2-firmware workflow ([#26](https://github.com/deploymenttheory/guestweave-macos/issues/26)) ([4fb39cf](https://github.com/deploymenttheory/guestweave-macos/commit/4fb39cf4c0922d2986b2f2938a951d3e7ff0d3dd))
* remove objcutil dependency for MAC address handling ([52547f4](https://github.com/deploymenttheory/guestweave-macos/commit/52547f489eaa717a091d988fc6b6070e96e23830))
* update ACPI extraction logic in QEMU workflow ([#38](https://github.com/deploymenttheory/guestweave-macos/issues/38)) ([cad0575](https://github.com/deploymenttheory/guestweave-macos/commit/cad057527a9f7ec25bddb3319a67809c414fcfba))
* update assembly entry point in QEMU workflow ([#40](https://github.com/deploymenttheory/guestweave-macos/issues/40)) ([80aca96](https://github.com/deploymenttheory/guestweave-macos/commit/80aca96848efed525cd9be6e3afc737576ccad69))
* update build-el2-firmware workflow dependencies ([#24](https://github.com/deploymenttheory/guestweave-macos/issues/24)) ([06d3f11](https://github.com/deploymenttheory/guestweave-macos/commit/06d3f118f970e623cd9c3e36c46dc9ccc47d5742))
* update dependencies and refactor objcutil usage ([b447094](https://github.com/deploymenttheory/guestweave-macos/commit/b44709413920ac9b14a5b957e597cfb92519ad04))
* update go-bindings-macosplatform import paths and version ([8b97311](https://github.com/deploymenttheory/guestweave-macos/commit/8b97311b2af829d53dbbabf829f14db21a255ba7))
* update go-bindings-macosplatform import paths and version ([11e826e](https://github.com/deploymenttheory/guestweave-macos/commit/11e826e522cd8a95f08073541e9aa23856ee2cf6))
* update go-bindings-macosplatform to v0.10.1 and refactor alert methods ([#19](https://github.com/deploymenttheory/guestweave-macos/issues/19)) ([51cbfd1](https://github.com/deploymenttheory/guestweave-macos/commit/51cbfd1a25f74009a37b39404d5d778def4b6c2c))
* various ui defects ([#59](https://github.com/deploymenttheory/guestweave-macos/issues/59)) ([018bde0](https://github.com/deploymenttheory/guestweave-macos/commit/018bde02a2b8df906f4e121db13a28c159c8174a))

## [Unreleased]

### Added

- Added xyz [@your_username](https://github.com/your_username)

### Fixed

- Fixed zyx [@your_username](https://github.com/your_username)

## [1.1.0] - 2021-06-23

### Added

- Added x [@your_username](https://github.com/your_username)

### Changed

- Changed y [@your_username](https://github.com/your_username)

## [1.0.0] - 2021-06-20

### Added

- Inititated y [@your_username](https://github.com/your_username)
- Inititated z [@your_username](https://github.com/your_username)
