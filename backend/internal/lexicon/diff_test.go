package lexicon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDiffWord(t *testing.T) {
	tests := []struct {
		name         string
		orig, edited string
		wrong, right string
		ok           bool
	}{
		{"single word", "Би улаанбатар хотод амьдардаг", "Би Улаанбаатар хотод амьдардаг", "улаанбатар", "Улаанбаатар", true},
		{"first word", "тэнгэр сайхан", "Тэнгэр сайхан", "тэнгэр", "Тэнгэр", true},
		{"last word", "миний нэр бат", "миний нэр Бат-Эрдэнэ", "бат", "Бат-Эрдэнэ", true},
		{"phrase replaced", "хаан бан ххк дуудлаа", "Хаан Банк дуудлаа", "хаан бан ххк", "Хаан Банк", true},
		{"one to many", "гэрэл асав", "гэрэлтүүлэг асав", "гэрэл", "гэрэлтүүлэг", true},
		{"punctuation stripped", "нэг, хоёр", "нэгэн, хоёр", "нэг", "нэгэн", true},
		{"identical", "сайн уу", "сайн уу", "", "", false},
		{"whitespace only", "сайн  уу", "сайн уу", "", "", false},
		{"punctuation only", "сайн уу.", "сайн уу", "", "", false},
		{"two separate regions", "a b c d e", "a x c y e", "", "", false},
		{"swap", "a b", "b a", "", "", false},
		{"insertion widened left", "би хотод амьдардаг", "би Улаанбаатар хотод амьдардаг", "би", "би Улаанбаатар", true},
		{"insertion at start widened right", "хотод амьдардаг", "Улаанбаатар хотод амьдардаг", "хотод", "Улаанбаатар хотод", true},
		{"deletion widened left", "би ээ хотод", "би хотод", "би ээ", "би", true},
		{"deletion at start widened right", "ээ хотод", "хотод", "ээ хотод", "хотод", true},
		{"all removed", "сайн", "", "", "", false},
		{"all new", "", "сайн", "", "", false},
		{"both empty", "", "", "", "", false},
		{"totally different", "сайн уу", "баяртай", "сайн уу", "баяртай", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, c, ok := DiffWord(tt.orig, tt.edited)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.wrong, w)
			assert.Equal(t, tt.right, c)
		})
	}
}

func TestDiffWord_HugeInputBounded(t *testing.T) {
	a := make([]byte, 0, 20000)
	b := make([]byte, 0, 20000)
	for i := 0; i < 5000; i++ {
		a = append(a, "x "...)
		b = append(b, "y "...)
	}
	// 5000x5000 tokens exceeds maxDiffCells: the guard rejects it cheaply.
	_, _, ok := DiffWord(string(a), string(b))
	assert.False(t, ok)
}
