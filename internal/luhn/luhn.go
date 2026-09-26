package luhn

// IsValid проверяет строку number на соответствие алгоритму Луна.
// Строка должна состоять только из цифр и иметь длину не менее одной цифры.
// Возвращает true, если контрольная сумма проходит проверку, и false в противном случае.
func IsValid(number string) bool {
	if len(number) == 0 {
		return false
	}

	sum := 0
	alt := false

	for i := len(number) - 1; i >= 0; i-- {
		ch := number[i]
		if ch < '0' || ch > '9' {
			return false
		}

		d := int(ch - '0')

		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}

		sum += d
		alt = !alt
	}

	return sum%10 == 0
}
