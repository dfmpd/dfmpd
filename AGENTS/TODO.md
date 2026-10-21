# DFMPD — Development Plan

> **Dot's Freakin Modular Player Daemon**  
> Everything is a plugin. Except the daemon. The daemon is eternal.
>
> Please don't mention MPD anymore. PLEASE.

**Status:** M0–M3 and external Playback PoC A implemented; later milestones remain design drafts\
**Last updated:** 2026-10-22\
**Implementation language:** Go  
**Repository starting point:** `LICENSE`, `README.md`, `go.mod` (implementation not started)

## 0. Purpose and non-goals

DFMPD is a **FLOSS, extensible music playback system**, consisting of a small daemon and interchangeable components. A user chooses the music source, playback engine, lyrics provider, frontend, configuration syntax, and eventually other facilities independently.

DFMPD is **not an MPD client, MPD-compatible server, or MPD protocol reimplementation**. Its similarity in naming does not imply protocol compatibility. It may integrate with other software through explicit adapters, without adopting their architecture.

The short-term aim is to play one local audio file through an mpv backend, control it from a CLI, and prove that the chosen boundaries allow another implementation to be substituted. **Do not make the entire plugin runtime a prerequisite for the first audible result.**

## 1. Architectural principles

1. **Minimal, durable core.** The daemon owns system state, playback queue, command routing, events, plugin registration, and eventually process lifecycle. It must not contain provider-specific networking, audio decoding, UI, or configuration-parser code.
2. **Everything else is replaceable.** Plugin categories define capabilities, not a mandatory bundled implementation.
3. **Modularity is behavioral, not merely a directory layout.** The eventual target is independently distributable and executable plugins. The first implementation may use Go interfaces while preserving future process boundaries.
4. **Multiple simultaneous frontends.** CLI, TUI, GUI, MPRIS and remote device clients must observe/control a coherent shared state rather than each owning a divergent playback session.
5. **Portable media identity.** A queued item has a stable identity; it does not store a potentially expiring stream URL as its canonical identity.
6. **Explicit error states and capabilities.** Unavailable sources, unsupported operations, and crashed plugins are surfaced rather than silently emulated or treated as daemon failure.
7. **Small first, extensible later.** Avoid speculative frameworks, premature SDK stability promises, and mandatory heavy desktop services.

## 2. Conceptual components

```text
                    +------------------------+
                    |      Players / UIs     |
                    | CLI / TUI / GUI / MPRIS|
                    | iPod / DeskThing       |
                    +-----------+------------+
                                |
                         Client IPC/API
                                |
                  +-------------v-------------+
                  |         dfmpd core         |
                  | queue / state / commands  |
                  | events / plugin registry  |
                  +--+-----------+----------+-+
                     |           |          |
                Source API  Playback API  Lyrics API
                     |           |          |
          Local / Navidrome  mpv      Navidrome / LRCLIB
                / YouTube

       ConfigLang (e.g. TOML) supplies a validated core config.
       Plugin process management will surround the adapters later.
```

### 2.1 Plugin categories

| Category | Examples | Contract/responsibility |
|---|---|---|
| **Source** | Local, Navidrome/OpenSubsonic, YouTube | Discover/search/list media, identify items, and resolve an item to playable media details. |
| **Playback** | mpv initially; other engines later | Start/load/play/pause/seek/stop and report playback state; own decoder/output interaction. |
| **Lyrics** | Navidrome, LRCLIB | Resolve plain or synchronized lyrics for a media identity and metadata. |
| **Player** (frontend/client) | CLI, TUI, GUI-Go, MPRIS | Translate user/external actions into core commands; subscribe to state/events. **Player** is DFMPD's chosen category label, *not* the audio decoding engine. |
| **ConfigLang** | TOML initially; others possible | Parse configuration syntax into a canonical, schema-validated configuration model. |

Categories are separate from installed implementations. Sources and lyrics providers may be active simultaneously, with priorities/fallbacks determined by policy. Several Players may connect concurrently. Initially only one output playback session is required; multi-zone/multi-output is **not yet designed**.

The YouTube Source is a proposed future plugin, not an MVP dependency; any actual integration needs to respect the service's terms and applicable rights.

### 2.2 Core responsibilities

- Own authoritative queue and playback-session state; serialize conflicting state mutations.
- Accept and route client commands; emit state-change/event notifications.
- Select plugin implementations and handle capabilities/errors.
- Later: discover, launch, supervise, restart and shut down external plugins safely.
- Maintain a stable, documented core-facing data model, without binding the core to a particular audio engine or source API.

**Not core responsibilities:** extracting metadata from every provider, networking to Navidrome, decoding FLAC/MP3, drawing UI, parsing arbitrary config syntaxes, fetching lyrics.

## 3. Media identity, resolution and delivery

This is an important previously agreed distinction:

- **Media Identity:** stable queue key identifying *which item* the user requested, including a source/plugin namespace and opaque item identifier. The core must not interpret a provider's opaque ID.
- **Media Descriptor:** transient, source-resolved information usable for playback (URI or access method, metadata, expiry hints, required headers/authorization where supported, and delivery capabilities).
- **Resolve:** the Source turns an Identity into a Descriptor **just before playback**. Design for initial resolution, possible prefetch, and refreshing expired descriptors.
- **Delivery:** control IPC, descriptor exchange, and transfer/streaming of media bytes are separate concerns. Passing a direct local path or accessible URL is an initial implementation choice, **not** a universal assumption.

```text
Queue: [MediaIdentity, MediaIdentity, ...]
                   |
                   v
          Source.resolve(identity)
                   |
             MediaDescriptor
                   |
                   v
          Playback.load(descriptor)
                   |
               audio output
```

**Open questions:** descriptor schema/version; direct URL versus proxied bytes versus file descriptor/stream transfer; credentials and secret handling; URL expiry and retry rules; Source/Playback capability negotiation; whether prefetch belongs to the core or a policy layer. Do not standardize the external wire schema before a second real plugin exercises it.

## 4. Playback: mpv first

The first Playback implementation controls **a separate mpv process**, rather than reimplementing audio codecs.

- Launch mpv from Go (`os/exec`) with an IPC socket (`--input-ipc-server`).
- Send/receive **mpv JSON IPC** messages over a Unix-domain socket.
- Implement lifecycle: start, readiness check, load, play/pause/resume, seek, stop, status, and clean shutdown.
- Capture relevant events/properties to maintain state; avoid treating successful command delivery as proof playback succeeded.
- Ensure stale socket cleanup, process-exit handling, timeouts/cancellation and no accidental orphan mpv instances.
- Use a private path under `$XDG_RUNTIME_DIR` where available (rather than a shared fixed `/tmp` socket) and correct permissions.
- Let mpv perform decoding: MP3 and FLAC can be test media, **not different playback backends**.

The initial test may pass an explicit local file path straight to mpv. This is a test seam; later it should arrive via `Source: Local` resolution. Avoid making `Playback` import `Source: Local`.

## 5. Clients and integrations

Provide separate executables by role rather than combining daemon and client
roles as subcommands of one executable:

- **Daemon (`dfmpd`):** owns Core, playback state, the queue and the DFMPD IPC server. Run in the foreground; do not implement self-daemonization. Future lifecycle management is intended to use a systemd user service.
- **CLI (`dfmpc`):** an independent client for playback commands, status and queue operations over DFMPD IPC. It does not start or manage the daemon.
- **TUI (`dfmpc-tui`):** a future independent client using the same daemon IPC; UI details remain deferred.
- **GUI-Go:** a proposed independent Go-based graphical client; UI toolkit **not decided**. Do not assume Qt/QML, LVGL, or any specific renderer is an agreed dependency.
- **MPRIS:** an external bridge/client (`dfmpd-mpris`) translating the shared state and operations to the Linux MPRIS D-Bus interface. It is **not a Playback engine**.
- **KDE Connect:** a potential transport/integration for Linux PC, Pixel, and the DIY iPod/DeskThing simultaneously; the standard KDE Connect Linux daemon is a first candidate. This is **a related device project**, not a hard dependency of DFMPD Core.

### iPod and DeskThing relationship

Two envisioned devices may use the same DFMPD concepts and client UI components, with different hardware, display profiles and source/playback selections:

- **DIY iPod:** offline/local playback first; compact screen, Click Wheel-like physical input, wired audio and possibly Bluetooth; Raspberry Pi Zero 2 W is a candidate, **not purchased/locked**.
- **DeskThing / Car Thing-like controller:** primarily remote monitoring/control of a Linux PC; KDE Connect being evaluated for music/notifications and other integrations.
- **Pico 2 H:** already on hand; candidate USB HID/serial input bridge for a reverse-engineered iPod Click Wheel. The Click Wheel, FPC breakout and final Pi platform remain unpurchased/unselected.
- **Shared UI/core:** desirable, but don't force DFMPD's server architecture to depend on either device's UI toolkit.

The devices can be built incrementally, independently of DFMPD plugin-process completion.

## 6. IPC and plugin lifecycle

### 6.1 Near-term implementation

Use idiomatic **Go interfaces inside one process** to keep source/playback boundaries clear. M2 provides a Unix-domain socket server for DFMPD control IPC; M3 adds the independent `dfmpc` client. The implemented client protocol is a restricted JSON-RPC 2.0 profile with newline-delimited JSON; batches are rejected without execution. Keep protocol scope and versioning explicit when implemented. Client IPC remains separate from mpv JSON IPC and the future external-plugin protocol.

### 6.2 Long-term implementation

Plugins should eventually be standalone processes distributed and updated separately from the daemon. Possible architecture:

```text
dfmpd
  |- internal adapter (development / fallback)
  |- exec source-local / source-navidrome
  |- exec playback-mpv
  |- exec lyrics-lrclib
  `- external clients via control IPC
```

Requirements before stabilizing an external plugin API:

- Plugin discovery, manifest/identity/version, registration/handshake.
- Capability negotiation and compatibility/version policy.
- Calls, events, cancellation and timeouts, including plugin health checks.
- Crash reporting/restarts with bounded backoff; state reconciliation.
- Plugin-specific configuration and secret handling.
- Authorization/trust/security boundaries for locally installed binaries.
- Separate lifecycle/transport contracts for **internal plugins**, **external plugins**, and **frontends** where appropriate.

A future **`dfmpd-plugin`** manager/CLI is envisioned for installing, inspecting, enabling or disabling plugins. Command syntax and packaging format remain undecided.

**Do not confuse** the mpv JSON IPC connection (daemon -> mpv) with the DFMPD client/plugin IPC contract. They are distinct protocols.

## 7. Configuration

`ConfigLang: TOML` is the first proposed configuration parser plugin. The core should consume a canonical validated configuration object, not directly depend on TOML syntax.

Potential concerns: selecting a parser during bootstrap, a minimal bootstrap grammar, plugin configuration sections, defaults, schema validation, live reload, per-device profiles, and sensible user/system config locations (XDG conventions on Linux). **Bootstrap design remains open:** making the parser itself a plugin introduces a circularity that needs an explicit minimal bootstrap mechanism.

*Illustrative target experience only (not a committed schema):*

```toml
[playback]
engine = "mpv"

[sources]
enabled = ["local", "navidrome"]

[lyrics]
providers = ["navidrome", "lrclib"]
strategy = "first-success"

[players]
enabled = ["cli", "mpris"]
```

## 8. Proposed repository layout (evolves with actual code)

```text
dfmpd/
├── cmd/
│   ├── dfmpd/          # foreground daemon entrypoint (M2)
│   ├── dfmpc/          # independent CLI client (M3)
│   └── dfmpc-tui/      # future independent TUI client
├── internal/
│   ├── core/           # commands, state, queue
│   ├── playback/       # interface + mpv adapter
│   ├── source/         # resolver interface + local source
│   ├── media/          # provisional identity, descriptor and metadata
│   └── ipc/            # client transport
├── PLAN.md
├── README.md
├── LICENSE
└── go.mod
```

**This tree is a suggested destination, not a request to create placeholder directories now.** Add packages only when code needs them. External-plugin SDK/repository structure remains intentionally undecided.

## 9. Delivery milestones

### M0 — Probe mpv (completed with a minimal Go probe)

- [x] Verify mpv launches idle with a private JSON IPC socket.
- [x] Load a local FLAC; play/pause/resume/stop/status (operator verified).
- Seek was explicitly deferred from M0 and is now covered by M1.
- [x] Inspect mpv events and failure behavior; document the actual fields used.

**Done when:** the mpv IPC experiment is reproducible without relying on a GUI.

### M1 — Playback adapter in Go

- [x] Minimal Go mpv controller, ideally first exercised by a Go test/demo.
- [x] Manage child process, socket readiness, calls/responses, events and shutdown.
- [x] Unit tests for command serialization and errors; small integration test if mpv exists.
- [x] Expose a narrow Playback interface; don't implement general plugin loading yet.

**Done when:** Go can play, pause, resume, seek, stop and inspect a real file via mpv, and clean up correctly.

Implementation: `internal/playback` defines the provisional internal contract;
`internal/playback/mpv` adapts the existing M0 transport. `cmd/mpv-probe` exercises
that contract. Absolute seek uses seconds; local input locations are a temporary
test seam, not the final Source/Descriptor schema. No external plugin runtime
or client IPC is implemented.

Validation: existing M0 tests plus adapter command/state/error tests and real mpv
with generated WAV/null output cover play, pause, resume, forward/backward seek,
stop, invalid media, recovery, process exit and cleanup. M1 manual acceptance
testing is complete: the operator verified FLAC audio output, play, stop, pause,
resume and seek.

### M2 — Daemon Core + IPC

- [x] Implement `dfmpd` as a long-running foreground process, without self-daemonization.
- [x] Connect Core to the existing Playback Adapter; introduce basic authoritative playback state and command routing.
- [x] Implement a Unix-domain socket server; choose/document the minimal DFMPD client protocol and socket lifecycle.
- [x] Ensure concurrent requests have deterministic state updates and clean shutdown releases owned resources.
- [x] Verify playback operations and status through IPC tests or a test harness; the production CLI belongs to M3.

**Done when:** a foreground `dfmpd` serves Unix-domain socket requests, controls one playback session through the existing adapter, reports shared state and shuts down cleanly. `dfmpc` and queue management are not required for M2 completion. A systemd user service is a future deployment option, not an M2 prerequisite.

M2 implementation: `cmd/dfmpd`, `internal/core` and `internal/ipc` reuse the
unchanged M1 adapter. Playback operations are serialized; status is fetched only
on request, with no cache, polling or events.
Client IPC uses newline-delimited JSON over a private XDG runtime Unix socket.
The JSON-RPC 2.0 profile rejects entire batches without execution and is not
fully compliant. See `cmd/dfmpd/README.md` for methods, limits and failure semantics.

Validation covers Core serialization/cancellation, multiple IPC clients, malformed
and oversized requests, connection limits, disconnects, real mpv operations and
failure, and SIGINT/SIGTERM cleanup. Real mpv tests use generated WAV/null output;
audible FLAC playback through M2 IPC remains a manual acceptance check.

### M3 — CLI + Queue + Local Source

- [x] Implement independent `dfmpc` playback commands and status queries over DFMPD IPC; do not start or manage `dfmpd` from the client.
- [x] Implement minimal Core-owned queue management and expose it through IPC and CLI: add/list/remove items and next/previous, with basic error handling.
- [x] Add Media Identity and initial Descriptor types; keep identities in the queue and resolve descriptors immediately before playback.
- [x] Implement the minimal local Source needed for this flow; prove it can be substituted without Core importing its implementation. Do not freeze the final Source/Descriptor or external-plugin schema.

**Done when:** one terminal runs `dfmpd` and another uses `dfmpc` for playback control, status and a minimal queue of local songs stored by identity and resolved before playback. The CLI does not own a separate playback session or daemon lifecycle.

Implementation: `cmd/dfmpc` uses the bounded JSON-RPC client in
`internal/ipc/client.go`. Core serializes queue and playback operations together.
Queue entries contain a separate entry ID and Media Identity, permitting duplicate
media. The Local Resolver validates absolute regular-file paths immediately
before playback; only the CLI converts relative paths. Descriptor and Metadata
remain provisional internal types, with no external-plugin wire contract.
Descriptors are transient and never appear in queue lists or public state.

Queue lists use offset/limit pagination (default 20, maximum 100), shortened as
needed to fit the existing 64 KiB frame limit. Queue capacity is 1024 entries.
Selection denotes the last accepted queue play, not proof of audible playback.
A potentially executed failed/timed-out play leaves an unknown outcome and
blocks navigation until an explicit successful play/stop resolves uncertainty.
Resolve failure does not change current playback or the queue. Next/previous
require a playing queue selection; paused/stopped navigation, automatic advance
and persistence are not implemented. Selected or uncertain attempted entries
cannot be removed; select another entry or use direct playback first.
Existing M2 playback methods remain available; successful direct play detaches
the selection without deleting queue entries.

Validation: normal tests, race tests, vet and build pass. Tests cover duplicate
identities, pagination including JSON expansion, resolver failures, uncertain
timeouts, concurrent clients and coexistence with M2 playback methods.
Real mpv tests use two generated FLAC files with null audio output and verify
next/previous against mpv's actual loaded path. Audible M3 acceptance remains
manual. See `cmd/dfmpc/README.md` and `cmd/dfmpd/README.md` for usage and IPC.

### Milestone mapping after the executable-role decision

| Previous plan | Updated plan |
|---|---|
| M0 mpv probe / M1 Playback Adapter | Completed; preserve existing implementations. |
| M2 daemon state, routing and client IPC | Remain in M2, now explicitly a foreground daemon with a Unix-domain socket server and Playback Adapter integration. |
| M2 CLI and two-terminal acceptance criterion | Move to M3 with the independent `dfmpc` client. M2 is verified through IPC tests/a test harness. |
| M3 Local Source, Media Identity/Descriptor and queue | Remain in M3 as the foundation for CLI + Queue; preserve late resolution and Source/Core separation. |
| M4–M6 remote sources, additional Players and external plugins | Retain their existing scope. The future TUI is named `dfmpc-tui`; GUI and MPRIS remain independent clients. |

### External Playback PoC A — before repository separation

- [x] Add `cmd/dfmpd-playback-mpv` using the unchanged mpv Adapter.
- [x] Add a daemon-side `playback.Adapter` proxy over private stdin/stdout JSON-RPC.
- [x] Validate version, kind, required operations and local-file capability at initialization.
- [x] Bound startup, RPC and shutdown waits; correlate unique IDs and never replay mutations.
- [x] Keep Core, Queue, Media Identity, Local Source and public client IPC unchanged.
- [x] Exercise real mpv/null-output FLAC playback, queue navigation and normal process cleanup.

The daemon explicitly selects the external executable with `-playback-plugin`.
The built-in Adapter remains the default; there is no implicit fallback from an
external failure. This advances the M4 external-process experiment only, not
M4's remote Source or M6's general plugin lifecycle. B's crash/descendant
supervision, independent repositories and SDK publication remain deferred.
See `cmd/dfmpd-playback-mpv/README.md` for the experimental wire contract,
limits, tests and abnormal-exit cleanup limitations.

### M4 — First remote/alternative plugin

- [ ] Navidrome/OpenSubsonic Source as the first target (depending on priorities).
- [ ] Check descriptor expiry and authentication behavior.
- [ ] Validate source/playback capabilities and revise data contracts before freezing them.
- [x] Begin external-process plugin proof of concept: Playback-only milestone A above; broader lifecycle remains deferred.

**Done when:** a second source uses the same queue and playback model.

### M5 — Lyrics + external Players

- [ ] Navidrome/LRCLIB lyrics provider(s), priority/fallback strategy.
- [ ] `dfmpd-mpris` bridge as a separate client.
- [ ] Independent `dfmpc-tui` and/or GUI clients depending on which device reaches prototype stage first.

### M6 — Standalone plugins and device integration

- [ ] External plugin protocol and lifecycle supervisor.
- [ ] `dfmpd-plugin` management tool.
- [ ] Evaluate resource budget on a Raspberry Pi Zero 2 W class device.
- [ ] Integrate compact hardware input/display and KDE Connect as separate concerns.

Milestone numbers describe dependencies, **not promised release versions**.

## 10. Decisions vs open questions

### Decided / strongly established

- Go language; a small authoritative daemon.
- Separate executables: `dfmpd` for Core/state/queue/IPC, `dfmpc` for CLI and future `dfmpc-tui` for TUI; GUI/MPRIS can be independent clients.
- `dfmpc` does not start or manage the daemon. `dfmpd` runs in the foreground without self-daemonization; future management uses a systemd user service.
- Unix-domain sockets for the initial DFMPD client IPC transport, separate from Playback mpv JSON IPC.
- No inherent MPD protocol compatibility.
- Source / Playback / Lyrics / Player / ConfigLang modular categories.
- mpv external process as first Playback backend.
- Stable Media Identity in queue; late resolution to Descriptor.
- Multiple Players can coexist; external process plugins are the long-term direction.
- FLOSS licensing intention; see `LICENSE` for actual terms.

### Tentative / to validate

- Future client protocol evolution/versioning; the implemented socket and framing are documented in `cmd/dfmpd/README.md`. External-plugin transport remains undecided.
- Detailed plugin ABI, manifest, schema, security model, process supervisor.
- ConfigLang bootstrap and final configuration schema.
- Media Descriptor schema and delivery transport.
- Repository/package structure and names of other future binaries; `dfmpd`, `dfmpc` and `dfmpc-tui` are decided.
- GUI technology, hardware/OS selection, and KDE Connect responsibilities.
- Resource limits, caching, offline-first behavior and cross-platform support.

## 11. Immediate next action

M0/M1 remain unchanged; M2, M3 and external Playback PoC A are implemented.
Review A and manually verify audible FLAC playback and queue navigation through
the external Playback executable. Stronger lifecycle supervision belongs to B.
Navidrome and MPRIS remain future milestones; no external-plugin contract is
frozen. Keep this document as a living plan and distinguish validated behavior
from future ambitions.

*Let it rip!*
