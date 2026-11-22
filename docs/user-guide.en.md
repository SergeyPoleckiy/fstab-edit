---
title: fstab-edit — User Guide
---

[Русская версия](user-guide.md)

# fstab-edit — User Guide

## 1. What fstab-edit is

**fstab-edit** is a utility for safely editing `/etc/fstab` on Linux.
It combines two interfaces:

- **CLI** on Cobra — for automation and scripts (all commands work
  non-interactively via `--yes` and the machine formats `json`/`csv`/`tsv`);
- **TUI** on bubbletea — a full interactive editor with an entry table,
  forms, completion, and diagnostic screens.

Key properties:

- **Transactional writes**: before saving, the target is re-read and
  verified by SHA-256 (base-hash control), the candidate content is checked
  by the internal validator and `findmnt --verify`, and the write is an
  atomic rename preserving the owner, permissions, and xattrs.
- **Backups and rollback**: a backup is created before every mutation, and
  exactly one automatic rollback attempt is made if an error occurs after
  the write.
- **Byte-for-byte preservation**: the lossless parser does not normalize
  the document — unchanged lines, comments, field order, and a missing
  trailing newline are preserved.
- **Logging**: every mutating operation is recorded in a structured JSON
  Lines log with secret masking.

The program is a static binary with no runtime dependencies (except the
system utilities listed in section 2).

## 2. Installation

### 2.1. Requirements

- **Linux** — the target platform.
- **Go 1.22 or newer** and `make` — only for building from source.
- Optional util-linux utilities (used when available):
  - `findmnt` — static check before saving and the `verify-mount` command;
    without it saving is blocked (exit code 5);
  - `lsblk` — the block-device screen in the TUI (key `B`) and completion
    of the "Device" form field; when unavailable the screen shows an error
    and device completion simply does not work;
  - `blkid` — extended device properties: available as an internal API
    (`ProbeDevice`) but not called from production flows; the
    `UUID=`/`PARTUUID=`/`LABEL=`/`PARTLABEL=` completion values come from
    the `lsblk` output.

### 2.2. Building from source

```bash
git clone https://github.com/SergeyPoleckiy/fstab-edit.git
cd fstab-edit
make            # builds ./fstab-edit
sudo make install    # build + install of the binary and the man page
```

`make install` copies the static binary to `$(PREFIX)/bin`
(`PREFIX` defaults to `/usr/local`; for another path use
`sudo make install PREFIX=/usr`) and the `fstab-edit(1)` man page (gzipped,
as customary in distributions) to `$(PREFIX)/share/man/man1`. After
installation, the full command and exit-code reference is available via
`man fstab-edit`. `DESTDIR` is supported for package builds or inspecting
the installed files (e.g. `make install DESTDIR=/tmp/stage`); uninstall with
`sudo make uninstall` (with the same `PREFIX`/`DESTDIR`).

The version is derived automatically from git (`git describe --tags`):
building from tag `v1.0` yields version `v1.0`, from commits after the tag —
`v1.0-N-g<hash>`, from an untagged tree — the short commit hash. Manual
version input is not supported.

### 2.3. Installing a prebuilt binary

Download the static binary from the project releases and copy it to a
directory in `PATH`:

```bash
sudo cp fstab-edit /usr/local/bin/
sudo chmod 755 /usr/local/bin/fstab-edit
fstab-edit --version
```

The release archive contains only the binary; the man page is not installed
this way — the full command reference is available in `docs/man/fstab-edit.1`
of the sources (install with `sudo make install`, see §2.2) and on the
release page of the project repository.

Uninstalling is simply deleting the binary: the program installs no service
files and does not modify the system:

```bash
sudo rm /usr/local/bin/fstab-edit
```

## 3. Quick start

```bash
# View entries (read-only, no log is created)
fstab-edit list

# Add an entry: dry-run shows the diff without writing
fstab-edit add --device UUID=3f5c8a12-... --mount /mnt/data --type ext4 \
    --options defaults,noatime --dry-run

# The same for real: --yes is required without a TTY
fstab-edit add --device UUID=3f5c8a12-... --mount /mnt/data --type ext4 \
    --options defaults,noatime --yes

# Check the document without changes
fstab-edit validate
fstab-edit verify-mount

# Open the interactive editor
fstab-edit tui        # or simply: fstab-edit
```

The default target file is `/etc/fstab`; for experiments and chroots use
`--file=<path>`. The `list`, `validate`, `verify-mount` commands and the
`--dry-run` mode write nothing: neither the target, nor the log, nor a backup.

## 4. CLI

### 4.1. Command overview

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
| `tui` | Open the interactive TUI editor |

### 4.2. Global (persistent) flags

Apply to all commands:

| Flag | Default | Description |
|------|---------|-------------|
| `--file <path>` | `/etc/fstab` | Path to the fstab file being edited |
| `--dry-run` | `false` | Show the diff without writing, backup, or lock |
| `--yes`, `-y` | `false` | Confirm an otherwise valid operation without a prompt |
| `--output`, `-o` | `table` | Output format: `table`, `json`, `csv`, `tsv` |
| `--log-level` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `--verbose`, `-v` | `false` | Additional diagnostics |
| `--quiet`, `-q` | `false` | Suppress normal stdout (errors are still printed) |
| `--backup-dir <path>` | target's directory | Directory for backup copies |
| `--offline` | `false` | Disable checks against external resources |
| `--lang <ru|en>` | auto-resolve | Message language |
| `--config <path>` | not set (auto-discovery, see §6.2) | Path to a YAML configuration |

A flag applies only when explicitly passed (Cobra `Changed`): if a flag is
not given, the value is taken from env, YAML, or the default (see section 6).

### 4.3. Entry selectors

The `edit`, `delete`, `comment`, `uncomment` commands require an entry
selector — a positional `<id>` or exactly one of the flags. A positional
`<id>` is interpreted as follows: a number is the physical line number of
the entry, any other value is a mount point; the source is not set
positionally — only via `--device`:

| Flag | Value |
|------|-------|
| `--line <n>` | Physical line number of the entry (positive integer) |
| `--mount-point <path>` | Full decoded mount point value |
| `--device <source>` | Full decoded source value of the entry |

Selection outcomes:

- Entry not found (zero matches) — exit code **1** (validation).
- Invalid arguments (a non-numeric or non-positive `--line`, etc.) —
  exit code **2** (usage).
- Several matches (ambiguous selector) — exit code **8** with a list of
  candidates. Interactively (a TTY and no `--yes`), the CLI first prints
  the candidate lines to stderr and offers to enter the number of one of
  them: the chosen line is applied, while an invalid number, empty input,
  or EOF exits with the same exit code **8** — an arbitrary candidate is
  never chosen automatically. Outside a TTY or with `--yes` the chooser is
  not invoked: exit code **8** with the candidate list immediately.

### 4.4. `list`

Shows the active and commented-out entries of the target:

```bash
fstab-edit list
fstab-edit list -o json | jq .
fstab-edit list --file ./fstab.test
```

The JSON output has `schema_version: 1`. Normal data goes to stdout,
diagnostics — to stderr.

### 4.5. `add`

Adds a new entry. `--device`, `--mount`, `--type`, and `--options` are
required; `dump` and `pass` default to `0`.

| Flag | Description |
|------|-------------|
| `--device <source>` | Entry source: `UUID=...`, `LABEL=...`, `/dev/...` |
| `--mount <path>` | Mount point |
| `--type <fs>` | Filesystem type: `ext4`, `xfs`, `btrfs`, `swap`, `nfs`, etc. |
| `--options <list>` | Comma-separated list of mount options |
| `--dump <n>` | Dump field (default `0`) |
| `--pass <n>` | Pass field (default `0`) |
| `--comment <text>` | Comment block in front of the entry |

```bash
fstab-edit add --device UUID=3f5c8a12-9d4e-4b7a-8c1f-2a6d9e0b4f55 \
    --mount /mnt/data --type ext4 --options defaults,noatime \
    --dump 0 --pass 2 --comment "Data disk" --dry-run
```

### 4.6. `edit`

Modifies the selected fields of an entry. Requires a selector and at least
one modifying flag. Unspecified fields are left untouched.

| Flag | Description |
|------|-------------|
| `--line` / `--mount-point` | Entry selector |
| `--device <source>` | New source value |
| `--mount <path>` | New mount point |
| `--type <fs>` | New FS type |
| `--options <list>` | New option list |
| `--dump <n>` / `--pass <n>` | New dump/pass fields |
| `--unset-dump` / `--unset-pass` | Remove the optional dump/pass field |
| `--comment <text>` | Replace the entry's comment block |
| `--clear-comment` | Remove the entry's comment block |

```bash
fstab-edit edit --mount-point /mnt/data --options defaults,noatime,nofail
fstab-edit edit --line 12 --clear-comment --dry-run
```

### 4.7. `delete`

Deletes an entry by selector:

```bash
fstab-edit delete --mount-point /mnt/data --dry-run
fstab-edit delete --mount-point /mnt/data --yes
```

### 4.8. `comment` and `uncomment`

Comments out an active entry or enables a commented-out one. When
commenting, the associated comment block is handled together with the entry:

```bash
fstab-edit comment --line 12 --dry-run
fstab-edit uncomment --line 12 --yes
```

### 4.9. `validate`

Checks the document with the internal validator (fields, source/mount/FS
type, options, duplicates, special warnings) and, without `--offline`, with
the external static check `findmnt --verify --tab-file` against a private
temporary file. `findmnt` does not mount anything — it only statically
checks the candidate. Diagnostics are merged in internal → external order:

```bash
fstab-edit validate
fstab-edit validate --offline --file ./fstab.test
```

Warnings are acceptable and yield exit code 0; errors — exit code 1.

### 4.10. `verify-mount`

Runs only the static `findmnt --verify` check against the actual bytes of
the target:

```bash
fstab-edit verify-mount
fstab-edit verify-mount --file ./fstab.test
```

### 4.11. `backup`

Creates a manual copy of the target without modifying it:

```bash
fstab-edit backup
fstab-edit backup --backup-dir /var/backups/fstab
fstab-edit backup --dry-run   # creates nothing and does not read the target
```

`--dry-run` runs before any I/O: the target is not read and no copy is
created; a structured result with an empty `backup_id` and `dry_run: true`
is printed.

### 4.12. `rollback`

Restores the target from a selected backup copy. The copy is selected by a
positional backup ID or `--restore=<id>` (default: newest):

```bash
fstab-edit rollback --dry-run                        # read-only pass, no write
fstab-edit rollback                                  # newest, with confirmation
fstab-edit rollback 2026-11-18_15-30-45.123456789Z   # a specific copy
fstab-edit rollback --restore=2026-11-18_15-30-45.123456789Z --yes
```

`--dry-run` performs a read-only pass: it selects and verifies the copy but
does not create a safety backup and does not write the target; a structured
result is published (`selected_backup_id`, `dry_run: true`), and no diff is
printed. Interactively without `--dry-run`, the diff of the selected copy
against the target is shown in the confirmation prompt before the write. A
safety backup of the current target is created before the write. The copy
is deployed via the domain rollback with backup checks (hash, sidecar,
owner, mode).

### 4.13. Confirmations, diff, and output

- A `[y/N]` confirmation (default: no) is mandatory for mutations and
  `rollback`. Without a TTY, `--yes` is required. "Yes" answers: `д`, `да`,
  `y`, `yes`; any other answer and an empty line mean refusal.
- `--yes` does not bypass validation errors, selector ambiguity, or
  external failures — only the prompt.
- The interactive diff and warning preview are printed to stderr only in
  prompt mode; with `--yes` the diff and diagnostics are available in the
  machine-JSON fields (`diff`, `diagnostics`).
- `--dry-run` shows the diff and diagnostics without changing anything and
  without requiring `--yes`.
- Normal data goes to stdout (exactly one structured document for machine
  formats); diagnostics, diffs, and prompts go to stderr.

## 5. TUI

### 5.1. Launch

```bash
fstab-edit tui        # or simply fstab-edit with no arguments
```

An interactive terminal (TTY) is required: without one the command exits
with a usage error (exit code 2) and the `tui.no_tty` message. The
no-argument launch is rewritten to `tui` only with an empty argv — `--help`,
`--version`, and the other commands behave as usual. The mouse is not
supported (keyboard only).

### 5.2. Main screen — entry table

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
| `Ctrl+S`, `F2` | Save via transaction (backup → findmnt → atomic replace) |
| `q`, `Ctrl+Q`, `Ctrl+C`, `F10` | Quit (asks for confirmation [y/N] if there are unsaved changes, default No) |

Errors and warnings are marked with textual markers (`!`, `?`) in all
themes — the interface stays readable even in a monochrome terminal.

### 5.3. Entry form (Ins/Enter)

Fields: **Device** (source), **Mount point**, **FS type**, **Options**,
**dump**, **pass**, plus the comment block:

| Key | Action |
|-----|--------|
| `Tab` / `Shift+Tab`, `↓`/`↑` | Move focus across fields (circular) |
| `Enter` | Confirm: validation → apply → table |
| `Esc` | Cancel, return to the table |
| `d` | Toggle the diff preview panel |
| `Ctrl+N` | Next completion suggestion (insert into the field) |
| `Ctrl+P` | Previous completion suggestion (insert into the field) |

**Completion.** For the "Device", "FS type", and "Options" fields, a hint
panel is shown under the fields. The "FS type" and "Options" fields filter
suggestions by the already entered prefix; the "Device" field shows all
devices without prefix filtering (the value is entered as a whole):

- **Device** — the list of block devices from `lsblk` (loaded when the form
  opens). The inserted value is chosen by priority (spec v2 stage 3):
  `UUID=<uuid>` — stable against device renaming, then
  `PARTUUID=<partuuid>` — the GPT partition UUID, independent of the
  filesystem, then `LABEL=<label>` and `PARTLABEL=<partition label>`; only
  when all four are missing — the `/dev/...` path. The right column of the
  hint shows the type, FS, size, mount point, and label; if the selected
  identifier occurs on more than one device (e.g. after cloning a disk),
  the column gets the "(non-unique identifier)" mark — insertion still
  works, but fstab may reference the wrong partition. If `lsblk` is
  unavailable, the form works as before — the panel is simply not shown.
- **FS type** — the list of supported filesystem types.
- **Options** — the base set of mount options plus additions per FS type;
  meaningless combinations (e.g. `discard` for network filesystems) are
  excluded.

The `Ctrl+N`/`Ctrl+P` keys cycle through the suggestions and insert the
selected one into the active field; with an empty hint panel they do
nothing.

### 5.4. Block device screen (`B`)

A flat table of the `lsblk` output: `PATH`, `TYPE`, `FSTYPE`, `SIZE`,
`MOUNTPOINT`, `LABEL`, `UUID` (the device tree is flattened into a list).
The data is loaded on every entry to the screen through the same secure
runner as the other checks:

| Key | Action |
|-----|--------|
| `↑`/`↓`, `k`/`j` | Move the cursor |
| `PgUp`/`PgDown` | Scroll the list |
| `Esc` | Return to the table |

An empty list is a normal state (a separate caption); a load error is shown
as text on the screen and does not block exiting with `Esc`.

### 5.5. Read-only screens (mounts, log)

The mount status screen (`M`) and the log screen (`L`) support navigation:

| Key | Action |
|-----|--------|
| `↑`/`↓`, `k`/`j` | Scroll |
| `PgUp`/`PgDown` | Page up/down |
| `Esc` | Return to the table |

The help screen (`F1`/`?`) is static (the whole key list fits one screen)
and is closed only with `Esc`.

### 5.6. Backup history screen (`H`)

| Key | Action |
|-----|--------|
| `↑`/`↓`, `k`/`j` | Select a copy |
| `d` | Diff the copy against the target |
| `r` | Restore the copy via the domain rollback (with a safety backup) |
| `Esc` | Return to the table |

Saving (`Ctrl+S`/`F2`) and restoring (`r`) in the TUI go through the same
transaction/rollback mechanisms as the CLI: there is no separate unsafe
write implementation in the TUI. An unavailable log blocks exactly the
save: the log is opened inside the transaction.

### 5.7. Themes and language

The color theme is `tui.theme` in the configuration: `dark` (default),
`light` (light background), `high-contrast` (extra contrast for poor
vision/projectors), or `monochrome` (no color, for basic terminals). An
unknown value is rejected with a configuration error.

On top of any built-in theme (including `monochrome`), the optional
`tui.colors` key applies — a custom palette: overriding the color of
individual semantic roles. Example:

```yaml
tui:
  theme: dark
  colors:
    title: "#00ffff"   # hex #rrggbb
    selected_fg: "0"   # ANSI number 0–255
    error: red         # ANSI color name
```

Allowed value formats: hex `#rrggbb` (exactly six hex digits), an ANSI
number `0`–`255`, or an ANSI color name — the eight base names (`black`,
`red`, `green`, `yellow`, `blue`, `magenta`, `cyan`, `white`) and the eight
bright variants (`bright_black` … `bright_white`). Partial formats (`#rgb`,
`256`, `gray`) are invalid. Allowed roles:

| Role | What it colors |
|------|----------------|
| `title` | Screen titles |
| `header` | Table header |
| `selected_fg` / `selected_bg` | Text / background of the selected row |
| `muted` | Muted text (key hints) |
| `info` | Informational text |
| `dirty` | The dirty indicator `*` in the title |
| `error` / `warning` | Semantic interface error / warning |
| `field_source` | The "Device" field of an entry (syntax highlighting) |
| `field_mount` | The "Mount point" field |
| `field_fstype` | The "FS type" field |
| `field_options` | The "Options" field |

Only the color is overridden: style attributes (bold, faint, underline,
selection inversion) are preserved, and the textual markers (`!`, `?`)
work in all themes. An unknown role or an invalid color format is rejected
with a configuration error (exit code 2) whose message names the rejected
pair. The severity-diagnostic roles of the validator are not available to
the user: their colors are fixed by spec §5.3.

The interface language is resolved via the common `--lang`/`FSTAB_EDIT_LANG`/locale
chain (see section 6).

### 5.8. Behavior on conflicts and errors

- **Unsaved changes**: quit (`q` etc.) and reload (`F5`) ask for
  confirmation `[y/N]` (default No).
- **Base-hash conflict**: if the target changed externally after loading,
  saving is blocked with the `conflict` class (exit code 4 in the CLI; in
  the TUI — an error message, the target stays old).
- **findmnt failure**: saving is blocked (exit code 5 / a `!` message in
  the TUI), the target is not touched.
- **Error after the write**: the domain transaction makes exactly one
  automatic rollback attempt from the backup of the current operation; on
  success the error is returned with `RolledBack=true`, on failure —
  critical exit code 7.
- **Undo (`Ctrl+Z`)** reverts the last in-memory action; it does not apply
  to changes already saved to disk.

## 6. Configuration (YAML)

### 6.1. Setting priorities

Settings are resolved by priority: **CLI flags > `FSTAB_EDIT_*` env > user
YAML > system YAML > defaults**. A CLI flag applies only when explicitly
passed (Cobra `Changed`); otherwise the value is taken from the underlying
level.

### 6.2. Automatic load paths

- System: `/etc/fstab-edit/config.yaml` — must be owned by root;
- User: `~/.config/fstab-edit/config.yaml` — not loaded automatically for a
  root process (euid 0).
- `--config <path>` sets an explicit YAML instead of system/user; env still
  takes precedence over it.

Security checks: a symlinked configuration is rejected; the file must not
be group/others-writable (otherwise exit code 3); the system config must be
owned by root, user/explicit — by the effective UID. Malformed YAML or
values — exit code 2.

### 6.3. Environment variables

| Variable | Setting |
|----------|---------|
| `FSTAB_EDIT_FILE` | Target path |
| `FSTAB_EDIT_BACKUP_DIR` | Backup directory |
| `FSTAB_EDIT_LOG_LEVEL` | Log level |
| `FSTAB_EDIT_LANG` | Language (`ru`/`en`) |
| `FSTAB_EDIT_OUTPUT` | Output format |

Unknown `FSTAB_EDIT_*` variables print `warning: ...` to stderr and are
ignored.

### 6.4. YAML schema

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `fstab_path` | string | `/etc/fstab` | Target file |
| `backup.dir` | string | target's directory | Backup directory |
| `backup.prefix` | string | `fstab.bak` | Backup file name prefix |
| `backup.keep` | int | `10` | How many newest copies rotation keeps |
| `log.file` | string | `/var/log/fstab-edit.log` | Log file |
| `log.level` | string | `info` | Log level |
| `log.max_size_mb` | int | `10` | Log rotation threshold, MiB |
| `log.keep` | int | `5` | How many rotated log archives to keep |
| `tui.theme` | string | `dark` | TUI theme: `dark`, `light`, `high-contrast`, or `monochrome` |
| `tui.colors` | map string→string | — | Palette role color overrides on top of `tui.theme` (see §5.7) |

Language and output format are not read from YAML (env/CLI only).

### 6.5. Configuration example

```yaml
# /etc/fstab-edit/config.yaml
fstab_path: /etc/fstab

backup:
  dir: /var/backups/fstab
  prefix: fstab.bak
  keep: 10

log:
  file: /var/log/fstab-edit.log
  level: info
  max_size_mb: 10
  keep: 5

tui:
  theme: dark
  colors:
    title: "#00ffff"
    selected_fg: "0"
    error: red
```

## 7. Operation log

The file log is JSON Lines, by default `/var/log/fstab-edit.log` (mode
`0600`). One JSON object per line:

| Field | Description |
|-------|-------------|
| `severity` | Level: `debug`, `info`, `warn`, `error` |
| `timestamp` | RFC 3339 UTC |
| `real_uid` / `effective_uid` | Process UIDs |
| `sudo_user` / `sudo_uid` | Only with a numeric `SUDO_UID` in the environment |
| `operation` | `add`, `edit`, `delete`, `comment`, `uncomment`, `backup`, `rollback` |
| `target` | Path to the edited file |
| `backup_id` | Backup ID of the operation (if created) |
| `result` | `success` / `error` / `cancelled` |
| `error_code` | Numeric error code (on `error`) |
| `duration_ms` | Operation duration |
| `old_value` / `new_value` | Changed diff lines (with redaction) |

Details:

- The log is opened **before** any mutation: an unavailable log blocks the
  change to the target with code 3 (no privileges) or 6 (other I/O) —
  backup/lock/write never start.
- Read-only commands (`list`, `validate`, `verify-mount`) and `--dry-run`
  do not create the file log at all. The TUI does not open the log at
  startup — only inside the save transaction.
- A log write failure after a successful replace is ignored and does not
  prevent the mandatory rollback.
- **Rotation**: when `log.max_size_mb` (default 10 MiB) is exceeded, the
  current file becomes `<file>.1`, and archives shift up to `log.keep`
  (default 5).
- **Redaction**: `old_value`/`new_value` are masked — `password=`,
  `credentials=`, `token=`, URL userinfo, etc. are replaced with `***`.

## 8. Backups and rollback

### 8.1. How copies are organized

- A backup ID is the core of the file name in UTC, strictly in the format
  `YYYY-MM-DD_HH-MM-SS.nnnnnnnnnZ` (nine nanosecond digits and a literal
  `Z`), e.g. `2026-11-18_15-30-45.123456789Z`.
- File names: `<prefix>.<backup ID>` (data) and `<prefix>.<backup ID>.json`
  (sidecar metadata). The default prefix is `fstab.bak`; the default
  directory is the target's directory.
- The sidecar (schema v1) is strict JSON: `backup_id`, `created_at`,
  `source_path`, `source_sha256`, `backup_sha256`, `uid`, `gid`, `mode`,
  `operation` (`before-add`, `before-edit`, `before-delete`, `before-comment`,
  `before-uncomment`, `before-rollback`, `manual`), `program_version`,
  `source_verification` (`not_checked`, `valid`, `invalid`, `unavailable`).
- A backup is created from a snapshot re-read under the lock; the files are
  written with mode `0600` via `O_CREAT|O_EXCL|O_NOFOLLOW|O_CLOEXEC`, each
  file is fsynced and re-read for SHA-256 verification. A backup guarantees
  byte-for-byte restoration of the copy's data.

### 8.2. Automatic and manual copies

- Every mutation (`add`/`edit`/`delete`/`comment`/`uncomment`) and
  `rollback` creates a backup of the current target **before** the change
  (`before-*`).
- `fstab-edit backup` creates a copy manually (`operation: manual`) without
  changing the target.
- In the TUI, the history screen (`H`) shows verified copies of the current
  target in newest-first order; `d` — diff the copy against the target,
  `r` — restore.

### 8.3. Rotation

After a fully successful write, rotation runs: `backup.keep` (default 10)
newest copies are kept; only its own verified pairs are deleted. Foreign,
corrupted, and symlink entries of the directory are not candidates and are
never deleted. The backup of the current transaction is passed as protected
and survives the rotation.

### 8.4. Restoration

```bash
# Select and verify a copy without writing (read-only pass, no diff printed)
fstab-edit rollback --dry-run

# Newest copy, with interactive confirmation (diff — in the confirmation prompt)
fstab-edit rollback

# A specific copy, without a prompt
fstab-edit rollback --restore=2026-11-18_15-30-45.123456789Z --yes
```

Manual rollback: the copy is selected via a positional ID or `--restore`
(default: newest), confirmation is mandatory (non-TTY requires `--yes`),
the diff of the selected copy against the target is shown in the
confirmation prompt, and a safety backup of the current target is created
before the write. Automatic rollback (inside the transaction) uses the
backup of the current operation directly.

## 9. Diagnostics

### 9.1. Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success (warnings allowed) |
| 1 | Validation errors |
| 2 | Usage error: invalid arguments/flags, malformed configuration |
| 3 | Insufficient privileges: unsafe config, unavailable log |
| 4 | Lock/base-hash conflict (the target changed externally) |
| 5 | External utility failure (findmnt, lsblk) |
| 6 | I/O: backup, write, or other file errors |
| 7 | Critical rollback: error after the write, and the rollback failed (highest priority) |
| 8 | Ambiguous entry selector |

### 9.2. Typical errors and what to do

| Symptom | Cause | Solution |
|---------|-------|----------|
| `exit 3`, config complaint | The config is group/others-writable, symlinked, or owned by someone else | `chmod 600` on the config; remove the symlink; fix the owner |
| `exit 3`, log unavailable | `/var/log/fstab-edit.log` is not writable by the current user | Run via sudo or set `log.file` to a writable location |
| `exit 4` on save | The target was changed by another process after loading | Re-read (`F5` in the TUI) and repeat the edit |
| `exit 5` | `findmnt` not found or failed | Install util-linux; check `findmnt --version` |
| `exit 7` | The write succeeded but the post-check failed and the rollback could not restore | **Manually** compare the target and the backup from the error message; the backup preserves recovery paths |
| `exit 8` | The selector matched several entries | Refine the selector (`--line`) or inspect the candidate list in the output |
| `tui.no_tty`, `exit 2` | TUI without an interactive terminal | Run in a TTY; use CLI commands with `--yes` in scripts |
| Warning about duplicates | Two entries with the same mount point or source | Check `fstab-edit list`, delete/edit the extra one |

## 10. FAQ

**Is root required?**
For writing to `/etc/fstab` — yes. All checks, `--dry-run`, `list`,
`validate --offline`, and working with `--file` in an accessible location
work without root.

**What happens to my fstab if the utility crashes mid-write?**
The target is replaced by an atomic rename: before a successful rename the
old file is intact. If an error occurred after the write, the transaction
attempts to restore the backup of the current operation exactly once; if
that fails too — exit code 7 with the paths of the target and the backup
for manual restoration.

**Why not just use `echo >> /etc/fstab`?**
Direct writing does not check syntax and semantics, does not make a backup,
and does not verify that the file has not changed externally. fstab-edit
does all of this transactionally and logs every operation.

**How to experiment safely?**
`fstab-edit --file ./fstab.test tui` or any CLI command with `--file` and
`--dry-run`: nothing is written and no log is created.

**What is the difference between `validate` and `verify-mount`?**
`validate` — the internal validator plus `findmnt --verify` (an in-memory
candidate); `verify-mount` — only the static `findmnt --verify` against the
actual bytes of the target. Neither mounts nor changes anything.

**Can fstab-edit be used in Ansible/scripts?**
Yes: machine-readable `--output=json|csv|tsv` formats with
`schema_version: 1`, stable exit codes, `--yes` for non-TTY, and diff and
diagnostics in JSON fields.

**How do I disable colors in the TUI?**
`tui.theme: monochrome` in the config (see section 6). Errors and warnings
remain marked with the textual markers `!` and `?`.

**What if `lsblk` is not installed?**
The program works fully: screen `B` shows a load error, and device
completion in the form simply does not appear. The other features do not
depend on `lsblk`.
