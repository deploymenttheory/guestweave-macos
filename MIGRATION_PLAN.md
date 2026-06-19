# weave-1 → idiomatic SDK migration (zero raw framework imports)

Continuation plan after a context clear. Goal: **no weave package imports
`github.com/deploymenttheory/go-bindings-macosplatform/bindings/frameworks/*`** — maximal
scope (even the deep foundation files). weave now pins SDK **v0.5.0**, whose idiomatic layer
(`opinionated/idiomatic/framework/<fw>`) is complete enough for this.

---

## CRITICAL WORKING RULES (learned the hard way — obey these)

1. **gopls / IDE diagnostics are UNRELIABLE here** — they throw stale cross-module phantom
   errors constantly ("undefined: X", "use of internal package", wrong setter types). **NEVER
   trust them.** Ground truth is always: `go build ./internal/<pkg>/...`.
2. **The v0.5.0 API is in the MODULE CACHE, not the local SDK repo.** The local repo at
   `/Users/dafyddwatkins/GitHub/sdk/go-bindings-macosplatform` is AHEAD of v0.5.0 and will lie.
   Always check: `"$(go env GOMODCACHE)/github.com/deploymenttheory/go-bindings-macosplatform@v0.5.0/opinionated/idiomatic/framework/<fw>/"`.
   (And grep files directly — `grep -hE "^func New" <File>_generated.go` — not clever
   `bc`/`paste` pipelines, which gave false "MISSING".)
3. **The `.Unwrap()`-inline trick** is the key to dropping a raw import while a v0.5.0 setter
   still takes a raw type (`...*raw.VZX`): pass `wrapper.Unwrap()` *inline* as the argument.
   The raw import is only needed when weave *writes the type name* `virtualization.VZX`
   (var decl, struct field, signature, generic arg, composite literal). Passing a raw-typed
   *expression* inline needs no import.
4. **Idiomatic constructors take `string` for URLs**, not `*foundation.NSURL`. Thread paths as
   strings; this also helps drop raw `foundation`.
5. **Idiomatic `With*` setters**: take a *provider interface* for abstract-base element types
   (`BootLoaderProvider`, `GraphicsDeviceConfigurationProvider`, `StorageDeviceConfigurationProvider`,
   …) — a concrete wrapper satisfies it directly; take **raw variadic** (`...*raw.VZX`) for
   concrete element types (use `.Unwrap()` inline). `Set*` setters take raw / raw `NSArray`.
6. Migrate **framework-by-framework per file**: a multi-framework file (e.g. uses
   virtualization + appkit + corefoundation) can drop *virtualization* now and keep the others
   for a later phase — it stays in the raw-import list until ALL its frameworks are migrated.
7. weave's `internal/objcutil` is a weave package (not a raw framework) — keeping it is fine
   until Phase 3; it doesn't count as a raw framework import.

---

## ✅ PHASE 1 COMPLETE (2026-06-18)

The virtualization atomic chunk is **done and green**. `internal/vmconfig`, `internal/vmdirectory`,
`internal/command`, `internal/vm`, `internal/controlsocket` (+ `internal/vnc`) all build, `go vet`
is clean, and tests pass. `go build ./...` is clean **except** the pre-existing acceptance
darwin build-tag bug (Phase 5 — confirmed failing identically on the un-migrated tree).

What landed:
- `vm/vm.go`: dropped raw `virtualization` entirely. `VM.{VirtualMachine,Configuration}` are now
  `*idiomatic.{VirtualMachine,VirtualMachineConfiguration}`; `VMOptions` device fields are idiomatic
  provider slices + idiomatic enums; `craftConfiguration` rewritten to the fluent `With*` builder
  (took `string` paths); `machineState`/state consts, restore-image load, installer, NVRAM
  (`MacAuxiliaryStorage`/`EFIVariableStore`), `MacMachineIdentifier`, `Connect` all idiomatic. The
  delegate + main-thread `dispatch.RunOnMainThread` + manual-block machinery was **preserved** —
  the idiomatic async methods (`Start(ctx)`, etc.) do NOT dispatch to the VM queue, so swapping to
  them would break the @MainActor contract. Bridged to raw at `.Send` sites via `.ID()` / `.Unwrap()`.
- `vm/vm_recovery.go`, `vm/vm_snapshot.go`, `vnc/vnc_fullfledged.go`: `vm.VirtualMachine.Ptr()` →
  `.ID()` (purego.ID is a type alias of objc.ID).
- `controlsocket/controlsocket.go`: `Connect` returns `*idiomatic.VirtioSocketConnection`.
- `command/commands_run.go`: all device builders + helper return types migrated to idiomatic
  (storage/USB/NBD/block/disk-image attachments, directory shares, serial ports, rosetta, nested-virt
  check, automount tag). Base-type bridging uses `&wrapper.Unwrap().VZStorageDeviceAttachment` (embedded
  field — no raw type *named*). **Raw `virtualization` import retained** in this one file for exactly
  three deferred spots: `VZVirtualMachineView` (runUI, line ~885 → Phase 2 appkit), the multi-dir
  `NSDictionary[..., *virtualization.VZSharedDirectory]` generic arg (line ~1246 → Phase 4 foundation /
  SDK NSDictionary ergonomics), and `VZErrorVirtualMachineLimitExceeded` (line ~554 — the idiomatic
  layer surfaces no `VZErrorCode` enum). This is the **only** remaining raw `virtualization` import in
  the codebase. The file stays in the raw-list anyway for appkit/corefoundation/foundation.

## ✅ PHASE 2 COMPLETE (2026-06-18)

AppKit pasteboard + Vision OCR migrated; all build/vet/test green.
- `clipboard/macpb/pasteboard.go`: idiomatic `appkit.GeneralPasteboard()`/`Pasteboard`/`PasteboardItem`
  + idiomatic slice getters (`Types()`, `PasteboardItems()` — internally `purego.NSArrayToSlice`, the
  ABI-safe iteration the old code hand-rolled, so the old raw-send helpers were deleted). **Dropped raw
  appkit entirely.** `WriteObjects` (whose idiomatic signature still takes `*NSArray[raw.NSPasteboardWriting]`)
  is sent raw via `purego.Send(pb.ID(), "writeObjects:", …)` to avoid naming the raw protocol type.
- `ocr/ocrservice.go`: idiomatic `vision.NewRecognizeTextRequest()` (fluent `With*`),
  `NewImageRequestHandlerWithURLOptions(path string, nil)`, results via `vision.RequestFromID(req.ID()).Results()`
  then per-observation `RecognizedTextObservationFromID`/`DetectedObjectObservationFromID` (the idiomatic
  vision wrappers are thin — no inheritance embedding — so inherited methods need a re-wrap by `.ID()`).
  `performRequests:error:` sent raw (NSError** out-param, mirroring the generated method body) to avoid the
  `*NSArray[*raw.VNRequest]` type. **Dropped raw vision + raw foundation entirely — `vision` is now gone
  from the whole repo.**
- `vmconfig/platformdarwin.go`: `appkit.NSScreenMainScreen()` → idiomatic `appkit.MainScreen()` +
  `.Unwrap()`. **Dropped raw appkit.** Raw `corefoundation` **kept** (documented exception): the idiomatic
  `NewMacGraphicsDisplayConfigurationForScreenSizeInPoints(screen, sizeInPoints corefoundation.CGSize)`
  ctor takes a CGSize by value, so the size literal must name the raw type; no idiomatic CGSize ctor exists.

Remaining raw `appkit`/`corefoundation` is now only `command/commands_run.go`'s `runUI` (NSWindow +
`VZVirtualMachineView` + CGRect — genuine AppKit window plumbing) plus the platformdarwin CGSize exception.

---

## ✅ PHASES A/B + HTTP + vm.go Foundation cluster COMPLETE (2026-06-19)

Beyond Phases 1–2, the deep-foundation tail was split into A (safe/standalone), B (SDK-gapped),
C (runtime-risky/coupled) and worked through. SDK features landed along the way:
**v0.6.0** array-variadic ergonomics (idiomatic methods/ctors take `...Provider` / `...purego.IDer`
instead of raw `*NSArray[raw.X]`), **v0.7.0** idiomatic NSString key constants
(`NSURLVolumeAvailableCapacityKey()` etc. as `*String`), **v0.7.1** empty-array fix (`With*`
collection setters + variadic array params emit `[NSArray array]` not `nil` — fixed a live SIGSEGV).

weave migrations done since Phase 2:
- **A** `oci/oci_layerizer_diskv2.go` — LZ4 via idiomatic `foundation.Data` + `NSDataCompressionAlgorithmLZ4`.
- **B** `vmstorage/diskspace.go` — volume capacity via the idiomatic volume keys + `ResourceValuesForKeysError`
  + `DictionaryFromID`/`ObjectForKey`/`NumberFromID`. `diskimage/diskutil.go` — plist via idiomatic
  `PropertyListWithDataOptionsFormatError`; NSTask/NSPipe → `os/exec`.
- **HTTP** `fetcher.go` rewritten on **net/http** (Go-native `FetchRequest`/`FetchResponse`, streamed chunks,
  no cookie jar = Harbor CSRF req; honours `*_PROXY` env, NOT macOS system proxy — documented tradeoff).
  Cascaded through `oci_registry.go`, `vm.go` (IPSW HEAD+download), `commands_run.go`, `port_smoke_test.go`.
- **vm.go Foundation cluster** — IPSW threaded as a `string` (not `*NSURL`): `VMRetrieveIPSW(string)`,
  `NewVMInstallingFromIPSW(...,ipswLocation string,...)` (remote = http(s) prefix; symlinks = `filepath.EvalSymlinks`),
  `loadMacOSRestoreImage` → `idiomatic.LoadFileURL(ctx, path)`, `install(string)`. `vm_progress.go` NSProgress
  → idiomatic `foundation.Progress` (wrap `installer.Progress()` via `ProgressFromID(purego.Retain(...Ptr()))`).
  `commands_create.go` threads the IPSW string. **vm.go, vm_progress.go, commands_create.go now raw-free.**

**Rebrand:** binary `weave`→`guestweave`; app identity (codesign `--identifier`, os_log subsystem, OTel
ServiceName/ProcessExecutableName) = `com.deploymenttheory.guestweave`; CLI verbs/menus/About stay `weave`.

**Runtime-verified on a real Mac** (binary MUST be codesigned w/ `entitlements.plist`, else `VZErrorDomain:10004`):
`guestweave create --from-ipsw` (full macOS install), `run` headed (AppKit window), `pull ghcr.io/...`
(net/http + bearer auth). Build/sign steps: see README.md / CLAUDE.md.

**Raw `bindings/frameworks` importers: 51 → 4.**

---

## ✅ ABSOLUTE-ZERO REACHED (2026-06-19)

All four remaining files are migrated; `grep -rl go-bindings-macosplatform/bindings/frameworks
internal/ cmd/` returns **nothing**. `go build ./...` + `go vet ./...` clean (acceptance build-tag
bug fixed), `go test ./internal/...` passes. Validated against the local SDK via a temporary
`replace` directive (pending the **v0.8.0** release + version bump).

**SDK v0.8.0 features added (codegen emitter, `internal/codegen/frameworks/emit/idiomatic/`):**
- **Geometry type aliases** — new `structs.go` (`emitStructTypeAliases`) emits `type CGSize =
  raw.CGSize` etc. into `<pkg>_type_aliases_generated.go` for every value-type struct.
- **Cross-framework NSString externs** — `constants.go` now emits `objc.ID` accessors for NSString
  externs in non-foundation packages (typed → `.Ptr()`, untyped uintptr → `purego.CFConstant`),
  e.g. appkit `NSAboutPanelOption*`.
- **Dictionary string-keyed ergonomics** — `dict_augment.tmpl` adds `SetString(string, objc.ID)` +
  `Get(string)` on `MutableDictionary`.
- **Idiomatic-typed dict ctor params** — `buildParamConstructor` maps generic `NSDictionary`/
  `NSMutableDictionary` constructor params to `purego.IDer` (caller passes `.ID()`), unblocking
  `NewMultipleDirectoryShareWithDirectories`. FileHandle/single-VZSharedDirectory params use the
  `.Unwrap()`-inline trick at the weave call sites (no SDK change needed).

**weave migrations:**
- `vmconfig/platformdarwin.go` — raw `corefoundation` → idiomatic (CGSize alias).
- `ui/window.go` — idiomatic appkit (`SharedApplication`/`Window`/`Menu`/`MenuItem`/`SeparatorItem`),
  idiomatic geometry, About-panel keys via the new `objc.ID` accessors (`aboutPanelKey` helper
  deleted), `VirtualMachineView` idiomatic; delegate/menu-target `RegisterClass` machinery kept.
- `command/commands_run.go` — idiomatic appkit (`SharedApplication`/`SharedWorkspace().OpenURL`),
  idiomatic `foundation.FileHandle` (serial PTY/path, block device) bridged via `.Unwrap()`,
  multi-dir share via the idiomatic `MutableDictionary` + `purego.IDer` ctor, NSTask → `os/exec`,
  `objcErr.Code == int64(idvirt.VZErrorVirtualMachineLimitExceeded)` (idiomatic enum — see v0.8.1 below).
- `objcutil/utils.go` — now imports **idiomatic** foundation only. `NSStr`→`*foundation.String`,
  `GoStr`/`NSDataToBytes` take `objc.ID`, `BytesToNSData`→`*foundation.Data`, `NSURLFromPath`→
  `*foundation.URL`; `EnvironmentValue`/`ExpandTilde`/`ResolveBinaryPath` reimplemented in pure Go;
  dead helpers deleted (`NSArrayURLs`/`NSArrayStrings`/`EmptyNSArray`/`NSArrayFromIDs`/`NSStringArray`/
  `URLResourceValue`/`WrapperID`/`SafeIndex` + unused `Sel*`). Callers bridge with `.Unwrap()`/`.ID()`/
  `.Ptr()` inline (pasteboard.go, platformdarwin.go, vmdirectory_lume.go, mcp, commands_create/ipsw,
  vm_snapshot.go, port_smoke_test.go).
- `internal/acceptance` — renamed `suite_netbehavior_linux.go` → `suite_netbehavior_linuxguest.go`
  (the `_linux` GOOS suffix + `//go:build darwin` ANDed to "never built"; host is always darwin,
  the linux/macos names are the *guest* OS).

**Finalize after release:** in the SDK repo commit the emitter + regenerated output and tag v0.8.0;
in weave `go get github.com/deploymenttheory/go-bindings-macosplatform@v0.8.0` then
`go mod edit -dropreplace github.com/deploymenttheory/go-bindings-macosplatform`. Re-verify on a Mac:
build + codesign `guestweave`, then `create --from-ipsw → run` (headed: window/menus/About) and `pull`.

### SDK v0.8.1 — error-code enums surfaced idiomatically
`emitEnums` only re-exported enums *referenced by a method/ctor signature*, so error-code enums
(`VZErrorCode` etc.) — which cross the boundary as Go `error` values, never as typed returns — were
never surfaced. Fix (`enums.go`): also re-export any enum whose Go type name ends in `ErrorCode`.
~63 error-code enums now emitted across the idiomatic tree (49 `_enums_generated.go` files). weave
`commands_run.go` drops the local `const … = 6` and uses
`int64(idvirt.VZErrorVirtualMachineLimitExceeded)`. (Validated via the local `replace`; ship as v0.8.1
+ bump alongside the v0.8.0 finalize above.)

### Final whole-repo sweep (2026-06-19) — runtime floor, nothing left to migrate
`grep -rn --include="*.go" go-bindings-macosplatform/bindings/ .` over the **entire** repo (root +
internal + tests) shows **zero `bindings/frameworks`** imports. The only remaining `bindings/`
references are the **runtime layer**, which has no idiomatic equivalent (the idiomatic wrappers are
built *on* it) and is allowed by design — these are the permanent floor, not migration debt:
- `bindings/runtime/purego` (FFI message-send: `Send`/`RegisterName`/`GetClass`/`ID`/`Retain`/
  `NSString`/`GoString`/`RegisterClass`/`NewBlock`/`NSErrorToError`/`Dlsym`/`RegisterFunc`/`CFString`/
  `OSStatusError`/`IDer`/…) — `ui/window.go`, `vm/vm.go`+`vm_recovery.go`+`vm_snapshot.go`,
  `vnc/vnc_fullfledged.go`, `ocr/ocrservice.go`, `diskimage/diskutil.go`, `vmstorage/diskspace.go`,
  `credentials/credentials_keychain.go`, `clipboard/macpb/pasteboard.go`, `objcutil/utils.go`.
- `bindings/runtime/cgo` as `dispatch` (`RunOnMainThread`/`PumpMainRunLoop`) — `vm/*`, `vnc`,
  `clipboard/engine.go`, `command/commands_run.go`, root `execute.go`.
- `bindings/runtime/purego/objcerrors` (`ObjCError`/`NSErrorToError`) — `objcutil/utils.go`,
  `command/commands_run.go`, `vmstorage/vmstoragehelper.go`.

Optional future cleanup (NOT raw-framework debt): `credentials/credentials_keychain.go` could move
from direct purego Security calls onto the SDK's `opinionated/custom/keychain`; several
`purego.NSString`/`GoString` spot-uses could route through `objcutil`. Neither affects the zero-raw-
frameworks bar.

---

## (historical) OUTSTANDING WORK — now done, retained for context

Four files remained. Each was blocked on a specific SDK feature (not just mechanical edits) or was the
`objcutil` cascade. Steps are ordered by impact / dependency.

### Step 1 — SDK feature: idiomatic geometry types  ⟶ unblocks platformdarwin.go (4→3) + helps window.go/runUI
The screen-size graphics ctor and the NSWindow ctor take `corefoundation.CGRect`/`CGSize` **by value**, so
constructing the literal names the raw type. Add to the SDK an idiomatic re-export of `CGRect`/`CGPoint`/`CGSize`
as **type aliases** (`type CGSize = rawcorefoundation.CGSize`) in
`opinionated/idiomatic/framework/corefoundation` (emit via codegen, or hand-author in `opinionated/custom`).
An alias *is* the raw type, so it satisfies the raw-taking ctors, but weave imports `opinionated/idiomatic/…`,
not `bindings/frameworks/…`. Then weave:
- `vmconfig/platformdarwin.go` — swap the `corefoundation` import to the idiomatic one; the `CGSize{…}` literal is
  unchanged → **raw-free**.
- `ui/window.go` / `commands_run.go` runUI — same swap for `CGRect`/`CGSize`/`CGPoint`.

### Step 2 — SDK feature: NSDictionary string-keyed ergonomic  ⟶ unblocks the multi-dir share (+ diskutil sugar)
`commands_run.go`'s multi-directory share builds `NSDictionary[*NSString,*VZSharedDirectory]` (names raw). Add a
codegen ergonomic on the idiomatic `Dictionary`/`MutableDictionary`: a string-keyed builder
(`Set(key string, value …)`) + `Get(key string)` / `Keys()` / `Values()` slice getters, so callers build and read
keyed dictionaries without naming the raw element type. Then rewrite `commands_run.go`'s multi-dir share with it.

### Step 3 — SDK feature: extend NSString-extern constants to appkit  ⟶ unblocks window.go About panel
The v0.7.0 NSString-extern accessor feature is foundation-only (idiomatic packages alias RAW foundation as
`foundation`, so `String`/`StringFromID` resolve only there). For appkit's `NSAboutPanelOption*` keys, either
(a) extend `emitConstants` to emit `*foundation.String` accessors cross-package (import idiomatic foundation under
a distinct alias), or (b) emit `objc.ID` accessors for non-foundation NSString externs and pass them as keys.

### Step 4 — weave: finish `ui/window.go` + `commands_run.go` (after Steps 1–3)
- `ui/window.go` — appkit type names → idiomatic appkit (`Application`/`Window`/`Menu`/`MenuItem`/`Workspace`);
  geometry via Step 1; About keys via Step 3; `VZVirtualMachineView` → idiomatic + `SetContentView(&view.Unwrap().NSView)`.
  Keep the `purego.RegisterClass` delegate / menu-target machinery (runtime, allowed).
- `command/commands_run.go` —
  - `VZErrorVirtualMachineLimitExceeded` → a local `const` (idiomatic surfaces no `VZErrorCode`).
  - NSFileHandle (serial PTY, block device) → idiomatic `foundation.FileHandle` (verify `InitWithFileDescriptor…`).
  - NSTask (tar extraction in `createConfiguration`) → `os/exec`.
  - `NSWorkspace`/`NSURL` (open VNC URL) → idiomatic appkit + string URL.
  - multi-dir `VZSharedDirectory` dict → Step 2.
  - the no-graphics `NSApplication` run → idiomatic appkit.

### Step 5 — `objcutil/utils.go` cascade ("removed last")
Reimplement the FFI helpers (`NSStr`/`GoStr`/`NSData*`/`NSArray*`/`AllocClass`/`ResolveBinaryPath`/`ExpandTilde`/…)
on **idiomatic foundation** (their signatures currently name raw `*foundation.NSString` etc.). Either change the
signatures to idiomatic foundation types (cascades to every caller) or migrate callers off `objcutil` first, then
shrink `objcutil/utils.go` to raw-free runtime helpers. Largest mechanical step.

### Step 6 — green gate (Phase 5)
- Fix the pre-existing acceptance darwin build-tag bug: `internal/acceptance/suite_netbehavior_macos.go` + `main.go`
  reference symbols defined only in the linux-tagged `suite_netbehavior_linux.go` (`_linux.go` => linux-only;
  `_macos.go` is NOT a GOOS so it builds everywhere). Move the shared infra (config, probes, reachability, guest
  channels, OCI-cache bridge) into an untagged `suite_netbehavior_common.go`; give the macOS suite a real `darwin`
  constraint; gate the `main.go` registry entries per-OS.
- Final gate: `grep -rl go-bindings-macosplatform/bindings/frameworks internal/ cmd/` returns nothing;
  `go build ./...` + `go vet ./...` clean; `go test ./internal/...` passes. Re-verify on a Mac:
  create-from-ipsw + run headed + pull.

---

## VERIFICATION
`go build $(go list ./... | grep -v /internal/acceptance)` after each file/group (ignore gopls — Rule 1).
Watch for "imported and not used" after dropping raw imports. Runtime (build + codesign `guestweave`):
`create --from-ipsw … → run` (headed) and `pull <oci-image>`.

## SDK workflow
SDK source at `/Users/dafyddwatkins/GitHub/sdk/go-bindings-macosplatform`. Idiomatic packages are CODEGEN
OUTPUT — edit the emitter (`internal/codegen/frameworks/emit/idiomatic/`) + templates, then
`go run ./cmd/generate/ idiomatic` (NOT `bindings`, which re-emits only the raw layer). Cut a release; weave
bumps via `go get github.com/deploymenttheory/go-bindings-macosplatform@vX.Y.Z`.
