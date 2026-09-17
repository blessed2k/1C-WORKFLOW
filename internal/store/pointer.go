package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

// Двухслотовый checksummed published-pointer (ADR-2 §9.2, выбран по замеру):
// один файл фиксированного размера, один fsync на публикацию, не зависит от
// os.Rename и не требует GC. Файл состоит из двух слотов
// `magic | seq | len | payload | crc32`; запись всегда идёт в НЕактивный слот с
// seq+1, чтение выбирает валидный слот с бо́льшим seq. Обрыв питания посреди
// записи портит только неактивный слот — активный остаётся целым.
//
// Rename поверх открытого SQLite не используется нигде: переименовывается
// (и то в другом варианте, отвергнутом ADR) только маленький файл указателя.

const (
	slotSize    = 512
	slotMagic   = "MCP1CPTR"
	slotHeadLen = 8 + 8 + 4 // magic + seq + payloadLen
	pointerName = "pointer.bin"
)

// ErrNoPointer — достоверной записи указателя нет. По разделу 18.1 это значит
// полный rebuild из XML, а НЕ выбор «последней validated-эпохи»: validated
// означает «готова», но не «была опубликована».
var ErrNoPointer = errors.New("нет достоверной записи published-pointer")

// ErrPointerUnreadable — файл указателя существует, но прочитать его не удалось.
// Это НЕ то же самое, что «указателя нет»: молча уйти в полный rebuild из-за
// ошибки доступа значит выбросить рабочий индекс.
var ErrPointerUnreadable = errors.New("файл указателя нечитаем")

type pointer struct{ path string }

func newPointer(dir string) *pointer { return &pointer{path: filepath.Join(dir, pointerName)} }

func encodeSlot(seq uint64, payload string) []byte {
	buf := make([]byte, slotSize)
	copy(buf, slotMagic)
	binary.LittleEndian.PutUint64(buf[8:], seq)
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(payload)))
	copy(buf[slotHeadLen:], payload)
	sum := crc32.ChecksumIEEE(buf[:slotHeadLen+len(payload)])
	binary.LittleEndian.PutUint32(buf[slotSize-4:], sum)
	return buf
}

func decodeSlot(buf []byte) (uint64, string, bool) {
	if len(buf) < slotSize || string(buf[:8]) != slotMagic {
		return 0, "", false
	}
	seq := binary.LittleEndian.Uint64(buf[8:])
	n := int(binary.LittleEndian.Uint32(buf[16:]))
	if n < 0 || slotHeadLen+n > slotSize-4 {
		return 0, "", false
	}
	want := binary.LittleEndian.Uint32(buf[slotSize-4:])
	if crc32.ChecksumIEEE(buf[:slotHeadLen+n]) != want {
		return 0, "", false
	}
	return seq, string(buf[slotHeadLen : slotHeadLen+n]), true
}

type slotState struct {
	seq   uint64
	pay   string
	valid bool
}

func (p *pointer) readSlots() (slots [2]slotState, readErr error) {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if !os.IsNotExist(err) {
			readErr = fmt.Errorf("%w: %v", ErrPointerUnreadable, err)
		}
		return
	}
	for i := 0; i < 2; i++ {
		lo, hi := i*slotSize, (i+1)*slotSize
		if hi > len(data) {
			continue
		}
		seq, pay, ok := decodeSlot(data[lo:hi])
		slots[i] = slotState{seq: seq, pay: pay, valid: ok}
	}
	return
}

// best возвращает индекс валидного слота с наибольшим seq или -1.
func best(slots [2]slotState) int {
	idx := -1
	for i := 0; i < 2; i++ {
		if slots[i].valid && (idx < 0 || slots[i].seq > slots[idx].seq) {
			idx = i
		}
	}
	return idx
}

// Read отдаёт последнюю достоверную публикацию.
func (p *pointer) Read() (string, uint64, error) {
	s, err := p.readSlots()
	if err != nil {
		return "", 0, err
	}
	i := best(s)
	if i < 0 {
		return "", 0, ErrNoPointer
	}
	return s[i].pay, s[i].seq, nil
}

// Publish пишет в неактивный слот и делает fsync: активный слот остаётся целым
// на всё время записи, поэтому обрыв питания не может потерять указатель.
func (p *pointer) Publish(payload string) (uint64, error) {
	if len(payload) > slotSize-slotHeadLen-4 {
		return 0, fmt.Errorf("payload не помещается в слот: %d байт", len(payload))
	}
	s, err := p.readSlots()
	if err != nil {
		return 0, err
	}
	active := best(s)
	var seq uint64
	if active >= 0 {
		seq = s[active].seq
	}
	target := 0
	if active == 0 {
		target = 1
	}
	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < 2*slotSize {
		if err := f.Truncate(2 * slotSize); err != nil {
			return 0, err
		}
	}
	next := seq + 1
	if _, err := f.WriteAt(encodeSlot(next, payload), int64(target*slotSize)); err != nil {
		return 0, err
	}
	if err := f.Sync(); err != nil {
		return 0, err
	}
	return next, nil
}
