package luhn

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIsValid_TableDriven — table-driven тест, покрывающий основные
// сценарии проверки алгоритма Луна: валидные номера, невалидные,
// пустая строка, нецифровые символы.
func TestIsValid_TableDriven(t *testing.T) {
	tests := []struct {
		name   string
		number string
		want   bool
	}{
		// Валидные номера (контрольная сумма делится на 10).
		{"single digit zero", "0", true},
		{"single digit 9", "9", false},
		{"valid 79927398713", "79927398713", true},
		{"valid 4532015112830366", "4532015112830366", true},
		{"valid 4242424242424242", "4242424242424242", true},
		{"valid 1234567812345670", "1234567812345670", true},
		{"valid 18-digit", "6011111111111117", true},

		// Невалидные номера (контрольная сумма не делится на 10).
		{"invalid checksum 79927398710", "79927398710", false},
		{"invalid 4532015112830367", "4532015112830367", false},
		{"invalid 4242424242424243", "4242424242424243", false},

		// Граничные случаи.
		{"empty string", "", false},
		{"single digit 1", "1", false},
		{"single digit 5", "5", false},

		// Нецифровые символы.
		{"contains letter", "1234a5678", false},
		{"contains space", "1234 5678", false},
		{"contains dash", "1234-5678", false},
		{"all letters", "abcdef", false},
		{"mixed alphanumeric", "1234x567", false},

		// Специальные символы.
		{"contains newline", "1234\n5678", false},
		{"contains unicode digit-like", "123４5678", false},
		{"contains plus", "1234+5678", false},
		{"contains dot", "1234.5678", false},
		{"contains minus", "1234-5678", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsValid(tt.number))
		})
	}
}

// TestIsValid_ShortNumbers проверяет несколько коротких чисел
// с известными результатами для уверенности в граничных случаях.
func TestIsValid_ShortNumbers(t *testing.T) {
	assert.False(t, IsValid("1")) // sum=1, 1%10 != 0
	assert.True(t, IsValid("0"))  // sum=0, 0%10 == 0
	assert.False(t, IsValid("9")) // sum=9, 9%10 != 0
	// "18": справа 8 (normal), 1 (alt: 1*2=2), sum=8+2=10, 10%10==0 → true
	assert.True(t, IsValid("18"))
	assert.False(t, IsValid("19")) // 9 + 2 = 11, 11%10 != 0
}

// TestIsValid_LuhnSpecNumber проверяет классический пример из
// описания алгоритма Луна на Википедии: 7992739871 + checksum 3 → 79927398713.
func TestIsValid_LuhnSpecNumber(t *testing.T) {
	// 79927398713 — 11 цифр с контрольной цифрой 3, валидный.
	assert.True(t, IsValid("79927398713"))
	// 79927398710 — контрольная цифра не совпадает, невалидный.
	assert.False(t, IsValid("79927398710"))
	// 7992739871 — 10 цифр без контрольной цифры.
	// Сумма: 1+5+8+9+3+5+2+9+9+5=56, 56%10=6 ≠ 0 → невалидный.
	assert.False(t, IsValid("7992739871"))
}

// TestIsValid_DoublingOverflow проверяет, что удвоение цифры с
// вычитанием 9 при превышении 9 работает корректно.
func TestIsValid_DoublingOverflow(t *testing.T) {
	// "90": справа 0 (normal), 9 (alt: 18→9), sum=0+9=9, 9%10≠0 → false
	assert.False(t, IsValid("90"))
	// "91": справа 1 (normal), 9 (alt: 18→9), sum=1+9=10, 10%10==0 → true
	assert.True(t, IsValid("91"))
}
