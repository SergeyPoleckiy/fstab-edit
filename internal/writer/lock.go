package writer

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// DefaultLockTimeout ограничивает ожидание блокировки при нулевом или
	// отрицательном явном timeout.
	DefaultLockTimeout = 10 * time.Second
	// lockRetryInterval ограничивает задержку повторной попытки flock и не
	// заменяет отдельное наблюдение за отменой context.
	lockRetryInterval = 20 * time.Millisecond
)

var (
	// ErrLockTimeout обозначает истечение явного или стандартного timeout
	// ожидания эксклюзивного flock.
	ErrLockTimeout = errors.New("истёк timeout ожидания lock")
)

// lockConfig содержит все системные пути и UID одного вызова. Благодаря
// передаче по значению тесты не изменяют process environment или global state.
type lockConfig struct {
	systemTarget     string
	systemDirectory  string
	runtimeDirectory string
	cacheDirectory   string
	uid              int
}

// Lock владеет открытым descriptor отдельного стабильного lock-файла.
// Поле closed предотвращает повторные операции с potentially reused descriptor.
type Lock struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	closed bool
}

// Path возвращает неизменный путь lock-файла, сохранённый при захвате.
func (lock *Lock) Path() string {
	return lock.path
}

// Close освобождает flock и всегда закрывает descriptor при первом вызове.
// Повторный вызов ничего не делает, даже если первый вернул ошибку.
func (lock *Lock) Close() error {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.closed {
		return nil
	}
	lock.closed = true

	var unlockErr error
	if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN); err != nil {
		unlockErr = fmt.Errorf("освободить flock %s: %w", lock.path, err)
	}
	var closeErr error
	if err := lock.file.Close(); err != nil {
		closeErr = fmt.Errorf("закрыть lock file %s: %w", lock.path, err)
	}
	return errors.Join(unlockErr, closeErr)
}

// normalizeLockTimeout выделяет нормализацию, чтобы default проверялся без
// реального десятисекундного ожидания занятого lock.
func normalizeLockTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return DefaultLockTimeout
	}
	return timeout
}

// AcquireLock канонизирует target и захватывает отдельный эксклюзивный flock.
// Системный target доступен только root; остальные target используют XDG/cache.
func AcquireLock(ctx context.Context, targetPath string, timeout time.Duration) (*Lock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout = normalizeLockTimeout(timeout)
	canonical, err := CanonicalTargetPath(targetPath)
	if err != nil {
		return nil, err
	}
	const systemTarget = "/etc/fstab"
	const systemDirectory = "/run/lock/fstab-edit"
	uid := os.Geteuid()
	if canonical == systemTarget && uid != 0 {
		return nil, fmt.Errorf("захват system lock %s требует root (UID 0): %w", systemDirectory, os.ErrPermission)
	}
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("определить user cache directory для lock: %w", err)
	}
	config := lockConfig{
		systemTarget:     systemTarget,
		systemDirectory:  systemDirectory,
		runtimeDirectory: os.Getenv("XDG_RUNTIME_DIR"),
		cacheDirectory:   cacheDirectory,
		uid:              uid,
	}
	return acquireCanonicalLockWithConfig(ctx, canonical, timeout, config)
}

// acquireLockWithConfig является неизменяемым per-call seam для temp paths.
// В production system root проверяется публичным AcquireLock; config.uid здесь
// задаёт ожидаемого владельца и позволяет тестировать чужой UID без chown.
func acquireLockWithConfig(ctx context.Context, targetPath string, timeout time.Duration, config lockConfig) (*Lock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout = normalizeLockTimeout(timeout)
	canonical, err := CanonicalTargetPath(targetPath)
	if err != nil {
		return nil, err
	}
	return acquireCanonicalLockWithConfig(ctx, canonical, timeout, config)
}

// acquireCanonicalLockWithConfig открывает stable inode до ожидания flock,
// поэтому конкурентные вызовы никогда не блокируют unlink/recreate разных файлов.
func acquireCanonicalLockWithConfig(ctx context.Context, canonical string, timeout time.Duration, config lockConfig) (*Lock, error) {
	directory, err := prepareLockDirectory(canonical, config)
	if err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(canonical)))
	path := filepath.Join(directory, name)
	file, err := openSecureLockFile(path, config.uid)
	if err != nil {
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(lockRetryInterval)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{file: file, path: path}, nil
		}
		if err == unix.EINTR {
			continue
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return nil, closeLockAfterError(file, path, fmt.Errorf("захватить flock %s: %w", path, err))
		}

		select {
		case <-ctx.Done():
			return nil, closeLockAfterError(file, path, fmt.Errorf("ожидание lock %s: %w", path, ctx.Err()))
		case <-timer.C:
			return nil, closeLockAfterError(file, path, fmt.Errorf("ожидание lock %s: %w", path, ErrLockTimeout))
		case <-ticker.C:
		}
	}
}

// prepareLockDirectory выбирает расположение по классу target и проверяет
// точные владельца, тип и mode, не исправляя небезопасные существующие пути.
func prepareLockDirectory(canonical string, config lockConfig) (string, error) {
	if canonical == config.systemTarget {
		if err := makeSecureDirectory(config.systemDirectory, config.uid); err != nil {
			return "", fmt.Errorf("подготовить system lock directory: %w", err)
		}
		return config.systemDirectory, nil
	}
	if config.runtimeDirectory != "" {
		if err := checkSecureDirectory(config.runtimeDirectory, config.uid); err != nil {
			return "", fmt.Errorf("проверить XDG runtime directory: %w", err)
		}
		directory := filepath.Join(config.runtimeDirectory, "fstab-edit")
		if err := makeSecureDirectory(directory, config.uid); err != nil {
			return "", fmt.Errorf("подготовить XDG lock directory: %w", err)
		}
		return directory, nil
	}
	directory := filepath.Join(config.cacheDirectory, "fstab-edit", fmt.Sprintf("runtime-%d", config.uid))
	if err := makeSecureDirectory(directory, config.uid); err != nil {
		return "", fmt.Errorf("подготовить cache lock directory: %w", err)
	}
	return directory, nil
}

// makeSecureDirectory создаёт отсутствующие компоненты с 0700, а затем
// отдельно проверяет final path через Lstat, чтобы отклонить symlink и chmod 0755.
func makeSecureDirectory(path string, uid int) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("создать lock directory %s: %w", path, err)
	}
	return checkSecureDirectory(path, uid)
}

// checkSecureDirectory использует metadata самого path, а не destination symlink.
func checkSecureDirectory(path string, uid int) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return fmt.Errorf("прочитать metadata lock directory %s: %w", path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("lock path %s не является real directory", path)
	}
	if stat.Uid != uint32(uid) {
		return fmt.Errorf("lock directory %s принадлежит UID %d вместо %d", path, stat.Uid, uid)
	}
	if stat.Mode&0o7777 != 0o700 {
		return fmt.Errorf("lock directory %s имеет mode %04o вместо 0700", path, stat.Mode&0o7777)
	}
	return nil
}

// openSecureLockFile не следует финальному symlink и проверяет уже открытый
// descriptor, исключая смену metadata между предварительной проверкой и open.
func openSecureLockFile(path string, uid int) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("открыть lock file %s: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, closeLockAfterError(file, path, fmt.Errorf("прочитать metadata lock file %s: %w", path, err))
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, closeLockAfterError(file, path, fmt.Errorf("lock file %s не является regular file", path))
	}
	if stat.Uid != uint32(uid) {
		return nil, closeLockAfterError(file, path, fmt.Errorf("lock file %s принадлежит UID %d вместо %d", path, stat.Uid, uid))
	}
	if stat.Mode&0o7777 != 0o600 {
		return nil, closeLockAfterError(file, path, fmt.Errorf("lock file %s имеет mode %04o вместо 0600", path, stat.Mode&0o7777))
	}
	return file, nil
}

// closeLockAfterError сохраняет первичную причину и присоединяет ошибку Close.
func closeLockAfterError(file *os.File, path string, cause error) error {
	if err := file.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("закрыть lock file %s: %w", path, err))
	}
	return cause
}
