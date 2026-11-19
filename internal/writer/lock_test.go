package writer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testSystemLockConfig подставляет temp target и temp lock directory вместо
// производственных /etc/fstab и /run/lock/fstab-edit.
func testSystemLockConfig(target, directory string) lockConfig {
	return lockConfig{
		systemTarget:    target,
		systemDirectory: directory,
		uid:             os.Geteuid(),
	}
}

// testRuntimeLockConfig создаёт безопасный XDG runtime base и конфигурацию
// не-system target, не затрагивая настоящее окружение процесса.
func testRuntimeLockConfig(t *testing.T) lockConfig {
	t.Helper()
	runtimeDirectory := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	return lockConfig{
		systemTarget:     filepath.Join(t.TempDir(), "system-fstab"),
		systemDirectory:  filepath.Join(t.TempDir(), "system-locks"),
		runtimeDirectory: runtimeDirectory,
		cacheDirectory:   filepath.Join(t.TempDir(), "cache"),
		uid:              os.Geteuid(),
	}
}

// closeTestLock делает отложенное и аварийное освобождение проверяемым,
// не маскируя ошибку Close при завершении теста.
func closeTestLock(t *testing.T, lock *Lock) {
	t.Helper()
	if err := lock.Close(); err != nil {
		t.Errorf("закрыть test lock %s: %v", lock.Path(), err)
	}
}

// countLockDescriptors считает только descriptors, указывающие на конкретный
// stable lock path; фоновые descriptors Go runtime не влияют на результат.
func countLockDescriptors(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("прочитать /proc/self/fd: %v", err)
	}
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if os.IsNotExist(err) {
			// Descriptor мог закрыться между ReadDir и Readlink.
			continue
		}
		if err != nil {
			t.Fatalf("прочитать symlink descriptor %s: %v", entry.Name(), err)
		}
		if target == path {
			count++
		}
	}
	return count
}

// expectedLockPath повторяет публичный контракт имени lock-файла, а не
// использует реализацию acquireLockWithConfig для получения ожидания.
func expectedLockPath(t *testing.T, target, directory string) string {
	t.Helper()
	canonical, err := CanonicalTargetPath(target)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(canonical)))
	return filepath.Join(directory, name)
}

// assertOwnedMode проверяет точные permission bits и владельца через stat_t.
func assertOwnedMode(t *testing.T, path string, mode os.FileMode, uid int) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("неожиданный mode %s: получено %04o, ожидалось %04o", path, info.Mode().Perm(), mode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("metadata %s не содержит syscall.Stat_t", path)
	}
	if stat.Uid != uint32(uid) {
		t.Fatalf("неожиданный владелец %s: получено %d, ожидалось %d", path, stat.Uid, uid)
	}
}

// TestAcquireLockSystemExclusionAndStableFile проверяет реальный flock,
// безопасное создание отдельного lock-файла и идемпотентное освобождение.
func TestAcquireLockSystemExclusionAndStableFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fstab")
	targetData := []byte("UUID=test / ext4 defaults 0 1\n")
	if err := os.WriteFile(target, targetData, 0o600); err != nil {
		t.Fatal(err)
	}
	config := testSystemLockConfig(target, filepath.Join(dir, "locks"))
	expected := expectedLockPath(t, target, config.systemDirectory)

	first, err := acquireLockWithConfig(nil, target, 100*time.Millisecond, config) //nolint:staticcheck // Намеренный nil проверяет обязательную нормализацию context.
	if err != nil {
		t.Fatalf("захватить первый lock: %v", err)
	}
	if first.Path() != expected {
		t.Fatalf("неожиданный lock path: получено %q, ожидалось %q", first.Path(), expected)
	}
	if first.Path() == target {
		t.Fatal("lock path не должен совпадать с target")
	}
	assertOwnedMode(t, config.systemDirectory, 0o700, os.Geteuid())
	assertOwnedMode(t, expected, 0o600, os.Geteuid())
	beforeRelease, err := os.Lstat(expected)
	if err != nil {
		t.Fatal(err)
	}
	actualData, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(actualData) != string(targetData) {
		t.Fatalf("target изменился: получено %q, ожидалось %q", actualData, targetData)
	}

	second, err := acquireLockWithConfig(context.Background(), target, 30*time.Millisecond, config)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("ожидался ErrLockTimeout, получено: lock=%v err=%v", second, err)
	}
	if second != nil {
		t.Fatal("при timeout не должен возвращаться Lock")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("закрыть первый lock: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("повторный Close должен быть идемпотентным: %v", err)
	}

	third, err := acquireLockWithConfig(context.Background(), target, 100*time.Millisecond, config)
	if err != nil {
		t.Fatalf("захватить lock после Close: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
	afterRelease, err := os.Lstat(expected)
	if err != nil {
		t.Fatalf("lock-файл должен остаться после release: %v", err)
	}
	if !os.SameFile(beforeRelease, afterRelease) {
		t.Fatal("release заменил lock-файл вместо сохранения stable inode")
	}
}

// TestAcquireLockDifferentTargetsUseDifferentPaths проверяет, что имя lock
// зависит от полного canonical target, а не только от basename.
func TestAcquireLockDifferentTargetsUseDifferentPaths(t *testing.T) {
	dir := t.TempDir()
	firstTarget := filepath.Join(dir, "one", "fstab")
	secondTarget := filepath.Join(dir, "two", "fstab")
	for _, target := range []string{firstTarget, secondTarget} {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := testRuntimeLockConfig(t)
	first, err := acquireLockWithConfig(context.Background(), firstTarget, 100*time.Millisecond, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestLock(t, first)
	second, err := acquireLockWithConfig(context.Background(), secondTarget, 100*time.Millisecond, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestLock(t, second)
	if first.Path() == second.Path() {
		t.Fatalf("разные target получили один lock path %q", first.Path())
	}
}

// TestAcquireLockCanonicalizesParentSymlink проверяет общий lock для двух
// способов адресации target через real parent и parent symlink.
func TestAcquireLockCanonicalizesParentSymlink(t *testing.T) {
	dir := t.TempDir()
	realDirectory := filepath.Join(dir, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDirectory, "fstab")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(dir, "linked")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	config := testRuntimeLockConfig(t)
	direct, err := acquireLockWithConfig(context.Background(), target, 100*time.Millisecond, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := direct.Close(); err != nil {
		t.Fatal(err)
	}
	throughLink, err := acquireLockWithConfig(context.Background(), filepath.Join(linkedDirectory, "fstab"), 100*time.Millisecond, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestLock(t, throughLink)
	if direct.Path() != throughLink.Path() {
		t.Fatalf("parent symlink изменил lock path: %q и %q", direct.Path(), throughLink.Path())
	}
}

// TestAcquireLockRejectsInsecureLockDirectory проверяет отказ без chmod для
// существующего каталога с расширенными правами.
func TestAcquireLockRejectsInsecureLockDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fstab")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockDirectory := filepath.Join(dir, "locks")
	if err := os.Mkdir(lockDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	config := testSystemLockConfig(target, lockDirectory)
	lock, err := acquireLockWithConfig(context.Background(), target, 30*time.Millisecond, config)
	if err == nil {
		closeTestLock(t, lock)
		t.Fatal("ожидался отказ для lock directory 0755")
	}
	assertOwnedMode(t, lockDirectory, 0o755, os.Geteuid())
}

// TestAcquireLockRejectsInsecureLockFile проверяет exact mode 0600 без
// незаметного chmod существующего файла.
func TestAcquireLockRejectsInsecureLockFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fstab")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockDirectory := filepath.Join(dir, "locks")
	if err := os.Mkdir(lockDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	config := testSystemLockConfig(target, lockDirectory)
	path := expectedLockPath(t, target, lockDirectory)
	if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLockWithConfig(context.Background(), target, 30*time.Millisecond, config)
	if err == nil {
		closeTestLock(t, lock)
		t.Fatal("ожидался отказ для lock file 0644")
	}
	assertOwnedMode(t, path, 0o644, os.Geteuid())
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("небезопасный lock file был изменён: data=%q err=%v", data, readErr)
	}
}

// TestAcquireLockRejectsSymlinkLockFile проверяет O_NOFOLLOW и сохранение
// как самого symlink, так и его destination.
func TestAcquireLockRejectsSymlinkLockFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fstab")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockDirectory := filepath.Join(dir, "locks")
	if err := os.Mkdir(lockDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	config := testSystemLockConfig(target, lockDirectory)
	path := expectedLockPath(t, target, lockDirectory)
	destination := filepath.Join(dir, "destination")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(destination, path); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireLockWithConfig(context.Background(), target, 30*time.Millisecond, config)
	if err == nil {
		closeTestLock(t, lock)
		t.Fatal("ожидался отказ для symlink lock file")
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink lock file был удалён или заменён: info=%v err=%v", info, statErr)
	}
	data, readErr := os.ReadFile(destination)
	if readErr != nil || string(data) != "keep" {
		t.Fatalf("destination symlink был изменён: data=%q err=%v", data, readErr)
	}
}

// TestAcquireLockRejectsForeignSimulatedOwner проверяет owner check через
// immutable per-call UID без chown и изменения реальных системных файлов.
func TestAcquireLockRejectsForeignSimulatedOwner(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fstab")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := testSystemLockConfig(target, filepath.Join(dir, "locks"))
	if err := os.Mkdir(config.systemDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	config.uid = os.Geteuid() + 1
	lock, err := acquireLockWithConfig(context.Background(), target, 30*time.Millisecond, config)
	if err == nil {
		closeTestLock(t, lock)
		t.Fatal("ожидался отказ для lock directory с чужим simulated UID")
	}
	assertOwnedMode(t, config.systemDirectory, 0o700, os.Geteuid())
}

// TestAcquireLockUsesSecureRuntimeDirectory проверяет расположение
// $XDG_RUNTIME_DIR/fstab-edit и отказ вместо fallback при небезопасном base.
func TestAcquireLockUsesSecureRuntimeDirectory(t *testing.T) {
	t.Run("secure base", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "fstab")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		config := testRuntimeLockConfig(t)
		lock, err := acquireLockWithConfig(context.Background(), target, 100*time.Millisecond, config)
		if err != nil {
			t.Fatal(err)
		}
		defer closeTestLock(t, lock)
		expectedDirectory := filepath.Join(config.runtimeDirectory, "fstab-edit")
		assertOwnedMode(t, expectedDirectory, 0o700, os.Geteuid())
		if lock.Path() != expectedLockPath(t, target, expectedDirectory) {
			t.Fatalf("неожиданный runtime lock path: %q", lock.Path())
		}
	})

	t.Run("insecure base", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "fstab")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		config := testRuntimeLockConfig(t)
		if err := os.Chmod(config.runtimeDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		lock, err := acquireLockWithConfig(context.Background(), target, 30*time.Millisecond, config)
		if err == nil {
			closeTestLock(t, lock)
			t.Fatal("ожидался отказ для XDG runtime directory 0755")
		}
		assertOwnedMode(t, config.runtimeDirectory, 0o755, os.Geteuid())
		if _, statErr := os.Lstat(filepath.Join(config.runtimeDirectory, "fstab-edit")); !os.IsNotExist(statErr) {
			t.Fatalf("при небезопасном XDG base не должен создаваться fallback/child: %v", statErr)
		}
	})
}

// TestAcquireLockUsesStableCacheFallback проверяет per-user fallback в cache,
// а не в общем os.TempDir или world-writable directory.
func TestAcquireLockUsesStableCacheFallback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fstab")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := testSystemLockConfig(target, filepath.Join(dir, "unused-system"))
	config.systemTarget = filepath.Join(dir, "other-target")
	config.cacheDirectory = filepath.Join(dir, "cache")
	lock, err := acquireLockWithConfig(context.Background(), target, 100*time.Millisecond, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestLock(t, lock)
	expectedDirectory := filepath.Join(config.cacheDirectory, "fstab-edit", fmt.Sprintf("runtime-%d", config.uid))
	assertOwnedMode(t, expectedDirectory, 0o700, os.Geteuid())
	if lock.Path() != expectedLockPath(t, target, expectedDirectory) {
		t.Fatalf("неожиданный fallback lock path: %q", lock.Path())
	}
	relative, err := filepath.Rel(config.cacheDirectory, lock.Path())
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatalf("fallback должен оставаться внутри per-user cache: path=%q relative=%q err=%v", lock.Path(), relative, err)
	}
}

// TestAcquireLockContextCancellation проверяет сохранение причин context в
// errors.Is при занятом lock, без ожидания общего timeout.
func TestAcquireLockContextCancellation(t *testing.T) {
	tests := []struct {
		name        string
		makeContext func() (context.Context, context.CancelFunc)
		want        error
	}{
		{
			name: "canceled",
			makeContext: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			want: context.Canceled,
		},
		{
			name: "deadline exceeded",
			makeContext: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "fstab")
			if err := os.WriteFile(target, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			config := testSystemLockConfig(target, filepath.Join(dir, "locks"))
			held, err := acquireLockWithConfig(context.Background(), target, 100*time.Millisecond, config)
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestLock(t, held)
			ctx, cancel := test.makeContext()
			defer cancel()
			started := time.Now()
			lock, err := acquireLockWithConfig(ctx, target, time.Second, config)
			if !errors.Is(err, test.want) {
				t.Fatalf("ожидалась причина %v, получено: lock=%v err=%v", test.want, lock, err)
			}
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Fatalf("отмена context обработана слишком поздно: %v", elapsed)
			}
		})
	}
}

// TestLockTimeoutNormalization проверяет default без реального десятисекундного
// ожидания и сохранение явного положительного timeout.
func TestLockTimeoutNormalization(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Nanosecond, -time.Second} {
		if actual := normalizeLockTimeout(timeout); actual != DefaultLockTimeout {
			t.Fatalf("timeout %v: получено %v, ожидалось %v", timeout, actual, DefaultLockTimeout)
		}
	}
	if actual := normalizeLockTimeout(37 * time.Millisecond); actual != 37*time.Millisecond {
		t.Fatalf("явный timeout изменён: %v", actual)
	}
	if DefaultLockTimeout != 10*time.Second {
		t.Fatalf("неожиданный default timeout: %v", DefaultLockTimeout)
	}
}

// TestAcquireLockTimeoutDoesNotLeakDescriptors проверяет по конкретному path,
// что серия failed timeouts оставляет только descriptor владельца, а после его
// Close не остаётся ни одного descriptor; повторный acquire также обязан успеть.
func TestAcquireLockTimeoutDoesNotLeakDescriptors(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "fstab")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := testSystemLockConfig(target, filepath.Join(dir, "locks"))
	held, err := acquireLockWithConfig(context.Background(), target, 100*time.Millisecond, config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestLock(t, held)
	if count := countLockDescriptors(t, held.Path()); count != 1 {
		t.Fatalf("до timeout ожидался один descriptor владельца, получено: %d", count)
	}
	for attempt := 0; attempt < 3; attempt++ {
		lock, err := acquireLockWithConfig(context.Background(), target, 30*time.Millisecond, config)
		if !errors.Is(err, ErrLockTimeout) {
			if lock != nil {
				closeTestLock(t, lock)
			}
			t.Fatalf("попытка %d: ожидался failed timeout, получено: %v", attempt+1, err)
		}
		// Каждый failed acquire обязан закрыть собственный descriptor, не трогая held.
		if count := countLockDescriptors(t, held.Path()); count != 1 {
			t.Fatalf("попытка %d оставила descriptors: получено %d, ожидался только held", attempt+1, count)
		}
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if count := countLockDescriptors(t, held.Path()); count != 0 {
		t.Fatalf("после held.Close остались descriptors lock path: %d", count)
	}
	acquired, err := acquireLockWithConfig(context.Background(), target, 100*time.Millisecond, config)
	if err != nil {
		t.Fatalf("ложная занятость после failed timeout: %v", err)
	}
	if err := acquired.Close(); err != nil {
		t.Fatal(err)
	}
}
