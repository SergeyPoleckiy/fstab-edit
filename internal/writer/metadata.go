package writer

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// xattrMaxRetries ограничивает повторные size/read попытки при гонке размера
// extended attribute. Первоначальная попытка не входит в этот лимит.
const xattrMaxRetries = 4

// copyMetadata переносит владельца, mode и все перечисленные xattrs только
// через descriptors, поэтому path substitution не меняет источник или назначение.
func copyMetadata(sourceFD, tempFD int, stat *unix.Stat_t, operations replaceOperations) error {
	// Chown предшествует xattrs: смена владельца может очистить security.capability,
	// а последующий Fsetxattr восстановит значение, прочитанное из source descriptor.
	if err := operations.fchown(tempFD, int(stat.Uid), int(stat.Gid)); err != nil {
		return fmt.Errorf("скопировать владельца target: %w", err)
	}
	if err := operations.fchmod(tempFD, stat.Mode&0o7777); err != nil {
		return fmt.Errorf("скопировать mode target: %w", err)
	}

	names, err := listXattrs(sourceFD, operations)
	if err != nil {
		return fmt.Errorf("перечислить xattrs target: %w", err)
	}
	for _, name := range names {
		value, err := readXattr(sourceFD, name, operations)
		if err != nil {
			return fmt.Errorf("прочитать xattr %q: %w", name, err)
		}
		// Пустой value передаётся без специальных случаев: zero-length xattr
		// отличается от отсутствия атрибута и должен сохраниться.
		if err := operations.fsetxattr(tempFD, name, value, 0); err != nil {
			return fmt.Errorf("установить xattr %q: %w", name, err)
		}
	}
	return nil
}

// listXattrs выполняет bounded retries, если содержимое source изменилось между
// определением размера list и чтением. Неподдерживаемый list означает отсутствие
// доступных xattrs; ошибки конкретного атрибута здесь не подавляются.
func listXattrs(sourceFD int, operations replaceOperations) ([]string, error) {
	retries := 0
	for {
		size, err := operations.flistxattr(sourceFD, nil)
		if err != nil {
			if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
				return nil, nil
			}
			if errors.Is(err, unix.ERANGE) && retries < xattrMaxRetries {
				retries++
				continue
			}
			return nil, err
		}
		if size < 0 {
			return nil, fmt.Errorf("отрицательный размер list xattrs: %d", size)
		}

		buffer := make([]byte, size)
		read, err := operations.flistxattr(sourceFD, buffer)
		if err != nil {
			// После непустого probe отказ read означает потерю подтверждённых
			// xattrs и обязан запретить rename; пустой list остаётся допустимым.
			if size == 0 && (errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)) {
				return nil, nil
			}
			if errors.Is(err, unix.ERANGE) && retries < xattrMaxRetries {
				retries++
				continue
			}
			return nil, err
		}
		if read < 0 || read > len(buffer) {
			return nil, fmt.Errorf("некорректный размер list xattrs: %d при buffer %d", read, len(buffer))
		}
		return splitXattrNames(buffer[:read]), nil
	}
}

// splitXattrNames разбирает NUL-separated list и отбрасывает пустые элементы,
// включая завершающий NUL, чтобы не вызывать Fgetxattr с пустым именем.
func splitXattrNames(data []byte) []string {
	names := make([]string, 0)
	for len(data) > 0 {
		end := 0
		for end < len(data) && data[end] != 0 {
			end++
		}
		if end > 0 {
			names = append(names, string(data[:end]))
		}
		if end == len(data) {
			break
		}
		data = data[end+1:]
	}
	return names
}

// readXattr повторно определяет размер после ERANGE и сохраняет точную длину,
// возвращённую реальным чтением, включая корректный zero-length value.
func readXattr(sourceFD int, name string, operations replaceOperations) ([]byte, error) {
	retries := 0
	for {
		size, err := operations.fgetxattr(sourceFD, name, nil)
		if err != nil {
			if errors.Is(err, unix.ERANGE) && retries < xattrMaxRetries {
				retries++
				continue
			}
			return nil, err
		}
		if size < 0 {
			return nil, fmt.Errorf("отрицательный размер xattr: %d", size)
		}

		buffer := make([]byte, size)
		read, err := operations.fgetxattr(sourceFD, name, buffer)
		if err != nil {
			if errors.Is(err, unix.ERANGE) && retries < xattrMaxRetries {
				retries++
				continue
			}
			return nil, err
		}
		if read < 0 || read > len(buffer) {
			return nil, fmt.Errorf("некорректный размер xattr: %d при buffer %d", read, len(buffer))
		}
		return buffer[:read], nil
	}
}
