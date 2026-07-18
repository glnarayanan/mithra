// Package finance implements deterministic number-only household finance.
package finance

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

const (
	MaxScale       = 6
	maxCoefficient = int64(9_000_000_000_000_000)
)

var (
	ErrBlank    = errors.New("amount is blank")
	ErrAmount   = errors.New("amount is invalid")
	ErrOverflow = errors.New("amount is outside supported bounds")
)

type Decimal struct {
	Coefficient int64
	Scale       uint8
}

func ParseAmount(input string) (Decimal, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return Decimal{}, ErrBlank
	}
	negative := false
	if strings.HasPrefix(value, "(") || strings.HasSuffix(value, ")") {
		if !strings.HasPrefix(value, "(") || !strings.HasSuffix(value, ")") || len(value) < 3 {
			return Decimal{}, ErrAmount
		}
		negative = true
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	if strings.HasPrefix(value, "-") {
		if negative {
			return Decimal{}, ErrAmount
		}
		negative = true
		value = value[1:]
	} else if strings.HasPrefix(value, "+") {
		value = value[1:]
	}
	if value == "" || strings.ContainsAny(value, "+-()") {
		return Decimal{}, ErrAmount
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return Decimal{}, ErrAmount
	}
	integer, ok := parseIntegerPart(parts[0])
	if !ok {
		return Decimal{}, ErrAmount
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > MaxScale || !digitsOnly(fraction) {
			return Decimal{}, ErrAmount
		}
	}
	digits := strings.TrimLeft(integer+fraction, "0")
	if digits == "" {
		digits = "0"
	}
	coefficient, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || coefficient > maxCoefficient {
		return Decimal{}, ErrOverflow
	}
	if negative && coefficient != 0 {
		coefficient = -coefficient
	}
	return Decimal{Coefficient: coefficient, Scale: uint8(len(fraction))}, nil
}

func Add(left, right Decimal) (Decimal, error) {
	scale := left.Scale
	if right.Scale > scale {
		scale = right.Scale
	}
	leftCoefficient, ok := rescale(left, scale)
	if !ok {
		return Decimal{}, ErrOverflow
	}
	rightCoefficient, ok := rescale(right, scale)
	if !ok || rightCoefficient > 0 && leftCoefficient > math.MaxInt64-rightCoefficient || rightCoefficient < 0 && leftCoefficient < math.MinInt64-rightCoefficient {
		return Decimal{}, ErrOverflow
	}
	coefficient := leftCoefficient + rightCoefficient
	if coefficient > maxCoefficient || coefficient < -maxCoefficient {
		return Decimal{}, ErrOverflow
	}
	return Decimal{Coefficient: coefficient, Scale: scale}, nil
}

func Sum(values []Decimal) (Decimal, error) {
	var total Decimal
	for _, value := range values {
		var err error
		total, err = Add(total, value)
		if err != nil {
			return Decimal{}, err
		}
	}
	return total, nil
}

func (value Decimal) PlainString() string {
	negative := value.Coefficient < 0
	coefficient := value.Coefficient
	if negative {
		coefficient = -coefficient
	}
	digits := strconv.FormatInt(coefficient, 10)
	if value.Scale > 0 {
		width := int(value.Scale) + 1
		if len(digits) < width {
			digits = strings.Repeat("0", width-len(digits)) + digits
		}
		point := len(digits) - int(value.Scale)
		digits = digits[:point] + "." + digits[point:]
	}
	if negative && value.Coefficient != 0 {
		return "-" + digits
	}
	return digits
}

func parseIntegerPart(value string) (string, bool) {
	if !strings.Contains(value, ",") {
		return value, digitsOnly(value)
	}
	groups := strings.Split(value, ",")
	if len(groups[0]) < 1 || len(groups[0]) > 3 || !digitsOnly(groups[0]) {
		return "", false
	}
	for _, group := range groups[1:] {
		if len(group) != 3 || !digitsOnly(group) {
			return "", false
		}
	}
	return strings.Join(groups, ""), true
}

func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func rescale(value Decimal, scale uint8) (int64, bool) {
	if value.Scale > scale || scale > MaxScale {
		return 0, false
	}
	coefficient := value.Coefficient
	for current := value.Scale; current < scale; current++ {
		if coefficient > math.MaxInt64/10 || coefficient < math.MinInt64/10 {
			return 0, false
		}
		coefficient *= 10
	}
	return coefficient, true
}
