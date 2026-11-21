# testdata/blockdev — синтетические наблюдённые выводы lsblk и blkid

Файлы созданы вручную по форматам util-linux и не содержат данных реальных
систем: все UUID, метки и серийные номера вымышленные.

- `lsblk-tree.json` — вывод `lsblk --json --output NAME,PATH,TYPE,FSTYPE,SIZE,MOUNTPOINT,LABEL,UUID,MODEL`: диск с двумя разделами, LVM-children у раздела, null-поля mountpoint/label/uuid, rom и loop устройства.
- `blkid-export-sda1.stdout` — вывод `blkid -o export /dev/sda1` с UUID/LABEL/TYPE/PARTUUID.
