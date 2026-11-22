# testdata/blockdev — синтетические наблюдённые выводы lsblk и blkid

Файлы созданы вручную по форматам util-linux и не содержат данных реальных
систем: все UUID, метки и серийные номера вымышленные.

- `lsblk-tree.json` — вывод `lsblk --json --output NAME,PATH,TYPE,FSTYPE,SIZE,MOUNTPOINT,LABEL,UUID,PARTUUID,PARTLABEL,MODEL`: диск с двумя разделами (у EFI-раздела заполнены PARTUUID/PARTLABEL), LVM-children у раздела, null-поля mountpoint/label/uuid/partuuid/partlabel, rom и loop устройства, диск со всеми четырьмя идентификаторами (nvme0n1), раздел только с PARTUUID (nvme1n1) и раздел только с PARTLABEL (nvme2n1).
- `blkid-export-sda1.stdout` — вывод `blkid -o export /dev/sda1` с UUID/LABEL/TYPE/PARTUUID.
