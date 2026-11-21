package tui

import (
	"bytes"
	"testing"

	"fstab-edit/internal/editor"
	"fstab-edit/internal/parser"
	"fstab-edit/pkg/models"
)

// testDocument парсит fstab-контент в документ для юнит-тестов сессии.
func testDocument(t *testing.T, content string) *models.Document {
	t.Helper()
	return parser.Parse([]byte(content))
}

// TestApplyOpSetsDirtyAndUndoable проверяет: успешная мутация делает сессию
// dirty, кладёт в undo-стек снапшот ДО мутации и реально меняет документ.
func TestApplyOpSetsDirtyAndUndoable(t *testing.T) {
	doc := testDocument(t, "UUID=a / ext4 defaults 0 1\n")
	s := &sessionState{original: doc.Serialize()}
	if s.dirty {
		t.Fatal("новая сессия не должна быть dirty")
	}
	err := s.applyOp(doc, func(d *models.Document) error {
		_, err := editor.Add(d, editor.NewEntry{Source: "UUID=b", MountPoint: "/data", FSType: "ext4", Options: "rw"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !s.dirty {
		t.Fatal("после мутации dirty обязан быть true")
	}
	if !bytes.Equal(s.undoStack[0], s.original) {
		t.Fatal("undo-стек должен хранить снапшот ДО мутации")
	}
	if len(doc.Nodes) != 2 {
		t.Fatalf("запись не добавлена: %d узлов", len(doc.Nodes))
	}
}

// TestApplyOpErrorLeavesStateUntouched: ошибка операции (здесь — недопустимый
// селектор) не пишет в undo-стек, не делает dirty и не меняет документ.
func TestApplyOpErrorLeavesStateUntouched(t *testing.T) {
	doc := testDocument(t, "UUID=a / ext4 defaults 0 1\n")
	s := &sessionState{original: doc.Serialize()}
	before := doc.Serialize()
	err := s.applyOp(doc, func(d *models.Document) error {
		_, err := editor.Edit(d, editor.ByLine(99), editor.Changes{})
		return err
	})
	if err == nil {
		t.Fatal("ожидалась ошибка селектора")
	}
	if s.dirty || len(s.undoStack) != 0 {
		t.Fatal("ошибка не должна менять состояние")
	}
	if !bytes.Equal(before, doc.Serialize()) {
		t.Fatal("документ изменился при ошибке")
	}
}

// TestApplyOpNoopNotInStack: операция, вернувшая nil, но не изменившая
// сериализацию, не должна попадать в undo-историю.
func TestApplyOpNoopNotInStack(t *testing.T) {
	doc := testDocument(t, "UUID=a / ext4 defaults 0 1\n")
	s := &sessionState{original: doc.Serialize()}
	err := s.applyOp(doc, func(d *models.Document) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if s.dirty {
		t.Fatal("no-op не должен делать сессию dirty")
	}
	if len(s.undoStack) != 0 {
		t.Fatal("no-op не должен попадать в undo-стек")
	}
}

// TestApplyOpNoopKeepsDirty — регрессия: dirty сравнивается с original сессии,
// а не с дельтой одной операции. No-op при уже изменённом документе не должен
// сбрасывать dirty; undo откатывает реальную мутацию и dirty=false.
func TestApplyOpNoopKeepsDirty(t *testing.T) {
	doc := testDocument(t, "UUID=a / ext4 defaults 0 1\n")
	s := &sessionState{original: doc.Serialize()}
	add := func() {
		t.Helper()
		if err := s.applyOp(doc, func(d *models.Document) error {
			_, err := editor.Add(d, editor.NewEntry{Source: "UUID=b", MountPoint: "/data", FSType: "ext4", Options: "rw"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	add()
	if !s.dirty {
		t.Fatal("после мутации dirty обязан быть true")
	}
	// Успешная no-op операция: nil-ошибка, документ не изменён.
	if err := s.applyOp(doc, func(d *models.Document) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !s.dirty {
		t.Fatal("no-op не должна сбрасывать dirty при живых несохранённых изменениях")
	}
	if len(s.undoStack) != 1 {
		t.Fatalf("no-op не должна попадать в undo-стек, в стеке %d снапшотов", len(s.undoStack))
	}
	if !s.undo(doc) {
		t.Fatal("undo должен вернуть true")
	}
	if s.dirty {
		t.Fatal("после undo реальной мутации dirty=false")
	}
	if !bytes.Equal(doc.Serialize(), s.original) {
		t.Fatal("документ не восстановлен")
	}
}

// TestUndoRestoresSnapshot: undo восстанавливает документ, пересчитывает
// dirty и возвращает false при пустом стеке.
func TestUndoRestoresSnapshot(t *testing.T) {
	doc := testDocument(t, "UUID=a / ext4 defaults 0 1\n")
	s := &sessionState{original: doc.Serialize()}
	_ = s.applyOp(doc, func(d *models.Document) error {
		_, err := editor.Add(d, editor.NewEntry{Source: "UUID=b", MountPoint: "/data", FSType: "ext4", Options: "rw"})
		return err
	})
	if !s.undo(doc) {
		t.Fatal("undo должен вернуть true")
	}
	if s.dirty {
		t.Fatal("после полного undo dirty=false")
	}
	if !bytes.Equal(doc.Serialize(), s.original) {
		t.Fatal("документ не восстановлен")
	}
	if s.undo(doc) {
		t.Fatal("второй undo при пустом стеке — false")
	}
}

// TestTwoMutationsUndoTwice: две мутации → два undo → документ равен original
// и dirty=false; третий undo — false.
func TestTwoMutationsUndoTwice(t *testing.T) {
	doc := testDocument(t, "UUID=a / ext4 defaults 0 1\n")
	s := &sessionState{original: doc.Serialize()}
	add := func(source, mount string) {
		t.Helper()
		if err := s.applyOp(doc, func(d *models.Document) error {
			_, err := editor.Add(d, editor.NewEntry{Source: source, MountPoint: mount, FSType: "ext4", Options: "rw"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("UUID=b", "/data")
	add("UUID=c", "/backup")
	if len(doc.Nodes) != 3 {
		t.Fatalf("ожидалось 3 узла после двух мутаций, получено %d", len(doc.Nodes))
	}
	if !s.undo(doc) {
		t.Fatal("первый undo должен вернуть true")
	}
	if !s.dirty {
		t.Fatal("после одного undo из двух мутаций dirty=true")
	}
	if !s.undo(doc) {
		t.Fatal("второй undo должен вернуть true")
	}
	if s.dirty {
		t.Fatal("после двух undo dirty=false")
	}
	if !bytes.Equal(doc.Serialize(), s.original) {
		t.Fatal("документ не восстановлен до original")
	}
	if s.undo(doc) {
		t.Fatal("третий undo при пустом стеке — false")
	}
}

// TestCandidateMatchesSerialize: candidate — это сериализация текущего
// документа (будущий байтовый кандидат для transaction).
func TestCandidateMatchesSerialize(t *testing.T) {
	doc := testDocument(t, "UUID=a / ext4 defaults 0 1\n")
	s := &sessionState{original: doc.Serialize()}
	if !bytes.Equal(s.candidate(doc), doc.Serialize()) {
		t.Fatal("candidate != Serialize")
	}
}
