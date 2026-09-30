package lexicon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func entry(wrong, correct string, scope domain.LexiconScope) domain.LexiconCorrection {
	return domain.LexiconCorrection{ID: uuid.New(), Wrong: wrong, Correct: correct, Scope: scope}
}

func TestMatcher_CyrillicWholeWord(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("улаанбатар", "Улаанбаатар", domain.ScopeSTT)})

	out, hits := m.Apply("Би улаанбатар хотод амьдардаг")
	assert.Equal(t, "Би Улаанбаатар хотод амьдардаг", out)
	require.Len(t, hits, 1)
	assert.Equal(t, "улаанбатар", hits[0].Wrong)
	assert.Equal(t, "Улаанбаатар", hits[0].Correct)
	assert.Equal(t, 3, hits[0].Pos)
}

func TestMatcher_Boundaries(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("Улаанбаатар", "УБ", domain.ScopeBoth)})

	for _, in := range []string{"Улаанбаатарын", "аУлаанбаатар", "Улаанбаатар1", "Улаанбаатарөө"} {
		out, hits := m.Apply(in)
		assert.Equal(t, in, out, in)
		assert.Empty(t, hits, in)
	}
	out, hits := m.Apply("Улаанбаатар")
	assert.Equal(t, "УБ", out)
	assert.Len(t, hits, 1)

	// An entry for the inflected form itself does match.
	m2 := Compile([]domain.LexiconCorrection{entry("Улаанбаатарын", "УБ-ын", domain.ScopeSTT)})
	out, _ = m2.Apply("Улаанбаатарын төв")
	assert.Equal(t, "УБ-ын төв", out)
}

func TestMatcher_PunctuationAdjacency(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("нэг", "1", domain.ScopeSTT)})
	out, hits := m.Apply("нэг, хоёр. (нэг) «нэг» нэг!")
	assert.Equal(t, "1, хоёр. (1) «1» 1!", out)
	assert.Len(t, hits, 4)
	out, _ = m.Apply("нэгэн нэгдүгээр")
	assert.Equal(t, "нэгэн нэгдүгээр", out)
}

func TestMatcher_MongolianSpecialLetters(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("өөр", "өөр", domain.ScopeSTT), entry("үүр", "үүр цайх", domain.ScopeSTT)})
	out, hits := m.Apply("ҮҮР болоход Өөр нэг")
	assert.Equal(t, "ҮҮР ЦАЙХ болоход Өөр нэг", out)
	assert.Len(t, hits, 2)
}

func TestMatcher_Phrases_LongestFirst(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{
		entry("хаан", "Хаан", domain.ScopeSTT),
		entry("хаан банк", "Хаан Банк", domain.ScopeSTT),
		entry("хаан банк ххк", "Хаан Банк ХХК", domain.ScopeSTT),
	})
	out, hits := m.Apply("хаан банк ххк болон хаан   банк, хаан")
	assert.Equal(t, "Хаан Банк ХХК болон Хаан Банк, Хаан", out)
	require.Len(t, hits, 3)
	assert.Equal(t, 0, hits[0].Pos)
	assert.Equal(t, "хаан   банк", hits[1].Wrong)
}

func TestMatcher_PhraseDoesNotSpanPunctuation(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("хаан банк", "Хаан Банк", domain.ScopeSTT)})
	out, hits := m.Apply("хаан, банк")
	assert.Equal(t, "хаан, банк", out)
	assert.Empty(t, hits)
}

func TestMatcher_CasePreservation(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{
		entry("гэрэл", "гэрэлтүүлэг", domain.ScopeSTT),
		entry("wifi", "Wi-Fi", domain.ScopeSTT),
	})
	tests := map[string]string{
		"гэрэл":      "гэрэлтүүлэг",
		"Гэрэл":      "Гэрэлтүүлэг",
		"ГЭРЭЛ":      "ГЭРЭЛТҮҮЛЭГ",
		"гЭрЭл":      "гэрэлтүүлэг",
		"Гэрэл асав": "Гэрэлтүүлэг асав",
		"WIFI":       "WI-FI",
		"Wifi":       "Wi-Fi",
		"wifi":       "Wi-Fi",
	}
	for in, want := range tests {
		out, _ := m.Apply(in)
		assert.Equal(t, want, out, in)
	}
}

func TestMatcher_ExactCaseIsVerbatim(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("SMS", "мессеж", domain.ScopeSTT)})
	out, _ := m.Apply("SMS ирлээ, sms ирлээ, Sms")
	assert.Equal(t, "мессеж ирлээ, мессеж ирлээ, Мессеж", out)
}

func TestMatcher_SingleUpperLetter(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("а", "аа", domain.ScopeSTT)})
	out, _ := m.Apply("А тэгэхээр а")
	assert.Equal(t, "Аа тэгэхээр аа", out)
}

func TestMatcher_ScopeFiltering(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{
		entry("a", "1", domain.ScopeSTT),
		entry("b", "2", domain.ScopeTTS),
		entry("c", "3", domain.ScopeBoth),
	})
	assert.Equal(t, 2, m.Len())
	out, _ := m.Apply("a b c")
	assert.Equal(t, "1 b 3", out)
}

func TestMatcher_TTSHints(t *testing.T) {
	stt := entry("x", "y", domain.ScopeSTT)
	stt.Phonetic = "ignored"
	tts := entry("Тэнгэр", "Тэнгэр", domain.ScopeTTS)
	tts.Phonetic = "тэнгэр"
	both := entry("ГЭР", "гэр", domain.ScopeBoth)
	both.Phonetic = "гэр"
	noPhon := entry("z", "zz", domain.ScopeBoth)
	m := Compile([]domain.LexiconCorrection{stt, tts, both, noPhon})

	hints := m.TTSHints()
	require.Len(t, hints, 2)
	assert.Equal(t, tts.ID, hints[0].ID)
	assert.Equal(t, both.ID, hints[1].ID)
	// tts-only entries are not applied to STT text.
	out, _ := m.Apply("Тэнгэр")
	assert.Equal(t, "Тэнгэр", out)
}

func TestMatcher_EmptyAndNil(t *testing.T) {
	var nilM *Matcher
	out, hits := nilM.Apply("сайн уу")
	assert.Equal(t, "сайн уу", out)
	assert.Nil(t, hits)
	assert.Nil(t, nilM.TTSHints())
	assert.Equal(t, 0, nilM.Len())

	m := Compile([]domain.LexiconCorrection{entry("   ", "x", domain.ScopeSTT), entry("a", "b", domain.ScopeSTT)})
	assert.Equal(t, 1, m.Len())
	out, hits = m.Apply("")
	assert.Equal(t, "", out)
	assert.Nil(t, hits)
}

func TestMatcher_NonWordLeadingPattern(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("#1", "нэгдүгээр", domain.ScopeSTT)})
	out, _ := m.Apply("сонголт #1 болон #12")
	// "#1" has no trailing boundary requirement issue: last rune is a digit,
	// so "#12" must not match.
	assert.Equal(t, "сонголт нэгдүгээр болон #12", out)
}

func TestMatcher_HyphenatedAndLatin(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("wi-fi", "вайфай", domain.ScopeSTT)})
	out, _ := m.Apply("Wi-Fi, WI-FI, wifi, wi-fix")
	assert.Equal(t, "Вайфай, ВАЙФАЙ, wifi, wi-fix", out)
}

func TestMatcher_AdjacentMatches(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("аа", "б", domain.ScopeSTT)})
	out, hits := m.Apply("аа аа,аа")
	assert.Equal(t, "б б,б", out)
	assert.Len(t, hits, 3)
}

func TestMatcher_ConcurrentApply(t *testing.T) {
	m := Compile([]domain.LexiconCorrection{entry("нэг", "1", domain.ScopeSTT)})
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				out, _ := m.Apply("нэг хоёр нэг")
				if out != "1 хоёр 1" {
					t.Errorf("unexpected %q", out)
					return
				}
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func benchEntries(n int) []domain.LexiconCorrection {
	es := make([]domain.LexiconCorrection, 0, n)
	for i := 0; i < n; i++ {
		wrong := fmt.Sprintf("буруу%d", i)
		if i%5 == 0 {
			wrong = fmt.Sprintf("сайн%d байна%d", i%37, i)
		}
		es = append(es, entry(wrong, fmt.Sprintf("зөв%d", i), domain.ScopeBoth))
	}
	return es
}

func BenchmarkApply_200Words_500Entries(b *testing.B) {
	m := Compile(benchEntries(500))
	words := make([]string, 200)
	for i := range words {
		switch i % 10 {
		case 0:
			words[i] = fmt.Sprintf("буруу%d,", i+1)
		case 5:
			words[i] = "Улаанбаатар"
		default:
			words[i] = "монгол"
		}
	}
	text := strings.Join(words, " ")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Apply(text)
	}
}
