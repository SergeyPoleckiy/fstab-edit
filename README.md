# fstab-edit

[![CI](https://github.com/SergeyPoleckiy/fstab-edit/actions/workflows/ci.yml/badge.svg)](https://github.com/SergeyPoleckiy/fstab-edit/actions/workflows/ci.yml)

[Русский](README.ru.md)

A utility for safely editing `/etc/fstab` on Linux: a CLI for automation and a TUI for interactive work — both interfaces are implemented. Key properties: transactional writes, static validation of the candidate file, backups, rollback, and byte-for-byte preservation of unchanged content.

## Table of Contents

- [AI-assisted development](#ai-assisted-development)
- [Status](#status)
- [Installation](#installation)
- [CLI](#cli)
- [TUI](#tui)
- [Configuration](#configuration)
- [Logging](#logging)
- [Localization](#localization)
- [Project structure](#project-structure)
- [Development safety](#development-safety)
- [Requirements](#requirements)
- [Checks](#checks)
- [Security documentation](#security-documentation)
- [Save transaction](#save-transaction)
- [Backups and rotation](#backups-and-rotation)
- [Project documentation](#project-documentation)
- [License](#license)

## AI-assisted development

This program is developed with the help of artificial intelligence — **Kimi Code CLI by Moonshot AI (月之暗面)**. The AI assists in designing the architecture and writing code, tests, and documentation. Every implemented block passes automated checks and a separate code review; requirements and final project decisions remain with the author.

## Status

- Stages **1–4** are implemented: **1 — core and CLI** (parser, model, editor, validator, transaction with rollback, backup, config/logging/i18n, 10 commands); **2 — TUI** (table, forms, undo, transactional save, diff/mounts/devices/log/history/help screens); **3 — devices and documentation** (lsblk/blkid, UUID/LABEL/FS-type/option completion, user guide with `make pdf`); **4 — polish** (syntax and severity highlighting, light/high-contrast themes, TUI tech debt, man page and `make install`). The CLI builds into a static `fstab-edit` binary; the TUI (package `tui/`, the `tui` command) is implemented: entry table, add/edit forms, undo, transactional save with a rollback result, diff/mounts/block-devices/log/history/help screens, in-form completion, four built-in themes. The full user guide is [docs/user-guide.en.md](docs/user-guide.en.md) (PDFs are built by `make pdf`).
- The data model, lossless parser, selectors, and in-memory mutations are implemented: Add, Edit, Delete, Comment and Uncomment, including associated comment blocks.
- The public diagnostics model and the internal read-only validator are implemented: field, source/mount/FS-type, option, duplicate, and special-warning checks run deterministically against the in-memory document.
- A secure runner is implemented: executables are resolved only in fixed system directories, arguments are passed without a shell, and the locale is pinned to `LC_ALL=C`. By default a **10 second** timeout and a **64 KiB limit per stream** of stdout/stderr apply.
- `findmnt --verify --tab-file` and a private candidate check are implemented. The candidate content is written without normalization or added newline to a `0600` file inside a `0700` temporary directory; a `RemoveAll` attempt follows on every outcome. A cleanup failure adds `findmnt.candidate_cleanup/warning` while preserving the primary diagnostics and `Execution.Err`. The internal validator and the external report are merged in internal → external order.
- Output classification is verified against fixtures of the observed **util-linux 2.42.4** format. `findmnt` only statically checks the candidate file and never mounts anything.
- Raw stdout/stderr may contain sensitive data and are not meant to be written to the log without redaction; the public `Report` contains only generalized diagnostics, while the raw output is kept separately in `Execution`.
- Secure snapshot/base-hash conflict handling, stable flock, and the low-level atomic `Replace` with metadata/xattr preservation are implemented. Backups and rotation are implemented: `Create`, `Open`, `List`, `Select`, and `Rotate` in `internal/backup`.
- The domain transaction is implemented in `internal/transaction`: internal validation, lock, re-check of the base SHA-256, `findmnt` candidate check, backup of the current operation, atomic replace, post-check, a single automatic rollback, and rotation after full success.
- The CLI is implemented on Cobra (`cmd/`, `main.go`): ten commands — `list`, `add`, `edit`, `delete`, `comment`, `uncomment`, `validate`, `verify-mount`, `backup`, `rollback`; dry-run, `[y/N]` confirmations, unified diff, entry selectors with ambiguity code 8, and `table`/`json`/`csv`/`tsv` output formats with `schema_version: 1`. CLI tests work only with temporary files, a fake runner, and scripted stdin; the real `/etc/fstab` and findmnt are never touched.
- YAML configuration with priorities and security checks (`internal/config`), structured JSON Lines logging with rotation and redaction (`internal/logger`), and RU/EN localization (`internal/i18n`) are implemented; both packages are wired into the CLI: settings are resolved before handlers, and the log is opened before any mutation and records the outcome of operations. The `tui` command opens the full TUI editor (see the "TUI" section below).
- The current development baseline: [Specification v2.0](docs/spec/fstab-editor-v2.md).
- The original edition is preserved: [Specification v1.0](docs/spec/fstab-editor.md).

## Installation

Building from source requires **Go ≥ 1.22** and `make` (the module has no external dependencies beyond those pinned in `go.mod`; the binary builds statically). The version is derived automatically from git (`git describe --tags`): building from tag `v1.0` yields version `v1.0`, from commits after the tag — `v1.0-N-g<hash>`, from an untagged tree — the short commit hash:

```bash
git clone https://github.com/SergeyPoleckiy/fstab-edit.git
cd fstab-edit
make            # builds ./fstab-edit
make install    # build + install to /usr/local (requires root)
```

`sudo make install` copies the static binary to `/usr/local/bin`
and the `fstab-edit(1)` man page to `/usr/local/share/man/man1` (gzipped,
as customary in distributions; the Russian version goes to
`/usr/local/share/man/ru/man1`);
after installation, help is available via `man fstab-edit`. The base directory
is changed with the `PREFIX` variable, and `DESTDIR` is a staging directory
for package builds (e.g. `make install DESTDIR=/tmp/stage PREFIX=/usr`).

Uninstallation is the reverse operation, removing the same files:

```bash
sudo make uninstall
```

After installation you can optionally create `/etc/fstab-edit/config.yaml` (see the "Configuration" section below). The log file `/var/log/fstab-edit.log` is created automatically on the first mutating operation; if the log is unavailable, the mutation is blocked (exit code 3 or 6).

## CLI

Build and help:

```bash
go build -o fstab-edit .
./fstab-edit --help
./fstab-edit --version
```

The default target file is `/etc/fstab`; the `--file=<path>` flag overrides it (handy for chroots and tests). The `--help` and `--version` flags do not read the target and do not run `findmnt`.

Commands:

| Command | Purpose |
|---------|---------|
| `list` | Show active and commented-out entries |
| `add` | Add a new entry |
| `edit` | Modify selected fields/comment |
| `delete` | Delete an entry |
| `comment` | Comment out an active entry |
| `uncomment` | Enable a commented-out entry |
| `validate` | Check the document (`--offline` — internal validator only) |
| `verify-mount` | Static `findmnt --verify` against the actual bytes of the target |
| `backup` | Create a manual copy without modifying the target |
| `rollback` | Restore a selected copy |
| `tui` | Open the interactive TUI editor (see the "TUI" section) |

An entry selector is a positional `<id>` (line number, mount point, or source) or exactly one of `--line`, `--mount-point`, `--device`; multiple matches yield exit code **8** with a list of candidates. `add` requires `--device`, `--mount`, `--type`, `--options` (dump/pass default to `0`); `edit` requires a selector and at least one modifying flag.

A `[y/N]` confirmation (default: no) is mandatory for mutations and `rollback`: without a TTY, `--yes` is required, and `--yes` does not bypass validation errors, selector ambiguity, or external failures. The interactive diff and warning preview are printed to stderr only in prompt mode: `--yes` skips the prompt entirely and prints nothing, while the diff and diagnostics remain available in the machine-JSON fields (`diff`, `diagnostics`). `--dry-run` shows the diff and diagnostics without changing anything and without requiring `--yes`.

Output formats `--output=table|json|csv|tsv`; JSON uses `schema_version: 1`. Normal data goes to stdout (exactly one structured document for machine formats); diagnostics, diffs, and prompts go to stderr. Exit codes: 0 success (warnings allowed), 1 validation, 2 usage, 3 insufficient privileges (unsafe config, unavailable log), 4 lock/base conflict, 5 external utility, 6 I/O/backup/write, 7 critical rollback (highest priority), 8 ambiguous selector.

## TUI

The interactive editor (`fstab-edit tui`, or just `fstab-edit` with no arguments) opens a table of the target's entries. An interactive terminal (TTY) is required: without one the command exits with a usage error (exit 2) and the `tui.no_tty` message. The no-argument launch is rewritten to `tui` only with an empty argv — `--help`, `--version`, and the other commands behave as before.

Main keys (main screen):

| Key | Action |
|-----|--------|
| `↑`/`↓`, `k`/`j` | Move the cursor |
| `Enter` | Edit the entry under the cursor |
| `Ins`, `i` | Add an entry |
| `Del` | Delete the entry under the cursor |
| `c` | Comment/uncomment the entry under the cursor |
| `d` | Diff of unsaved changes |
| `M`, `m` | Mount status (findmnt) |
| `B`, `b` | Block devices (lsblk) |
| `H`, `h` | Backup history |
| `L`, `l` | Operation log |
| `F1` or `?` | Help |
| `Ctrl+Z` | Undo the last action |
| `F5` | Reload the target (asks for confirmation if there are unsaved changes) |
| `Ctrl+S`/`F2` | Save via transaction (backup → findmnt → atomic replace) |
| `q`/`Ctrl+Q`/`Ctrl+C`/`F10` | Quit (asks for confirmation [y/N] if there are unsaved changes, default No) |

The history screens (`H`) support `d` (diff a copy against the target) and `r` (restore a copy via the domain rollback with a safety backup). The devices screen (`B`) shows a flat `lsblk` list (PATH, TYPE, FSTYPE, SIZE, MOUNTPOINT, LABEL, UUID) and refreshes on every entry. Saving and restoring go through the same transaction/rollback mechanisms as the CLI: there is no separate unsafe write implementation in the TUI.

The entry form (Ins/Enter) supports completion: the hint panel filters by prefix for the "FS type" and "Options" fields, while the "Device" field shows all devices without filtering; `Ctrl+N`/`Ctrl+P` cycle through the suggestions and insert the selected one into the field. For devices, the inserted value is chosen by priority `UUID=` → `PARTUUID=` → `LABEL=` → `PARTLABEL=` → `/dev/...` (stable against disk renaming); the list comes from `lsblk`; if the selected identifier is not unique (shared by several devices), the hint is marked "(non-unique identifier)". If `lsblk` is unavailable, the form works without device hints. The set of options depends on the FS type (meaningless combinations, e.g. `discard` for network filesystems, are excluded).

The color theme is `tui.theme` in the configuration: `dark` (default), `light`, `high-contrast` (extra contrast for poor vision/projectors), or `monochrome`; an unknown value is rejected with a configuration error (exit code 2). On top of any built-in theme (including `monochrome`), the optional `tui.colors` applies — overriding the colors of semantic palette roles (hex `#rrggbb`, ANSI number `0`–`255`, or ANSI color name `black`…`bright_white`; roles: `title`, `header`, `selected_fg`/`selected_bg`, `muted`, `info`, `dirty`, `error`, `warning`, `field_source`, `field_mount`, `field_fstype`, `field_options`); an unknown role or an invalid format is rejected with a configuration error (exit code 2). Details and an example are in the user guide §5.7. Errors and warnings are marked with textual markers (`!`, `?`) in all themes. The interface language is resolved via the common `--lang`/`FSTAB_EDIT_LANG`/locale chain.

Limitations: the mouse is not supported (keyboard only). Deferred for the future: option-purpose descriptions in form hints, .deb/.rpm/.pkg.tar.zst packages. User documentation: [docs/user-guide.en.md](docs/user-guide.en.md) (English) and [docs/user-guide.md](docs/user-guide.md) (Russian); PDF versions are built with `make pdf` (pandoc + xelatex).

## Configuration

Settings are resolved by the priority from spec §13.1: **CLI flags > `FSTAB_EDIT_*` env > user YAML > system YAML > defaults**. A CLI flag applies only when explicitly passed (Cobra `Changed`); otherwise the value is taken from the underlying level.

- Automatic load paths: system — `/etc/fstab-edit/config.yaml` (requires root ownership), user — `~/.config/fstab-edit/config.yaml`. A root process (euid 0) does not automatically load the user config (spec §13.2).
- `--config <path>` sets an explicit YAML instead of system/user; env still takes precedence over it.
- Env variables: `FSTAB_EDIT_FILE`, `FSTAB_EDIT_BACKUP_DIR`, `FSTAB_EDIT_LOG_LEVEL`, `FSTAB_EDIT_LANG`, `FSTAB_EDIT_OUTPUT`. Unknown `FSTAB_EDIT_*` variables print `warning: ...` to stderr and are ignored.
- YAML schema (spec §13.3): `fstab_path`, `backup.dir|prefix|keep`, `log.file|level|max_size_mb|keep`, `tui.theme`, `tui.colors`. Language and output format are not read from YAML (env/CLI only). The `tui.theme` key sets the TUI appearance (`dark` — default, `light`, `high-contrast`, `monochrome`); `tui.colors` — optional palette role color overrides on top of the theme (see the TUI section above).
- Security checks (spec §13.2): a symlinked configuration is rejected, the file must not be group/others-writable (otherwise exit code 3), the system config must be owned by root, user/explicit — by the effective UID. Malformed YAML or values — exit code 2.

## Logging

The file log is JSON Lines, by default `/var/log/fstab-edit.log` (mode `0600`).

- An event is a single JSON line: `severity`, `timestamp` (RFC 3339 UTC), `real_uid`/`effective_uid`, `sudo_user`/`sudo_uid` (only with a numeric `SUDO_UID`), `operation` (`add`, `edit`, `delete`, `comment`, `uncomment`, `backup`, `rollback`), `target`, `backup_id`, `result` (`success`/`error`/`cancelled`), `error_code`, `duration_ms`, `old_value`/`new_value`. Codes and fields are not localized.
- The log is opened **before** any mutation (`add`/`edit`/`delete`/`comment`/`uncomment`/`rollback`/`backup`): an unavailable log blocks the change to the target with code 3 (no privileges) or 6 (other I/O) — backup/lock/write never start. A write failure after a successful replace is ignored and does not prevent the mandatory rollback.
- Read-only commands (`list`, `validate`, `verify-mount`) and `--dry-run` do not create the file log at all. The TUI does not open the log at startup: the log is opened inside the save transaction, so an unavailable log blocks exactly the save.
- Rotation: when `log.max_size_mb` (default 10 MiB) is exceeded, the current file becomes `<file>.1`, and archives shift up to `log.keep` (default 5).
- Redaction: `old_value`/`new_value` (changed diff lines) are masked — `password=`, `credentials=`, `token=`, URL userinfo, etc. are replaced with `***`.

## Localization

The message language is selected by the priority from spec §15: `--lang` > `FSTAB_EDIT_LANG` > `LC_ALL` > `LANG` > Russian (default). `ru` and `en` are supported; help texts, flag usage, error diagnostics, and prompt confirmations are translated. "Yes" answers: ru — `д`, `да`, `y`, `yes`; en — `y`, `yes`; any other answer and an empty line mean refusal (default No).

Translation limitations: validator domain diagnostics (`models.Diagnostic.Message` — Russian strings of the lower layer) and error cause chains of lower levels are not translated; Cobra's system messages about an unknown flag are printed as-is.

Examples:

```bash
fstab-edit list
fstab-edit list -o json

fstab-edit add --device UUID=abc-123 --mount /mnt/data --type ext4 \
    --options defaults,noatime --dump 0 --pass 2

fstab-edit edit --mount-point /mnt/data --options defaults,noatime,nofail
fstab-edit delete --mount-point /mnt/data --yes

fstab-edit comment --line 12
fstab-edit uncomment --line 12

fstab-edit validate --file ./fstab.test --offline
fstab-edit verify-mount --file ./fstab.test

fstab-edit backup --file ./fstab.test
fstab-edit rollback --dry-run
fstab-edit rollback --restore=2026-11-18_15-30-45.123456789Z
```

CLI tests do not use the real `/etc/fstab`: all scenarios work with temporary files, a fake runner, and scripted stdin.

## Project structure

```text
fstab-edit/
├── cmd/                  # Cobra commands: flags, prompt, exit mapping
├── main.go               # Production wiring of process dependencies
├── internal/
│   ├── cliapp/           # CLI domain layer: selectors, preview, service, rollback
│   ├── diff/             # Line-based unified diff for preview
│   ├── output/           # table/json/csv/tsv renderers, schema v1
│   ├── parser/           # Lossless parser and byte-for-byte preservation
│   ├── editor/           # Selectors and in-memory mutations
│   ├── validator/        # Syntactic and semantic checks
│   ├── writer/           # Locks, version control, atomic write
│   ├── backup/           # Backup, metadata, selection and rotation
│   ├── transaction/      # Domain write orchestration and automatic rollback
│   ├── config/           # YAML configuration and priorities
│   ├── logger/           # Structured log
│   ├── i18n/             # RU/EN localization
│   └── sysutil/          # Secure launching of external utilities
├── pkg/
│   └── models/           # Public data models
├── tui/                  # TUI interface
├── docs/                 # Documentation: user-guide.en.md (EN), user-guide.md (RU),
│                         # DEVELOPMENT.md, spec/, man/ (EN + RU)
├── examples/             # Configuration examples (config-minimal.yaml, config-full.yaml)
├── AGENTS.md             # Project rules for AI agents (architecture, workflow)
├── CHANGELOG.md          # Changelog (Keep a Changelog)
├── Makefile              # build/install/uninstall/test/pdf/version
├── LICENSE.en            # MIT license, English text
├── LICENSE.ru            # Unofficial Russian translation of the MIT license
├── README.md             # This file — project overview in English (primary)
└── README.ru.md          # Project overview in Russian
```

## Development safety

Never use the real `/etc/fstab` of the host system for development or manual experiments. findmnt tests use a fake runner and never launch the external utility; candidate tests work only with private temporary files; CLI and file tests use temporary files and the `--file` option; destructive and privileged checks run only in isolated VMs.

## Requirements

- Go 1.22 or newer.
- Linux.
- Regular tests do not require root.
- `findmnt` is mandatory for the pre-save check.
- `lsblk` is used by the devices screen (`B`) and form completion; without it the TUI works without device hints.
- `blkid` is used for extended device properties (optional).
- `golangci-lint` is used for local checks and CI.

## Checks

```bash
go test ./... -v
go test ./... -cover
go test -race ./...
go vet ./...
golangci-lint run
```

## Security documentation

Current guarantees of the low-level writer:

1. `ReadSnapshot` reads the exact bytes only from a safely opened regular-file descriptor. `VerifyBaseHash` re-reads the target and detects external changes by SHA-256, even if size and mtime are unchanged; read-only APIs do not create a lock.
2. A lock is created only by an explicit `AcquireLock` in a separate stable file whose name contains the SHA-256 of the canonical target. The owner and modes are checked: directory `0700`, file `0600`; protected system/XDG/cache directories are used. The standard wait timeout is **10 seconds**. The library does not hold the lock during a prompt: its lifetime belongs to the future transaction.
3. `Replace` writes the candidate to a temp `0600` file in the parent directory, then strictly performs `fchown` → `fchmod` → xattr copying via descriptors → file sync → file close → rename → directory sync → re-read and SHA-256 verification. Before a successful rename, an error leaves the target old and triggers temp cleanup; cleanup errors are attached via `errors.Join` without losing the primary cause.
4. UID/GID, permission bits, and available xattrs, including zero-length values, are preserved. A failure to read or set a specific security xattr forbids the replace. SELinux/AppArmor policy is not changed; the enforcing VM test remains a separate release check and is not considered satisfied on a regular temp FS.
5. A directory sync or post-rename verification error is returned with `ReplaceResult.Replaced=true`. The low-level writer itself does not perform an automatic rollback: the domain transaction restores the backup of the current operation when `err != nil && result.Replaced`. With `Replaced=false`, the target is considered unchanged.

## Save transaction

Guarantees of `internal/transaction.Execute`:

1. A no-op candidate, byte-equal to the target, completes before lock, validation, backup, `findmnt`, and write with `Changed=false`; internal candidate errors forbid the lock and the changes.
2. The lock is held from re-reading the target and the base SHA-256 through backup, atomic replace, post-check, a possible rollback, and the rotation allowed after success. An external change to the target forbids backup/write and completes with the `conflict` class.
3. A backup is created only from a snapshot re-read under the lock and is re-verified via `backup.Open` before the replace.
4. Post-write warnings are acceptable; errors trigger exactly one attempt to restore the backup of the current transaction. `Replaced=false` never triggers a rollback.
5. A successful rollback returns the primary error with `RolledBack=true`. A rollback failure returns the `rollback` class, preserving the primary and rollback causes and the recovery paths of target/backup/metadata.
6. Rotation runs only after a fully successful post-check; the backup of the current transaction is passed as protected. A rotation error returns the `rotation_after_success` class but does not roll back the already successfully written target.
7. CLI, prompt, diff rendering, dry-run, and manual rollback are wired in `cmd/` and `main.go`; config/logger/i18n are wired into the CLI pipeline: settings are resolved in `PersistentPreRunE`, the log is opened before the mutation and records the outcome of operations with secret redaction. The transaction returns a typed `FailureKind`, which the CLI maps to stable exit codes.

## Backups and rotation

Implemented guarantees of `internal/backup` (`Create`, `Open`, `List`, `Select`, `Rotate`):

1. A backup ID is the core of the file name in UTC, strictly in the format `YYYY-MM-DD_HH-MM-SS.nnnnnnnnnZ` (exactly nine nanosecond digits and a literal `Z`); `created_at` in the sidecar must denote the same UTC time, including nanoseconds.
2. File names: `<prefix>.<backup ID>` for data and `<prefix>.<backup ID>.json` for the sidecar. The default prefix is `fstab.bak`; the default rotation keep is **10** newest copies. The default directory is the target's directory.
3. Collision semantics: if a name is taken, `Create` iterates numeric suffixes `.1`, `.2`, … (up to 1000 attempts); a symlink under a taken name is an unsafe substitution and is rejected without moving to a suffix.
4. Sidecar schema v1 (`schema_version: 1`) — strict JSON without unknown fields and trailing values: `backup_id`, `created_at`, `source_path` (canonical absolute clean target), `source_sha256`, `backup_sha256` (64 lowercase hex), `uid`, `gid`, `mode` (no higher than `07777`), `operation` (whitelist: `before-add`, `before-edit`, `before-delete`, `before-comment`, `before-uncomment`, `before-rollback`, `manual`), `program_version`, `source_verification` (whitelist: `not_checked`, `valid`, `invalid`, `unavailable`).
5. `Create` never modifies the target and returns a `Backup` only after writing both files with mode `0600` via `O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC`, fsyncing each file, re-reading, and matching the data SHA-256 with the metadata. A backup guarantees byte-for-byte restoration of the copy's data, but `source_verification` does not claim the damaged source is functional: `invalid`/`unavailable` record the fact of the check, they do not fix the document.
6. `Open`/`List`/`Select` reject symlink substitution at any level, a mode other than `0600`, corrupt schema, hash mismatch, invalid modes/enums/UTC, a foreign `source_path`, and a foreign prefix. `List` returns verified copies of the current target in deterministic newest-first order; `Select` distinguishes not found (`ErrBackupNotFound`) from an ambiguous collision ID (`ErrBackupAmbiguous`).
7. `Rotate` deletes only its own verified pairs (sidecar, then data): foreign, corrupted, symlink, and unverified entries of the directory are not candidates and are never deleted; `os.RemoveAll` is never called. `keep <= 0` is replaced by the default 10, protected IDs survive even inside the deletion zone, `os.ErrNotExist` on removal is not a failure, and a removal error stops the rotation with a partial `RotationResult` (`Failed`) and the primary error preserved via `errors.Is`. Rotation errors contain paths but not backup data, and do not affect the target.
8. The automatic rollback transaction uses the backup of the current operation directly and does not re-select it via `Select`. Manual rollback is wired in `cmd/rollback.go`: copy selection happens before the safety backup via a positional ID or `--restore` (default: newest), confirmation with a diff is mandatory (non-TTY requires `--yes`), and `--dry-run` shows the selected copy and diff without creating a safety copy and without writing the target.

## Project documentation

- [CHANGELOG.md](CHANGELOG.md) — changelog by release.
- [docs/user-guide.en.md](docs/user-guide.en.md) — user guide (English); [docs/user-guide.md](docs/user-guide.md) — Russian original (PDFs of both versions are built by `make pdf`).
- [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) — architecture, build, code standards, and the release process for developers.
- [examples/](examples/) — configuration examples (`config-minimal.yaml`, `config-full.yaml`).
- [docs/man/fstab-edit.1](docs/man/fstab-edit.1) — man page in English (primary); [docs/man/fstab-edit.ru.1](docs/man/fstab-edit.ru.1) — Russian translation (after `make install` — `man fstab-edit`; the language is selected by locale).

## License

- [MIT License — English](LICENSE.en)
- [MIT License — Russian translation](LICENSE.ru)

The Russian text is an unofficial translation; the English version takes legal precedence.
